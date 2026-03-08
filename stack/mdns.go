package stack

import "net/netip"

const (
	mdnsPort = 5353
	mdnsTTL  = 120 // seconds

	dnsTypeA   = 1
	dnsTypePTR = 12
	dnsTypeTXT = 16
	dnsTypeSRV = 33
	dnsTypeANY = 255

	maxServices = 4
)

var mdnsAddr = netip.AddrFrom4([4]byte{224, 0, 0, 251})

// MdnsMulticastMAC is the Ethernet multicast MAC for mDNS (01:00:5E:00:00:FB).
var MdnsMulticastMAC = [6]byte{0x01, 0x00, 0x5E, 0x00, 0x00, 0xFB}

// Service describes a DNS-SD service to advertise via mDNS.
type Service struct {
	Name string   // Instance name (e.g., "My Accessory")
	Type string   // Service type (e.g., "_hap._tcp")
	Port uint16   // Service port
	TXT  []string // Key=value pairs (e.g., "c#=1", "sf=1")
}

type mdnsResponder struct {
	stack    *Stack
	hostname string
	services [maxServices]Service
	numSvc   int
}

func (m *mdnsResponder) init(s *Stack) {
	m.stack = s
}

// SetHostname enables the mDNS responder for "name.local".
func (m *mdnsResponder) SetHostname(name string) {
	m.hostname = name
}

// AddService registers a DNS-SD service for advertisement.
// Returns false if the service table is full.
func (m *mdnsResponder) AddService(svc Service) bool {
	if m.numSvc >= maxServices {
		return false
	}
	m.services[m.numSvc] = svc
	m.numSvc++
	return true
}

// handleQuery processes an incoming mDNS query.
func (m *mdnsResponder) handleQuery(_ netip.Addr, data []byte) {
	if m.hostname == "" || !m.stack.localIP.IsValid() {
		return
	}
	if len(data) < 12 {
		return
	}

	flags := uint16(data[2])<<8 | uint16(data[3])
	if flags&0x8000 != 0 {
		return // response, not query
	}

	qdCount := uint16(data[4])<<8 | uint16(data[5])
	if qdCount == 0 {
		return
	}

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

		if qclass&0x7FFF != 1 { // not class IN
			continue
		}

		m.handleQuestion(data, nameStart, qtype)
	}
}

func (m *mdnsResponder) handleQuestion(data []byte, nameStart int, qtype uint16) {
	// A record: hostname.local
	if qtype == dnsTypeA || qtype == dnsTypeANY {
		if matchNameStr(data, nameStart, m.hostname+".local") {
			m.sendA()
			return
		}
	}

	// DNS-SD service type enumeration: _services._dns-sd._udp.local
	if qtype == dnsTypePTR || qtype == dnsTypeANY {
		if matchNameStr(data, nameStart, "_services._dns-sd._udp.local") {
			m.sendServiceEnum()
			return
		}
	}

	// Check each registered service
	for i := range m.numSvc {
		svc := &m.services[i]
		svcFQDN := svc.Type + ".local"              // e.g., _hap._tcp.local
		instFQDN := svc.Name + "." + svc.Type + ".local" // e.g., My Accessory._hap._tcp.local

		// PTR: _hap._tcp.local → instance name
		if qtype == dnsTypePTR || qtype == dnsTypeANY {
			if matchNameStr(data, nameStart, svcFQDN) {
				m.sendPTR(svc)
				return
			}
		}

		// SRV: instance._hap._tcp.local → host + port
		if qtype == dnsTypeSRV || qtype == dnsTypeANY {
			if matchNameStr(data, nameStart, instFQDN) {
				m.sendSRV(svc)
				return
			}
		}

		// TXT: instance._hap._tcp.local → key=value pairs
		if qtype == dnsTypeTXT || qtype == dnsTypeANY {
			if matchNameStr(data, nameStart, instFQDN) {
				m.sendTXT(svc)
				return
			}
		}
	}
}

// matchNameStr does case-insensitive comparison of a DNS wire-format name
// against a dot-separated string like "My Accessory._hap._tcp.local".
func matchNameStr(data []byte, offset int, target string) bool {
	tpos := 0
	for offset < len(data) {
		labelLen := int(data[offset])
		if labelLen == 0 {
			return tpos == len(target) // both at end
		}
		if labelLen&0xC0 == 0xC0 {
			return false // don't follow compression pointers
		}
		offset++

		// Check separator: if we're not at the start, target should have a dot
		if tpos > 0 {
			if tpos >= len(target) || target[tpos] != '.' {
				return false
			}
			tpos++ // skip the dot
		}

		// Compare label bytes
		if tpos+labelLen > len(target) {
			return false
		}
		if offset+labelLen > len(data) {
			return false
		}
		for j := range labelLen {
			a := data[offset+j]
			b := target[tpos+j]
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
		tpos += labelLen
	}
	return false
}

// --- Response builders ---

// writeHeader writes the mDNS response header and returns the offset after it.
// anCount is the number of answer records.
func writeHeader(buf []byte, anCount uint16) int {
	buf[0] = 0x00 // ID = 0 for mDNS
	buf[1] = 0x00
	buf[2] = 0x84 // flags: QR=1, AA=1
	buf[3] = 0x00
	buf[4] = 0x00 // QDCOUNT=0
	buf[5] = 0x00
	buf[6] = byte(anCount >> 8) // ANCOUNT
	buf[7] = byte(anCount)
	buf[8] = 0x00 // NSCOUNT=0
	buf[9] = 0x00
	buf[10] = 0x00 // ARCOUNT=0
	buf[11] = 0x00
	return 12
}

// writeRRHeader writes the common part of a resource record (name, type, class, TTL)
// and reserves space for RDLENGTH. Returns offset of the RDATA start, or -1.
// The caller must fill in RDLENGTH at (returned offset - 2).
func writeRRHeader(buf []byte, i int, name string, rrtype uint16, cacheFlush bool) int {
	i = encodeDNSName(buf, i, name)
	if i < 0 || i+10 > len(buf) {
		return -1
	}
	buf[i] = byte(rrtype >> 8)
	buf[i+1] = byte(rrtype)
	i += 2
	cls := uint16(1) // IN
	if cacheFlush {
		cls |= 0x8000
	}
	buf[i] = byte(cls >> 8)
	buf[i+1] = byte(cls)
	i += 2
	// TTL
	buf[i] = 0
	buf[i+1] = 0
	buf[i+2] = byte(mdnsTTL >> 8)
	buf[i+3] = byte(mdnsTTL)
	i += 4
	// RDLENGTH placeholder (2 bytes)
	i += 2
	return i
}

func (m *mdnsResponder) sendA() {
	var buf [256]byte
	i := writeHeader(buf[:], 1)

	rdataStart := writeRRHeader(buf[:], i, m.hostname+".local", dnsTypeA, true)
	if rdataStart < 0 {
		return
	}

	// RDATA: 4-byte IPv4 address
	ip := m.stack.localIP.As4()
	buf[rdataStart] = ip[0]
	buf[rdataStart+1] = ip[1]
	buf[rdataStart+2] = ip[2]
	buf[rdataStart+3] = ip[3]
	rdlen := 4

	// Fill RDLENGTH
	buf[rdataStart-2] = byte(rdlen >> 8)
	buf[rdataStart-1] = byte(rdlen)

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:rdataStart+rdlen])
}

func (m *mdnsResponder) sendPTR(svc *Service) {
	var buf [512]byte
	i := writeHeader(buf[:], 1)

	rdataStart := writeRRHeader(buf[:], i, svc.Type+".local", dnsTypePTR, false)
	if rdataStart < 0 {
		return
	}

	// RDATA: instance FQDN
	rdEnd := encodeDNSName(buf[:], rdataStart, svc.Name+"."+svc.Type+".local")
	if rdEnd < 0 {
		return
	}
	rdlen := rdEnd - rdataStart
	buf[rdataStart-2] = byte(rdlen >> 8)
	buf[rdataStart-1] = byte(rdlen)

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:rdEnd])
}

func (m *mdnsResponder) sendSRV(svc *Service) {
	var buf [512]byte
	i := writeHeader(buf[:], 1)

	rdataStart := writeRRHeader(buf[:], i, svc.Name+"."+svc.Type+".local", dnsTypeSRV, true)
	if rdataStart < 0 {
		return
	}

	if rdataStart+6 >= len(buf) {
		return
	}

	// RDATA: priority(2) + weight(2) + port(2) + target
	j := rdataStart
	buf[j] = 0 // priority
	buf[j+1] = 0
	buf[j+2] = 0 // weight
	buf[j+3] = 0
	buf[j+4] = byte(svc.Port >> 8)
	buf[j+5] = byte(svc.Port)
	j += 6

	j = encodeDNSName(buf[:], j, m.hostname+".local")
	if j < 0 {
		return
	}

	rdlen := j - rdataStart
	buf[rdataStart-2] = byte(rdlen >> 8)
	buf[rdataStart-1] = byte(rdlen)

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:j])
}

func (m *mdnsResponder) sendTXT(svc *Service) {
	var buf [512]byte
	i := writeHeader(buf[:], 1)

	rdataStart := writeRRHeader(buf[:], i, svc.Name+"."+svc.Type+".local", dnsTypeTXT, true)
	if rdataStart < 0 {
		return
	}

	// RDATA: sequence of length-prefixed strings
	j := rdataStart
	for _, kv := range svc.TXT {
		if j+1+len(kv) > len(buf) {
			return
		}
		buf[j] = byte(len(kv))
		j++
		copy(buf[j:], kv)
		j += len(kv)
	}
	// If no TXT records, write a single zero-length string
	if len(svc.TXT) == 0 {
		if j >= len(buf) {
			return
		}
		buf[j] = 0
		j++
	}

	rdlen := j - rdataStart
	buf[rdataStart-2] = byte(rdlen >> 8)
	buf[rdataStart-1] = byte(rdlen)

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:j])
}

// sendServiceEnum responds to _services._dns-sd._udp.local PTR queries
// with one PTR record per registered service type.
func (m *mdnsResponder) sendServiceEnum() {
	for i := range m.numSvc {
		svc := &m.services[i]

		var buf [512]byte
		j := writeHeader(buf[:], 1)

		rdataStart := writeRRHeader(buf[:], j, "_services._dns-sd._udp.local", dnsTypePTR, false)
		if rdataStart < 0 {
			return
		}

		rdEnd := encodeDNSName(buf[:], rdataStart, svc.Type+".local")
		if rdEnd < 0 {
			return
		}
		rdlen := rdEnd - rdataStart
		buf[rdataStart-2] = byte(rdlen >> 8)
		buf[rdataStart-1] = byte(rdlen)

		m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:rdEnd])
	}
}
