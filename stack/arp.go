package stack

import (
	"net/netip"
	"time"
)

const (
	arpOpRequest = 1
	arpOpReply   = 2
	arpHdrSize   = 28 // ARP payload size (for IPv4 over Ethernet)
	arpTTL       = 5 * time.Minute
)

type arpEntry struct {
	ip     netip.Addr
	mac    [6]byte
	expiry time.Time
	valid  bool
}

type arpTable struct {
	entries [ARPTableLen]arpEntry
}

func (t *arpTable) init() {
	for i := range t.entries {
		t.entries[i].valid = false
	}
}

// lookup returns the MAC for an IP, or false if not found.
func (t *arpTable) lookup(ip netip.Addr) ([6]byte, bool) {
	for i := range t.entries {
		e := &t.entries[i]
		if e.valid && e.ip == ip {
			return e.mac, true
		}
	}
	return [6]byte{}, false
}

// update adds or refreshes an ARP entry.
func (t *arpTable) update(ip netip.Addr, mac [6]byte, now time.Time) {
	// Update existing
	for i := range t.entries {
		e := &t.entries[i]
		if e.valid && e.ip == ip {
			e.mac = mac
			e.expiry = now.Add(arpTTL)
			return
		}
	}
	// Find free or oldest entry
	oldest := 0
	for i := range t.entries {
		if !t.entries[i].valid {
			oldest = i
			break
		}
		if t.entries[i].expiry.Before(t.entries[oldest].expiry) {
			oldest = i
		}
	}
	t.entries[oldest] = arpEntry{
		ip:     ip,
		mac:    mac,
		expiry: now.Add(arpTTL),
		valid:  true,
	}
}

// handleARP processes an incoming ARP frame (full Ethernet frame).
func (s *Stack) handleARP(frame []byte) {
	if len(frame) < ethHeaderSize+arpHdrSize {
		return
	}
	arp := frame[ethHeaderSize:]

	// Parse ARP header
	htype := uint16(arp[0])<<8 | uint16(arp[1])
	ptype := uint16(arp[2])<<8 | uint16(arp[3])
	hlen := arp[4]
	plen := arp[5]
	op := uint16(arp[6])<<8 | uint16(arp[7])

	if htype != 1 || ptype != 0x0800 || hlen != 6 || plen != 4 {
		return // not Ethernet/IPv4
	}

	var senderMAC [6]byte
	copy(senderMAC[:], arp[8:14])
	senderIP := netip.AddrFrom4([4]byte{arp[14], arp[15], arp[16], arp[17]})
	targetIP := netip.AddrFrom4([4]byte{arp[24], arp[25], arp[26], arp[27]})

	// Update ARP table with sender info
	s.arp.update(senderIP, senderMAC, s.now())

	// If it's a request for our IP, send reply
	if op == arpOpRequest && targetIP == s.localIP {
		s.sendARPReply(senderMAC, senderIP)
	}
}

// sendARPRequest sends an ARP request for the given IP.
func (s *Stack) sendARPRequest(targetIP netip.Addr) error {
	var arp [arpHdrSize]byte
	// Hardware type: Ethernet (1)
	arp[0] = 0
	arp[1] = 1
	// Protocol type: IPv4
	arp[2] = 0x08
	arp[3] = 0x00
	// Hardware/protocol address lengths
	arp[4] = 6
	arp[5] = 4
	// Operation: request
	arp[6] = 0
	arp[7] = arpOpRequest
	// Sender MAC + IP
	copy(arp[8:14], s.mac[:])
	if s.localIP.IsValid() {
		ip4 := s.localIP.As4()
		copy(arp[14:18], ip4[:])
	}
	// Target MAC (zero for request)
	// Target IP
	tip4 := targetIP.As4()
	copy(arp[24:28], tip4[:])

	broadcast := [6]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	return s.sendEthFrame(broadcast, ethTypeARP, arp[:])
}

// sendARPReply sends an ARP reply.
func (s *Stack) sendARPReply(dstMAC [6]byte, dstIP netip.Addr) {
	var arp [arpHdrSize]byte
	arp[0] = 0
	arp[1] = 1
	arp[2] = 0x08
	arp[3] = 0x00
	arp[4] = 6
	arp[5] = 4
	arp[6] = 0
	arp[7] = arpOpReply
	copy(arp[8:14], s.mac[:])
	if s.localIP.IsValid() {
		ip4 := s.localIP.As4()
		copy(arp[14:18], ip4[:])
	}
	copy(arp[18:24], dstMAC[:])
	dip4 := dstIP.As4()
	copy(arp[24:28], dip4[:])

	s.sendEthFrame(dstMAC, ethTypeARP, arp[:])
}

// multicastMAC returns the Ethernet multicast MAC for an IPv4 multicast address.
// Maps 224.x.y.z → 01:00:5E:(x&0x7F):y:z
func multicastMAC(ip netip.Addr) [6]byte {
	a := ip.As4()
	return [6]byte{0x01, 0x00, 0x5E, a[1] & 0x7F, a[2], a[3]}
}

// resolve resolves an IP to a MAC address. If not in ARP cache,
// sends an ARP request and waits for reply.
func (s *Stack) resolve(ip netip.Addr) ([6]byte, bool) {
	// Broadcast uses broadcast MAC directly
	if ip.As4() == [4]byte{255, 255, 255, 255} {
		return [6]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, true
	}
	// Multicast: derive MAC directly, no ARP needed
	if ip.IsMulticast() {
		return multicastMAC(ip), true
	}
	// If not on same subnet, resolve gateway instead
	if !s.sameSubnet(ip) {
		if !s.gateway.IsValid() {
			return [6]byte{}, false
		}
		ip = s.gateway
	}
	mac, ok := s.arp.lookup(ip)
	if ok {
		return mac, true
	}
	// Send ARP request and wait for reply
	s.sendARPRequest(ip)
	for i := 0; i < 200; i++ { // ~2 seconds
		time.Sleep(10 * time.Millisecond)
		s.dev.Poll()
		mac, ok = s.arp.lookup(ip)
		if ok {
			return mac, true
		}
		if i%50 == 49 { // Retry ARP every 500ms
			s.sendARPRequest(ip)
		}
	}
	return [6]byte{}, false
}
