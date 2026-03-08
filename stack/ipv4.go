package stack

import (
	"net/netip"
)

const (
	ipv4HeaderSize = 20
	ipProtoICMP    = 1
	ipProtoTCP     = 6
	ipProtoUDP     = 17
)

// handleIPv4 processes an incoming IPv4 packet.
func (s *Stack) handleIPv4(pkt []byte, _ []byte) {
	if len(pkt) < ipv4HeaderSize {
		return
	}

	version := pkt[0] >> 4
	if version != 4 {
		return
	}

	ihl := int(pkt[0]&0x0F) * 4
	if ihl < ipv4HeaderSize || ihl > len(pkt) {
		return
	}

	totalLen := int(uint16(pkt[2])<<8 | uint16(pkt[3]))
	if totalLen > len(pkt) {
		totalLen = len(pkt)
	}

	// Verify header checksum
	if checksumIPv4(pkt[:ihl]) != 0 {
		return // bad checksum
	}

	protocol := pkt[9]
	srcIP := netip.AddrFrom4([4]byte{pkt[12], pkt[13], pkt[14], pkt[15]})
	dstIP := netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]})

	// Only process packets addressed to us or broadcast.
	// Accept everything when localIP is unset (pre-DHCP).
	if s.localIP.IsValid() && dstIP != s.localIP && !dstIP.IsMulticast() && dstIP.As4() != [4]byte{255, 255, 255, 255} {
		return
	}

	payload := pkt[ihl:totalLen]

	switch protocol {
	case ipProtoICMP:
		s.handleICMP(srcIP, payload)
	case ipProtoTCP:
		s.handleTCP(srcIP, dstIP, payload)
	case ipProtoUDP:
		s.handleUDP(srcIP, dstIP, payload)
	}
}

// sendIPv4 sends an IPv4 packet. The transport-layer payload must already
// be written into s.txPkt[txTransportOffset:txTransportOffset+payloadLen].
// dstMAC must be obtained from resolve() before calling this.
//
// This writes the IP header at txPkt[ethHeaderSize:] and the Ethernet
// header at txPkt[0:], then sends the complete frame. Zero copies,
// zero allocations.
func (s *Stack) sendIPv4(dstMAC [6]byte, dst netip.Addr, protocol uint8, payloadLen int) error {
	totalLen := ipv4HeaderSize + payloadLen
	if totalLen > MTU {
		return errPacketTooLarge
	}

	// IPv4 header directly in txPkt
	ip := s.txPkt[ethHeaderSize:]
	ip[0] = 0x45 // version 4, IHL 5 (20 bytes)
	ip[1] = 0    // DSCP/ECN
	ip[2] = byte(totalLen >> 8)
	ip[3] = byte(totalLen)
	ip[4] = 0    // ID
	ip[5] = 0
	ip[6] = 0x40 // Don't Fragment
	ip[7] = 0
	ip[8] = 64 // TTL
	ip[9] = protocol
	ip[10] = 0 // checksum (filled below)
	ip[11] = 0

	var src [4]byte
	if s.localIP.IsValid() {
		src = s.localIP.As4()
	}
	copy(ip[12:16], src[:])
	d := dst.As4()
	copy(ip[16:20], d[:])

	// Compute header checksum
	cs := checksumIPv4(ip[:ipv4HeaderSize])
	ip[10] = byte(cs >> 8)
	ip[11] = byte(cs)

	// Ethernet header directly in txPkt
	copy(s.txPkt[0:6], dstMAC[:])
	copy(s.txPkt[6:12], s.mac[:])
	s.txPkt[12] = 0x08
	s.txPkt[13] = 0x00

	return s.dev.SendEth(s.txPkt[:ethHeaderSize+totalLen])
}

// checksumIPv4 computes the RFC 1071 one's complement checksum.
func checksumIPv4(data []byte) uint16 {
	return checksum(data, 0)
}

// checksum computes the Internet checksum over data with an initial value.
func checksum(data []byte, initial uint32) uint16 {
	sum := initial
	n := len(data)
	for i := 0; i+1 < n; i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if n%2 != 0 {
		sum += uint32(data[n-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// pseudoHeaderChecksum computes the TCP/UDP pseudo-header checksum.
func pseudoHeaderChecksum(src, dst netip.Addr, protocol uint8, length uint16) uint32 {
	var s, d [4]byte
	if src.IsValid() {
		s = src.As4()
	}
	if dst.IsValid() {
		d = dst.As4()
	}
	var sum uint32
	sum += uint32(s[0])<<8 | uint32(s[1])
	sum += uint32(s[2])<<8 | uint32(s[3])
	sum += uint32(d[0])<<8 | uint32(d[1])
	sum += uint32(d[2])<<8 | uint32(d[3])
	sum += uint32(protocol)
	sum += uint32(length)
	return sum
}
