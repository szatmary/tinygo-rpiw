package stack

import (
	"net/netip"
	"time"
)

const (
	tcpHeaderSize  = 20
	tcpOptionMSS   = 2
	tcpMaxSegSize  = 1460
	tcpRetxTimeout = 1 * time.Second
	tcpMaxRetx     = 8
	tcpTimeWaitDur = 2 * time.Second
)

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
	mss := uint16(tcpMaxSegSize)
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
			if flags&tcpRST == 0 {
				s.sendTCPReset(srcIP, srcPort, dstPort, seqNum, ackNum, flags)
			}
			return
		}
		if flags&tcpSYN != 0 {
			s.handleSYNOnListener(sock, srcIP, srcPort, dstPort, seqNum, mss)
		}
		return
	}

	s.tcpInput(sock, seqNum, ackNum, flags, window, mss, data)
}

// tcpInput processes a TCP segment for an existing socket.
func (s *Stack) tcpInput(sock *Socket,
	seqNum, ackNum uint32, flags uint8, window, mss uint16, data []byte) {

	tcp := &sock.tcp

	switch tcp.state {
	case tcpSynSent:
		if flags&tcpSYN != 0 && flags&tcpACK != 0 {
			tcp.ackNum = seqNum + 1
			tcp.seqNum = ackNum
			tcp.sndUna = ackNum
			tcp.remoteWin = window
			tcp.mss = mss
			tcp.state = tcpEstablished
			sock.state = sockConnected
			tcp.retxTime = time.Time{}
			s.sendTCPFlags(sock, tcpACK)
		} else if flags&tcpRST != 0 {
			tcp.state = tcpClosed
			sock.state = sockClosed
		}

	case tcpEstablished:
		if flags&tcpACK != 0 {
			s.tcpProcessACK(sock, ackNum)
			tcp.remoteWin = window
		}
		if len(data) > 0 && seqNum == tcp.ackNum {
			n := sock.rxWrite(data)
			tcp.ackNum += uint32(n)
			s.sendTCPFlags(sock, tcpACK)
		}
		// Only process FIN if segment is in sequence (all prior data received)
		if flags&tcpFIN != 0 && seqNum+uint32(len(data)) == tcp.ackNum {
			tcp.ackNum++
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
		if flags&tcpFIN != 0 && !tcp.finAcked && seqNum == tcp.ackNum {
			tcp.ackNum = seqNum + 1
			tcp.state = tcpTimeWait
			s.sendTCPFlags(sock, tcpACK)
		}

	case tcpFinWait2:
		if flags&tcpFIN != 0 && seqNum == tcp.ackNum {
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

	if tcp.state == tcpTimeWait && now.After(tcp.retxTime) {
		tcp.state = tcpClosed
		sock.state = sockClosed
		return
	}

	if !tcp.retxTime.IsZero() && now.After(tcp.retxTime) {
		if tcp.retxCount >= tcpMaxRetx {
			tcp.state = tcpClosed
			sock.state = sockClosed
			return
		}
		tcp.retxCount++
		tcp.retxTime = now.Add(tcpRetxTimeout * time.Duration(1<<tcp.retxCount))
		tcp.seqNum = tcp.sndUna
		s.tcpOutput(sock)
	}
}

// tcpOutput sends pending data or control segments.
// Writes transport payload directly into txPkt — zero allocations.
func (s *Stack) tcpOutput(sock *Socket) {
	tcp := &sock.tcp
	flags := uint8(tcpACK)

	hdrLen := tcpHeaderSize
	switch tcp.state {
	case tcpSynSent:
		flags = tcpSYN
		hdrLen = 24 // 20 + 4 bytes MSS option
	case tcpFinWait1, tcpLastAck:
		flags |= tcpFIN
		tcp.finSent = true
	}

	// Resolve MAC first — this might use txPkt for ARP, but
	// after it returns, txPkt is safe to write into.
	dstMAC, ok := s.resolve(sock.remoteAddr)
	if !ok {
		return
	}

	// Peek data directly into txPkt (no intermediate buffer)
	sendLen := 0
	if tcp.state == tcpEstablished || tcp.state == tcpCloseWait {
		maxSend := int(tcp.remoteWin)
		if mss := int(tcp.mss); mss < maxSend {
			maxSend = mss
		}
		maxPayload := MTU - ipv4HeaderSize - hdrLen
		if maxSend > maxPayload {
			maxSend = maxPayload
		}
		if maxSend > 0 {
			dataStart := txTransportOffset + hdrLen
			sendLen = sock.txPeek(s.txPkt[dataStart : dataStart+maxSend])
			if sendLen > 0 {
				flags |= tcpPSH
			}
		}
	}

	s.sendTCP(sock, dstMAC, flags, sendLen)

	if sendLen > 0 || flags&(tcpSYN|tcpFIN) != 0 {
		if tcp.retxTime.IsZero() {
			tcp.retxTime = s.now().Add(tcpRetxTimeout)
		}
	}
}

// sendTCPFlags sends a TCP segment with only flags (no data).
func (s *Stack) sendTCPFlags(sock *Socket, flags uint8) {
	dstMAC, ok := s.resolve(sock.remoteAddr)
	if !ok {
		return
	}
	s.sendTCP(sock, dstMAC, flags, 0)
}

// sendTCP builds a TCP header at txPkt[txTransportOffset:], computes
// the checksum, and sends via sendIPv4. payloadLen bytes of data must
// already be at txPkt[txTransportOffset+hdrLen:].
// Zero allocations, zero intermediate copies.
func (s *Stack) sendTCP(sock *Socket, dstMAC [6]byte, flags uint8, payloadLen int) {
	tcp := &sock.tcp
	hdrLen := tcpHeaderSize
	if flags&tcpSYN != 0 {
		hdrLen = 24
	}

	buf := s.txPkt[txTransportOffset:]
	buf[0] = byte(sock.localPort >> 8)
	buf[1] = byte(sock.localPort)
	buf[2] = byte(sock.remotePort >> 8)
	buf[3] = byte(sock.remotePort)
	buf[4] = byte(tcp.seqNum >> 24)
	buf[5] = byte(tcp.seqNum >> 16)
	buf[6] = byte(tcp.seqNum >> 8)
	buf[7] = byte(tcp.seqNum)
	buf[8] = byte(tcp.ackNum >> 24)
	buf[9] = byte(tcp.ackNum >> 16)
	buf[10] = byte(tcp.ackNum >> 8)
	buf[11] = byte(tcp.ackNum)
	buf[12] = byte(hdrLen/4) << 4
	buf[13] = flags
	win := uint16(sock.rxFree())
	buf[14] = byte(win >> 8)
	buf[15] = byte(win)
	buf[16] = 0 // checksum
	buf[17] = 0
	buf[18] = 0 // urgent
	buf[19] = 0

	if flags&tcpSYN != 0 {
		mss := uint16(tcpMaxSegSize)
		buf[20] = tcpOptionMSS
		buf[21] = 4
		buf[22] = byte(mss >> 8)
		buf[23] = byte(mss)
	}

	totalLen := hdrLen + payloadLen
	phcs := pseudoHeaderChecksum(sock.localAddr, sock.remoteAddr, ipProtoTCP, uint16(totalLen))
	cs := checksum(buf[:totalLen], phcs)
	buf[16] = byte(cs >> 8)
	buf[17] = byte(cs)

	s.sendIPv4(dstMAC, sock.remoteAddr, ipProtoTCP, totalLen)

	seqAdv := uint32(payloadLen)
	if flags&tcpSYN != 0 {
		seqAdv++
	}
	if flags&tcpFIN != 0 {
		seqAdv++
	}
	tcp.seqNum += seqAdv
}

// sendTCPReset sends a RST segment. Writes directly into txPkt.
func (s *Stack) sendTCPReset(dstIP netip.Addr, dstPort, srcPort uint16, seqNum, ackNum uint32, origFlags uint8) {
	dstMAC, ok := s.resolve(dstIP)
	if !ok {
		return
	}

	buf := s.txPkt[txTransportOffset:]
	buf[0] = byte(srcPort >> 8)
	buf[1] = byte(srcPort)
	buf[2] = byte(dstPort >> 8)
	buf[3] = byte(dstPort)

	// Clear remaining header bytes
	for i := 4; i < tcpHeaderSize; i++ {
		buf[i] = 0
	}

	if origFlags&tcpACK != 0 {
		buf[4] = byte(ackNum >> 24)
		buf[5] = byte(ackNum >> 16)
		buf[6] = byte(ackNum >> 8)
		buf[7] = byte(ackNum)
		buf[12] = (tcpHeaderSize / 4) << 4
		buf[13] = tcpRST
	} else {
		ack := seqNum + 1
		buf[8] = byte(ack >> 24)
		buf[9] = byte(ack >> 16)
		buf[10] = byte(ack >> 8)
		buf[11] = byte(ack)
		buf[12] = (tcpHeaderSize / 4) << 4
		buf[13] = tcpRST | tcpACK
	}

	phcs := pseudoHeaderChecksum(s.localIP, dstIP, ipProtoTCP, tcpHeaderSize)
	cs := checksum(buf[:tcpHeaderSize], phcs)
	buf[16] = byte(cs >> 8)
	buf[17] = byte(cs)

	s.sendIPv4(dstMAC, dstIP, ipProtoTCP, tcpHeaderSize)
}

// handleSYNOnListener handles incoming SYN on a listening socket.
func (s *Stack) handleSYNOnListener(listener *Socket, srcIP netip.Addr, srcPort, dstPort uint16, seqNum uint32, mss uint16) {
	fd, err := s.allocSocket()
	if err != nil {
		s.sendTCPReset(srcIP, srcPort, dstPort, seqNum, 0, tcpSYN)
		return
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
	sock.tcp.remoteWin = uint16(tcpMaxSegSize)
	sock.tcp.mss = mss

	s.sendTCPFlags(sock, tcpSYN|tcpACK)

	listener.pendingConn = fd
}

// findTCPSocket finds a connected TCP socket matching the given tuple.
func (s *Stack) findTCPSocket(remoteIP netip.Addr, remotePort, localPort uint16) *Socket {
	for i := range s.sockets {
		sock := &s.sockets[i]
		if sock.state == sockFree || sock.protocol != protoTCP || sock.state == sockListening {
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

// parseMSSOption extracts MSS from TCP options.
func parseMSSOption(opts []byte) uint16 {
	for i := 0; i < len(opts); {
		kind := opts[i]
		if kind == 0 {
			break
		}
		if kind == 1 {
			i++
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
	return tcpMaxSegSize
}
