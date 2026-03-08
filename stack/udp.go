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

	// Check for DHCP (port 68)
	if dstPort == 68 {
		s.dhcp.handlePacket(data)
		return
	}

	// Check for DNS response (from our DNS socket)
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
		// If connected, check remote matches
		if sock.remoteAddr.IsValid() && (sock.remoteAddr != srcIP || sock.remotePort != srcPort) {
			continue
		}
		// Store sender info for unconnected sockets
		if !sock.remoteAddr.IsValid() {
			sock.remoteAddr = srcIP
			sock.remotePort = srcPort
		}
		sock.rxWrite(data)
		return
	}
}

// sendUDP sends a UDP packet.
func (s *Stack) sendUDP(srcPort, dstPort uint16, dst netip.Addr, data []byte) error {
	totalLen := udpHeaderSize + len(data)
	pkt := make([]byte, totalLen)

	pkt[0] = byte(srcPort >> 8)
	pkt[1] = byte(srcPort)
	pkt[2] = byte(dstPort >> 8)
	pkt[3] = byte(dstPort)
	pkt[4] = byte(totalLen >> 8)
	pkt[5] = byte(totalLen)
	pkt[6] = 0 // checksum (optional for UDP over IPv4)
	pkt[7] = 0

	copy(pkt[udpHeaderSize:], data)

	// Compute UDP checksum
	phcs := pseudoHeaderChecksum(s.localIP, dst, ipProtoUDP, uint16(totalLen))
	cs := checksum(pkt, phcs)
	if cs == 0 {
		cs = 0xFFFF
	}
	pkt[6] = byte(cs >> 8)
	pkt[7] = byte(cs)

	return s.sendIPv4(dst, ipProtoUDP, pkt)
}
