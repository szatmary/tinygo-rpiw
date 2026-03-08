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

// mdnsService is the internal representation with pre-computed FQDNs.
type mdnsService struct {
	svc      Service
	svcFQDN  string // e.g. "_hap._tcp.local"
	instFQDN string // e.g. "My Accessory._hap._tcp.local"
}

type mdnsResponder struct {
	stack    *Stack
	hostFQDN string // e.g. "picow.local"
	services [maxServices]mdnsService
	numSvc   int
	buf      [512]byte // shared response buffer
}

func (m *mdnsResponder) init(s *Stack) {
	m.stack = s
}

// SetHostname enables the mDNS responder for "name.local".
func (m *mdnsResponder) SetHostname(name string) {
	m.hostFQDN = name + ".local"
}

// AddService registers a DNS-SD service for advertisement.
// Returns false if the service table is full.
func (m *mdnsResponder) AddService(svc Service) bool {
	if m.numSvc >= maxServices {
		return false
	}
	m.services[m.numSvc] = mdnsService{
		svc:      svc,
		svcFQDN:  svc.Type + ".local",
		instFQDN: svc.Name + "." + svc.Type + ".local",
	}
	m.numSvc++
	return true
}

// handleQuery processes an incoming mDNS query.
func (m *mdnsResponder) handleQuery(_ netip.Addr, data []byte) {
	if m.hostFQDN == "" || !m.stack.localIP.IsValid() {
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
		if matchNameStr(data, nameStart, m.hostFQDN) {
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

	// Check each registered service (no allocations — FQDNs are pre-computed)
	for i := range m.numSvc {
		ms := &m.services[i]

		if qtype == dnsTypePTR || qtype == dnsTypeANY {
			if matchNameStr(data, nameStart, ms.svcFQDN) {
				m.sendPTR(ms)
				return
			}
		}

		if qtype == dnsTypeSRV || qtype == dnsTypeANY {
			if matchNameStr(data, nameStart, ms.instFQDN) {
				m.sendSRV(ms)
				return
			}
		}

		if qtype == dnsTypeTXT || qtype == dnsTypeANY {
			if matchNameStr(data, nameStart, ms.instFQDN) {
				m.sendTXT(ms)
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

		if tpos > 0 {
			if tpos >= len(target) || target[tpos] != '.' {
				return false
			}
			tpos++
		}

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

// --- Response builders (all use m.buf to avoid stack allocations) ---

func writeHeader(buf []byte, anCount uint16) int {
	buf[0] = 0x00 // ID = 0 for mDNS
	buf[1] = 0x00
	buf[2] = 0x84 // flags: QR=1, AA=1
	buf[3] = 0x00
	buf[4] = 0x00 // QDCOUNT=0
	buf[5] = 0x00
	buf[6] = byte(anCount >> 8)
	buf[7] = byte(anCount)
	buf[8] = 0x00 // NSCOUNT=0
	buf[9] = 0x00
	buf[10] = 0x00 // ARCOUNT=0
	buf[11] = 0x00
	return 12
}

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
	buf[i] = 0
	buf[i+1] = 0
	buf[i+2] = byte(mdnsTTL >> 8)
	buf[i+3] = byte(mdnsTTL)
	i += 4
	i += 2 // RDLENGTH placeholder
	return i
}

func (m *mdnsResponder) sendA() {
	buf := m.buf[:]
	i := writeHeader(buf, 1)

	rdataStart := writeRRHeader(buf, i, m.hostFQDN, dnsTypeA, true)
	if rdataStart < 0 {
		return
	}

	ip := m.stack.localIP.As4()
	buf[rdataStart] = ip[0]
	buf[rdataStart+1] = ip[1]
	buf[rdataStart+2] = ip[2]
	buf[rdataStart+3] = ip[3]

	buf[rdataStart-2] = 0
	buf[rdataStart-1] = 4

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:rdataStart+4])
}

func (m *mdnsResponder) sendPTR(ms *mdnsService) {
	buf := m.buf[:]
	i := writeHeader(buf, 1)

	rdataStart := writeRRHeader(buf, i, ms.svcFQDN, dnsTypePTR, false)
	if rdataStart < 0 {
		return
	}

	rdEnd := encodeDNSName(buf, rdataStart, ms.instFQDN)
	if rdEnd < 0 {
		return
	}
	rdlen := rdEnd - rdataStart
	buf[rdataStart-2] = byte(rdlen >> 8)
	buf[rdataStart-1] = byte(rdlen)

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:rdEnd])
}

func (m *mdnsResponder) sendSRV(ms *mdnsService) {
	buf := m.buf[:]
	i := writeHeader(buf, 1)

	rdataStart := writeRRHeader(buf, i, ms.instFQDN, dnsTypeSRV, true)
	if rdataStart < 0 {
		return
	}

	if rdataStart+6 >= len(buf) {
		return
	}

	j := rdataStart
	buf[j] = 0 // priority
	buf[j+1] = 0
	buf[j+2] = 0 // weight
	buf[j+3] = 0
	buf[j+4] = byte(ms.svc.Port >> 8)
	buf[j+5] = byte(ms.svc.Port)
	j += 6

	j = encodeDNSName(buf, j, m.hostFQDN)
	if j < 0 {
		return
	}

	rdlen := j - rdataStart
	buf[rdataStart-2] = byte(rdlen >> 8)
	buf[rdataStart-1] = byte(rdlen)

	m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:j])
}

func (m *mdnsResponder) sendTXT(ms *mdnsService) {
	buf := m.buf[:]
	i := writeHeader(buf, 1)

	rdataStart := writeRRHeader(buf, i, ms.instFQDN, dnsTypeTXT, true)
	if rdataStart < 0 {
		return
	}

	j := rdataStart
	for _, kv := range ms.svc.TXT {
		if j+1+len(kv) > len(buf) {
			return
		}
		buf[j] = byte(len(kv))
		j++
		copy(buf[j:], kv)
		j += len(kv)
	}
	if len(ms.svc.TXT) == 0 {
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

func (m *mdnsResponder) sendServiceEnum() {
	for i := range m.numSvc {
		ms := &m.services[i]

		buf := m.buf[:]
		j := writeHeader(buf, 1)

		rdataStart := writeRRHeader(buf, j, "_services._dns-sd._udp.local", dnsTypePTR, false)
		if rdataStart < 0 {
			return
		}

		rdEnd := encodeDNSName(buf, rdataStart, ms.svcFQDN)
		if rdEnd < 0 {
			return
		}
		rdlen := rdEnd - rdataStart
		buf[rdataStart-2] = byte(rdlen >> 8)
		buf[rdataStart-1] = byte(rdlen)

		m.stack.sendUDP(mdnsPort, mdnsPort, mdnsAddr, buf[:rdEnd])
	}
}
