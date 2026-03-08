package stack

import (
	"net/netip"
	"time"
)

const (
	tcpHeaderSize  = 20
	tcpOptionMSS   = 2
	tcpRetxTimeout = 1 * time.Second
	tcpMaxRetx     = 8
	tcpTimeWaitDur = 2 * time.Second
)

const tcpMaxSegSize = 1460 // used for buffer sizing

var tcpDefaultMSS uint16 = tcpMaxSegSize

// TCP flags
const (
	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpPSH = 0x08
	tcpACK = 0x10
)

// handleTCP processes an incoming TCP segment.
func (s *Stack) handleTCP(srcIP, _ netip.Addr, payload []byte) {
	if len(payload) < tcpHeaderSize {
		return
	}

	srcPort := uint16(payload[0])<<8 | uint16(payload[1])
	dstPort := uint16(payload[2])<<8 | uint16(payload[3])
	seqNum := uint32(payload[4])<<24 | uint32(payload[5])<<16 | uint32(payload[6])<<8 | uint32(payload[7])
	ackNum := uint32(payload[8])<<24 | uint32(payload[9])<<16 | uint32(payload[10])<<8 | uint32(payload[11])
	dataOff := int(payload[12]>>4) * 4
	flags := payload[13]
	window := uint16(payload[14])<<8 | uint16(payload[15])

	if dataOff < tcpHeaderSize || dataOff > len(payload) {
		return
	}

	// Parse MSS option from SYN packets
	mss := uint16(tcpDefaultMSS)
	if flags&tcpSYN != 0 && dataOff > tcpHeaderSize {
		mss = parseMSSOption(payload[tcpHeaderSize:dataOff])
	}

	data := payload[dataOff:]

	// Find matching socket
	sock := s.findTCPSocket(srcIP, srcPort, dstPort)
	if sock == nil {
		// Check for listening socket
		sock = s.findListeningSocket(dstPort)
		if sock == nil {
			// Send RST for unexpected segments
			if flags&tcpRST == 0 {
				s.sendTCPReset(srcIP, srcPort, dstPort, seqNum, ackNum, flags)
			}
			return
		}
		// Handle SYN on listening socket
		if flags&tcpSYN != 0 {
			s.handleSYNOnListener(sock, srcIP, srcPort, dstPort, seqNum, mss)
			return
		}
		return
	}

	s.tcpInput(sock, srcIP, srcPort, seqNum, ackNum, flags, window, mss, data)
}

// tcpInput processes a TCP segment for an existing socket.
func (s *Stack) tcpInput(sock *Socket, _ netip.Addr, _ uint16,
	seqNum, ackNum uint32, flags uint8, window, mss uint16, data []byte) {

	tcp := &sock.tcp

	switch tcp.state {
	case tcpSynSent:
		if flags&tcpSYN != 0 && flags&tcpACK != 0 {
			// SYN-ACK received
			tcp.ackNum = seqNum + 1
			tcp.seqNum = ackNum
			tcp.sndUna = ackNum
			tcp.remoteWin = window
			tcp.mss = mss
			tcp.state = tcpEstablished
			sock.state = sockConnected
			tcp.retxTime = time.Time{} // cancel SYN retransmit
			// Send ACK
			s.sendTCPFlags(sock, tcpACK)
		} else if flags&tcpRST != 0 {
			tcp.state = tcpClosed
			sock.state = sockClosed
		}

	case tcpEstablished:
		// Process ACK
		if flags&tcpACK != 0 {
			s.tcpProcessACK(sock, ackNum)
			tcp.remoteWin = window
		}

		// Process incoming data
		if len(data) > 0 && seqNum == tcp.ackNum {
			n := sock.rxWrite(data)
			tcp.ackNum += uint32(n)
			s.sendTCPFlags(sock, tcpACK)
		}

		// Process FIN
		if flags&tcpFIN != 0 {
			tcp.ackNum = seqNum + uint32(len(data)) + 1
			tcp.finRecvd = true
			tcp.state = tcpCloseWait
			s.sendTCPFlags(sock, tcpACK)
		}

		if flags&tcpRST != 0 {
			tcp.state = tcpClosed
			sock.state = sockClosed
		}

	case tcpFinWait1:
		if flags&tcpACK != 0 {
			s.tcpProcessACK(sock, ackNum)
			if tcp.finSent && ackNum == tcp.seqNum {
				tcp.finAcked = true
				if flags&tcpFIN != 0 {
					tcp.ackNum = seqNum + 1
					tcp.state = tcpTimeWait
					s.sendTCPFlags(sock, tcpACK)
				} else {
					tcp.state = tcpFinWait2
				}
			}
		}
		if flags&tcpFIN != 0 && !tcp.finAcked {
			tcp.ackNum = seqNum + 1
			tcp.state = tcpTimeWait
			s.sendTCPFlags(sock, tcpACK)
		}

	case tcpFinWait2:
		if flags&tcpFIN != 0 {
			tcp.ackNum = seqNum + 1
			tcp.state = tcpTimeWait
			tcp.retxTime = s.now().Add(tcpTimeWaitDur)
			s.sendTCPFlags(sock, tcpACK)
		}

	case tcpCloseWait:
		if flags&tcpACK != 0 {
			s.tcpProcessACK(sock, ackNum)
		}

	case tcpLastAck:
		if flags&tcpACK != 0 {
			tcp.state = tcpClosed
			sock.state = sockClosed
		}

	case tcpSynReceived:
		if flags&tcpACK != 0 {
			tcp.state = tcpEstablished
			sock.state = sockConnected
		}
		if flags&tcpRST != 0 {
			tcp.state = tcpClosed
			sock.state = sockClosed
		}
	}
}

// tcpProcessACK handles ACK number advancement.
func (s *Stack) tcpProcessACK(sock *Socket, ackNum uint32) {
	tcp := &sock.tcp
	acked := int(ackNum - tcp.sndUna)
	if acked > 0 && acked <= sock.txAvailable() {
		sock.txAdvance(acked)
		tcp.sndUna = ackNum
		tcp.retxCount = 0
		if sock.txAvailable() > 0 {
			// Still have unACKed data — restart timer
			tcp.retxTime = s.now().Add(tcpRetxTimeout)
		} else {
			tcp.retxTime = time.Time{}
		}
	}
}

// tcpTimer handles retransmissions and timeouts.
func (s *Stack) tcpTimer(sock *Socket, now time.Time) {
	tcp := &sock.tcp
	if tcp.state == tcpClosed {
		return
	}

	// Time-wait cleanup
	if tcp.state == tcpTimeWait && now.After(tcp.retxTime) {
		tcp.state = tcpClosed
		sock.state = sockClosed
		return
	}

	// Retransmission
	if !tcp.retxTime.IsZero() && now.After(tcp.retxTime) {
		if tcp.retxCount >= tcpMaxRetx {
			tcp.state = tcpClosed
			sock.state = sockClosed
			return
		}
		tcp.retxCount++
		tcp.retxTime = now.Add(tcpRetxTimeout * time.Duration(1<<tcp.retxCount))
		// Rewind seqNum to unACKed position for retransmit
		tcp.seqNum = tcp.sndUna
		s.tcpOutput(sock)
	}
}

// tcpOutput sends pending data or control segments.
func (s *Stack) tcpOutput(sock *Socket) {
	tcp := &sock.tcp

	// Determine what to send
	var seg [tcpMaxSegSize]byte
	sendLen := 0
	flags := uint8(tcpACK)

	switch tcp.state {
	case tcpSynSent:
		flags = tcpSYN
	case tcpEstablished, tcpCloseWait:
		// Send data if available
		maxSend := int(tcp.remoteWin)
		if mss := int(tcp.mss); mss < maxSend {
			maxSend = mss
		}
		if maxSend > len(seg) {
			maxSend = len(seg)
		}
		sendLen = sock.txPeek(seg[:maxSend])
		if sendLen > 0 {
			flags |= tcpPSH
		}
	case tcpFinWait1, tcpLastAck:
		flags |= tcpFIN
		tcp.finSent = true
	}

	s.sendTCPSegment(sock, flags, seg[:sendLen])

	// Set retransmission timer
	if sendLen > 0 || flags&(tcpSYN|tcpFIN) != 0 {
		if tcp.retxTime.IsZero() {
			tcp.retxTime = s.now().Add(tcpRetxTimeout)
		}
	}
}

// sendTCPFlags sends a TCP segment with only flags (no data).
func (s *Stack) sendTCPFlags(sock *Socket, flags uint8) {
	s.sendTCPSegment(sock, flags, nil)
}

// sendTCPSegment builds and sends a TCP segment.
func (s *Stack) sendTCPSegment(sock *Socket, flags uint8, data []byte) {
	tcp := &sock.tcp

	hdrLen := tcpHeaderSize
	// Add MSS option to SYN packets
	if flags&tcpSYN != 0 {
		hdrLen = 24 // 20 + 4 bytes MSS option
	}

	totalLen := hdrLen + len(data)
	pkt := make([]byte, totalLen)

	// Source port
	pkt[0] = byte(sock.localPort >> 8)
	pkt[1] = byte(sock.localPort)
	// Dest port
	pkt[2] = byte(sock.remotePort >> 8)
	pkt[3] = byte(sock.remotePort)
	// Sequence number
	pkt[4] = byte(tcp.seqNum >> 24)
	pkt[5] = byte(tcp.seqNum >> 16)
	pkt[6] = byte(tcp.seqNum >> 8)
	pkt[7] = byte(tcp.seqNum)
	// ACK number
	pkt[8] = byte(tcp.ackNum >> 24)
	pkt[9] = byte(tcp.ackNum >> 16)
	pkt[10] = byte(tcp.ackNum >> 8)
	pkt[11] = byte(tcp.ackNum)
	// Data offset + flags
	pkt[12] = byte(hdrLen/4) << 4
	pkt[13] = flags
	// Window
	win := uint16(sock.rxFree())
	pkt[14] = byte(win >> 8)
	pkt[15] = byte(win)
	// Checksum (filled below)
	pkt[16] = 0
	pkt[17] = 0
	// Urgent pointer
	pkt[18] = 0
	pkt[19] = 0

	// MSS option
	if flags&tcpSYN != 0 {
		pkt[20] = tcpOptionMSS
		pkt[21] = 4
		pkt[22] = byte(tcpDefaultMSS >> 8)
		pkt[23] = byte(tcpDefaultMSS)
	}

	// Data
	copy(pkt[hdrLen:], data)

	// TCP checksum with pseudo-header
	phcs := pseudoHeaderChecksum(sock.localAddr, sock.remoteAddr, ipProtoTCP, uint16(totalLen))
	cs := checksum(pkt, phcs)
	pkt[16] = byte(cs >> 8)
	pkt[17] = byte(cs)

	s.sendIPv4(sock.remoteAddr, ipProtoTCP, pkt)

	// Advance sequence number
	seqAdv := uint32(len(data))
	if flags&tcpSYN != 0 {
		seqAdv++
	}
	if flags&tcpFIN != 0 {
		seqAdv++
	}
	tcp.seqNum += seqAdv
}

// sendTCPReset sends a RST segment.
func (s *Stack) sendTCPReset(dstIP netip.Addr, dstPort, srcPort uint16, seqNum, ackNum uint32, origFlags uint8) {
	var pkt [tcpHeaderSize]byte
	pkt[0] = byte(srcPort >> 8)
	pkt[1] = byte(srcPort)
	pkt[2] = byte(dstPort >> 8)
	pkt[3] = byte(dstPort)

	if origFlags&tcpACK != 0 {
		// Use their ACK as our SEQ
		pkt[4] = byte(ackNum >> 24)
		pkt[5] = byte(ackNum >> 16)
		pkt[6] = byte(ackNum >> 8)
		pkt[7] = byte(ackNum)
		pkt[12] = (tcpHeaderSize / 4) << 4
		pkt[13] = tcpRST
	} else {
		// ACK their SEQ
		ack := seqNum + 1
		pkt[8] = byte(ack >> 24)
		pkt[9] = byte(ack >> 16)
		pkt[10] = byte(ack >> 8)
		pkt[11] = byte(ack)
		pkt[12] = (tcpHeaderSize / 4) << 4
		pkt[13] = tcpRST | tcpACK
	}

	phcs := pseudoHeaderChecksum(s.localIP, dstIP, ipProtoTCP, tcpHeaderSize)
	cs := checksum(pkt[:], phcs)
	pkt[16] = byte(cs >> 8)
	pkt[17] = byte(cs)

	s.sendIPv4(dstIP, ipProtoTCP, pkt[:])
}

// handleSYNOnListener handles incoming SYN on a listening socket.
func (s *Stack) handleSYNOnListener(listener *Socket, srcIP netip.Addr, srcPort, dstPort uint16, seqNum uint32, mss uint16) {
	// Allocate a new socket for this connection
	fd, err := s.allocSocket()
	if err != nil {
		return // no free sockets
	}

	sock := &s.sockets[fd]
	sock.state = sockConnecting
	sock.protocol = protoTCP
	sock.localPort = dstPort
	sock.localAddr = s.localIP
	sock.remotePort = srcPort
	sock.remoteAddr = srcIP

	sock.tcp.state = tcpSynReceived
	sock.tcp.ackNum = seqNum + 1
	sock.tcp.seqNum = s.tcpISN()
	sock.tcp.sndUna = sock.tcp.seqNum
	sock.tcp.remoteWin = uint16(tcpDefaultMSS)
	sock.tcp.mss = mss

	// Send SYN-ACK
	s.sendTCPFlags(sock, tcpSYN|tcpACK)

	// Store pending connection on listener
	listener.pendingConn = fd
}

// findTCPSocket finds a connected TCP socket matching the given tuple.
func (s *Stack) findTCPSocket(remoteIP netip.Addr, remotePort, localPort uint16) *Socket {
	for i := range s.sockets {
		sock := &s.sockets[i]
		if sock.state == sockFree || sock.protocol != protoTCP {
			continue
		}
		if sock.state == sockListening {
			continue
		}
		if sock.localPort == localPort && sock.remotePort == remotePort && sock.remoteAddr == remoteIP {
			return sock
		}
	}
	return nil
}

// findListeningSocket finds a listening socket on the given port.
func (s *Stack) findListeningSocket(localPort uint16) *Socket {
	for i := range s.sockets {
		sock := &s.sockets[i]
		if sock.state == sockListening && sock.localPort == localPort {
			return sock
		}
	}
	return nil
}

// tcpISN generates an initial sequence number.
// Uses a simple counter — not cryptographically secure but sufficient for MCU use.
var tcpISNCounter uint32 = 0x12345678

func (s *Stack) tcpISN() uint32 {
	tcpISNCounter += 64000
	return tcpISNCounter
}

// parseMSSOption extracts MSS from TCP options.
func parseMSSOption(opts []byte) uint16 {
	for i := 0; i < len(opts); {
		kind := opts[i]
		if kind == 0 {
			break // end
		}
		if kind == 1 {
			i++ // NOP
			continue
		}
		if i+1 >= len(opts) {
			break
		}
		optLen := int(opts[i+1])
		if optLen < 2 || i+optLen > len(opts) {
			break
		}
		if kind == tcpOptionMSS && optLen == 4 {
			return uint16(opts[i+2])<<8 | uint16(opts[i+3])
		}
		i += optLen
	}
	return tcpDefaultMSS
}
