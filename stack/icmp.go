package stack

import "net/netip"

const (
	icmpTypeEchoReply   = 0
	icmpTypeEchoRequest = 8
	icmpHeaderSize      = 8
)

// handleICMP processes an incoming ICMP packet.
func (s *Stack) handleICMP(srcIP netip.Addr, payload []byte) {
	if len(payload) < icmpHeaderSize {
		return
	}

	switch payload[0] {
	case icmpTypeEchoRequest:
		s.sendICMPEchoReply(srcIP, payload)
	case icmpTypeEchoReply:
		s.pingRecvd = true
	}
}

// SendPing sends an ICMP echo request. Writes directly into txPkt.
func (s *Stack) SendPing(dst netip.Addr) error {
	dstMAC, ok := s.resolve(dst)
	if !ok {
		return errNoRoute
	}

	buf := s.txPkt[txTransportOffset:]
	buf[0] = icmpTypeEchoRequest
	buf[1] = 0 // code
	buf[2] = 0 // checksum (filled below)
	buf[3] = 0
	buf[4] = 0x12 // ID
	buf[5] = 0x34
	buf[6] = 0x00 // Seq
	buf[7] = 0x01
	buf[8] = 'p' // Data
	buf[9] = 'i'
	buf[10] = 'n'
	buf[11] = 'g'

	cs := checksum(buf[:12], 0)
	buf[2] = byte(cs >> 8)
	buf[3] = byte(cs)

	s.pingRecvd = false
	return s.sendIPv4(dstMAC, dst, ipProtoICMP, 12)
}

// PingResult returns true if a ping reply was received.
func (s *Stack) PingResult() bool {
	return s.pingRecvd
}

// sendICMPEchoReply responds to a ping request.
// Writes directly into txPkt — zero allocations.
func (s *Stack) sendICMPEchoReply(dst netip.Addr, request []byte) {
	dstMAC, ok := s.resolve(dst)
	if !ok {
		return
	}

	n := len(request)
	if n > MTU-ipv4HeaderSize {
		n = MTU - ipv4HeaderSize
	}

	buf := s.txPkt[txTransportOffset:]
	copy(buf[:n], request)

	buf[0] = icmpTypeEchoReply
	buf[1] = 0
	buf[2] = 0
	buf[3] = 0

	cs := checksum(buf[:n], 0)
	buf[2] = byte(cs >> 8)
	buf[3] = byte(cs)

	s.sendIPv4(dstMAC, dst, ipProtoICMP, n)
}
