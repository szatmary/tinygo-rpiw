package stack

import "net/netip"

const (
	icmpTypeEchoReply   = 0
	icmpTypeEchoRequest = 8
	icmpHeaderSize      = 8
)

// pingResult stores the ping response state.
var pingGot bool

// handleICMP processes an incoming ICMP packet.
func (s *Stack) handleICMP(srcIP netip.Addr, payload []byte) {
	if len(payload) < icmpHeaderSize {
		return
	}

	icmpType := payload[0]

	switch icmpType {
	case icmpTypeEchoRequest:
		s.sendICMPEchoReply(srcIP, payload)
	case icmpTypeEchoReply:
		pingGot = true
	}
}

// SendPing sends an ICMP echo request to the given IP.
func (s *Stack) SendPing(dst netip.Addr) error {
	var pkt [16]byte
	pkt[0] = icmpTypeEchoRequest
	pkt[1] = 0 // code
	// ID
	pkt[4] = 0x12
	pkt[5] = 0x34
	// Seq
	pkt[6] = 0x00
	pkt[7] = 0x01
	// Data
	pkt[8] = 'p'
	pkt[9] = 'i'
	pkt[10] = 'n'
	pkt[11] = 'g'

	// Checksum
	pkt[2] = 0
	pkt[3] = 0
	cs := checksum(pkt[:12], 0)
	pkt[2] = byte(cs >> 8)
	pkt[3] = byte(cs)

	pingGot = false
	return s.sendIPv4(dst, ipProtoICMP, pkt[:12])
}

// PingResult returns true if a ping reply was received.
func (s *Stack) PingResult() bool {
	return pingGot
}

// sendICMPEchoReply responds to a ping request.
func (s *Stack) sendICMPEchoReply(dst netip.Addr, request []byte) {
	// Build reply: change type to 0, recalculate checksum
	reply := make([]byte, len(request))
	copy(reply, request)

	reply[0] = icmpTypeEchoReply
	reply[1] = 0 // code
	reply[2] = 0 // zero checksum before computing
	reply[3] = 0

	cs := checksum(reply, 0)
	reply[2] = byte(cs >> 8)
	reply[3] = byte(cs)

	s.sendIPv4(dst, ipProtoICMP, reply)
}
