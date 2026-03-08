package stack

import "net/netip"

const (
	mdnsPort = 5353
	mdnsTTL  = 120 // seconds
)

var mdnsAddr = netip.AddrFrom4([4]byte{224, 0, 0, 251})

// MdnsMulticastMAC is the Ethernet multicast MAC for mDNS (01:00:5E:00:00:FB).
// The caller should add this to the hardware multicast filter.
var MdnsMulticastMAC = [6]byte{0x01, 0x00, 0x5E, 0x00, 0x00, 0xFB}

type mdnsResponder struct {
	stack    *Stack
	hostname string // without ".local" suffix
}

func (m *mdnsResponder) init(s *Stack) {
	m.stack = s
}

// SetHostname enables the mDNS responder for "name.local".
func (m *mdnsResponder) SetHostname(name string) {
	m.hostname = name
}

// handleQuery checks if the incoming DNS query is for our hostname.local
// and responds with our IP address.
func (m *mdnsResponder) handleQuery(_ netip.Addr, data []byte) {
	if m.hostname == "" || !m.stack.localIP.IsValid() {
		return
	}
	if len(data) < 12 {
		return
	}

	flags := uint16(data[2])<<8 | uint16(data[3])
	if flags&0x8000 != 0 {
		return // it's a response, not a query
	}

	qdCount := uint16(data[4])<<8 | uint16(data[5])
	if qdCount == 0 {
		return
	}

	// Parse question(s) looking for our hostname.local
	offset := 12
	for i := 0; i < int(qdCount) && offset < len(data); i++ {
		nameStart := offset
		offset = skipDNSName(data, offset)
		if offset < 0 || offset+4 > len(data) {
			return
		}
		qtype := uint16(data[offset])<<8 | uint16(data[offset+1])
		qclass := uint16(data[offset+2])<<8 | uint16(data[offset+3])
		offset += 4

		// Match A record (1) or ANY (255), class IN (1) — ignore unicast bit
		if (qtype != 1 && qtype != 255) || (qclass&0x7FFF) != 1 {
			continue
		}

		if m.matchName(data, nameStart) {
			m.sendResponse(data[:2]) // pass query ID
			return
		}
	}
}

// matchName checks if the DNS name at offset matches "hostname.local".
func (m *mdnsResponder) matchName(data []byte, offset int) bool {
	// First label: hostname
	if offset >= len(data) {
		return false
	}
	labelLen := int(data[offset])
	offset++
	if labelLen != len(m.hostname) {
		return false
	}
	if offset+labelLen >= len(data) {
		return false
	}
	for i := range labelLen {
		a := data[offset+i]
		b := m.hostname[i]
		// Case-insensitive compare
		if a >= 'A' && a <= 'Z' {
			a += 32
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		if a != b {
			return false
		}
	}
	offset += labelLen

	// Second label: "local"
	if offset >= len(data) || data[offset] != 5 {
		return false
	}
	offset++
	if offset+5 >= len(data) {
		return false
	}
	local := "local"
	for i := range 5 {
		a := data[offset+i]
		b := local[i]
		if a >= 'A' && a <= 'Z' {
			a += 32
		}
		if a != b {
			return false
		}
	}
	offset += 5

	// Root label
	if offset >= len(data) {
		return false
	}
	return data[offset] == 0
}

// sendResponse builds and sends an mDNS A record response.
func (m *mdnsResponder) sendResponse(queryID []byte) {
	var buf [256]byte
	i := 0

	// Header
	buf[i] = queryID[0] // ID
	buf[i+1] = queryID[1]
	i += 2
	buf[i] = 0x84 // flags: QR=1, AA=1
	buf[i+1] = 0x00
	i += 2
	buf[i] = 0x00 // QDCOUNT=0
	buf[i+1] = 0x00
	i += 2
	buf[i] = 0x00 // ANCOUNT=1
	buf[i+1] = 0x01
	i += 2
	buf[i] = 0x00 // NSCOUNT=0
	buf[i+1] = 0x00
	i += 2
	buf[i] = 0x00 // ARCOUNT=0
	buf[i+1] = 0x00
	i += 2

	// Answer: hostname.local A record
	i = encodeDNSName(buf[:], i, m.hostname+".local")
	if i < 0 {
		return
	}

	// TYPE: A (1)
	buf[i] = 0x00
	buf[i+1] = 0x01
	i += 2
	// CLASS: IN (1) with cache-flush bit set
	buf[i] = 0x80
	buf[i+1] = 0x01
	i += 2
	// TTL
	buf[i] = 0
	buf[i+1] = 0
	buf[i+2] = byte(mdnsTTL >> 8)
	buf[i+3] = byte(mdnsTTL)
	i += 4
	// RDLENGTH: 4
	buf[i] = 0x00
	buf[i+1] = 0x04
	i += 2
	// RDATA: IPv4 address
	ip := m.stack.localIP.As4()
	buf[i] = ip[0]
	buf[i+1] = ip[1]
	buf[i+2] = ip[2]
	buf[i+3] = ip[3]
	i += 4

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:i])
}
