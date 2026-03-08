package stack

import "net/netip"

const udpHeaderSize = 8

// handleUDP processes an incoming UDP packet.
func (s *Stack) handleUDP(srcIP, _ netip.Addr, payload []byte) {
	if len(payload) < udpHeaderSize {
		return
	}

	srcPort := uint16(payload[0])<<8 | uint16(payload[1])
	dstPort := uint16(payload[2])<<8 | uint16(payload[3])
	data := payload[udpHeaderSize:]

	if dstPort == 68 {
		s.dhcp.handlePacket(data)
		return
	}

	if s.dns.active && srcPort == 53 {
		s.dns.handleResponse(data)
		return
	}

	// Dispatch to socket
	for i := range s.sockets {
		sock := &s.sockets[i]
		if sock.state == sockFree || sock.protocol != protoUDP {
			continue
		}
		if sock.localPort != dstPort {
			continue
		}
		if sock.remoteAddr.IsValid() && (sock.remoteAddr != srcIP || sock.remotePort != srcPort) {
			continue
		}
		if !sock.remoteAddr.IsValid() {
			sock.remoteAddr = srcIP
			sock.remotePort = srcPort
		}
		sock.rxWrite(data)
		return
	}
}

// sendUDP sends a UDP packet. Writes directly into txPkt — zero allocations.
func (s *Stack) sendUDP(srcPort, dstPort uint16, dst netip.Addr, data []byte) error {
	dstMAC, ok := s.resolve(dst)
	if !ok {
		return errNoRoute
	}

	totalLen := udpHeaderSize + len(data)
	buf := s.txPkt[txTransportOffset:]

	buf[0] = byte(srcPort >> 8)
	buf[1] = byte(srcPort)
	buf[2] = byte(dstPort >> 8)
	buf[3] = byte(dstPort)
	buf[4] = byte(totalLen >> 8)
	buf[5] = byte(totalLen)
	buf[6] = 0 // checksum (filled below)
	buf[7] = 0

	copy(buf[udpHeaderSize:], data)

	phcs := pseudoHeaderChecksum(s.localIP, dst, ipProtoUDP, uint16(totalLen))
	cs := checksum(buf[:totalLen], phcs)
	if cs == 0 {
		cs = 0xFFFF
	}
	buf[6] = byte(cs >> 8)
	buf[7] = byte(cs)

	return s.sendIPv4(dstMAC, dst, ipProtoUDP, totalLen)
}
