package stack

import (
	"net/netip"
	"time"
)

const dnsPort = 53

type dnsResolver struct {
	stack    *Stack
	active   bool
	queryID  uint16
	resolved bool
	result   netip.Addr
}

func (d *dnsResolver) init(s *Stack) {
	d.stack = s
	d.queryID = 0x1234
}

// Resolve performs a DNS A record lookup.
// Blocks until resolved or timeout.
func (d *dnsResolver) Resolve(name string, timeout time.Duration) (netip.Addr, error) {
	if !d.stack.dnsServer.IsValid() {
		return netip.Addr{}, errDNSFailed
	}

	d.queryID++
	d.active = true
	d.resolved = false

	// Build DNS query
	pkt := d.buildQuery(name)

	// Send query
	if err := d.stack.sendUDP(dnsPort+1, dnsPort, d.stack.dnsServer, pkt); err != nil {
		d.active = false
		return netip.Addr{}, err
	}

	// Poll for response
	deadline := d.stack.now().Add(timeout)
	for d.stack.now().Before(deadline) {
		if err := d.stack.Poll(); err != nil {
			d.active = false
			return netip.Addr{}, err
		}
		if d.resolved {
			d.active = false
			return d.result, nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Retry once
	d.stack.sendUDP(dnsPort+1, dnsPort, d.stack.dnsServer, pkt)
	deadline = d.stack.now().Add(timeout)
	for d.stack.now().Before(deadline) {
		if err := d.stack.Poll(); err != nil {
			d.active = false
			return netip.Addr{}, err
		}
		if d.resolved {
			d.active = false
			return d.result, nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	d.active = false
	return netip.Addr{}, errDNSFailed
}

func (d *dnsResolver) handleResponse(data []byte) {
	if len(data) < 12 {
		return
	}

	// Check ID
	id := uint16(data[0])<<8 | uint16(data[1])
	if id != d.queryID {
		return
	}

	// Check it's a response (QR=1) and no error (RCODE=0)
	flags := uint16(data[2])<<8 | uint16(data[3])
	if flags&0x8000 == 0 { // not a response
		return
	}
	if flags&0x000F != 0 { // error
		return
	}

	anCount := uint16(data[6])<<8 | uint16(data[7])
	if anCount == 0 {
		return
	}

	// Skip question section
	offset := 12
	offset = skipDNSName(data, offset)
	if offset < 0 || offset+4 > len(data) {
		return
	}
	offset += 4 // skip QTYPE + QCLASS

	// Parse answer records
	for i := 0; i < int(anCount) && offset < len(data); i++ {
		offset = skipDNSName(data, offset)
		if offset < 0 || offset+10 > len(data) {
			return
		}
		atype := uint16(data[offset])<<8 | uint16(data[offset+1])
		// aclass := uint16(data[offset+2])<<8 | uint16(data[offset+3])
		// ttl at offset+4..+7
		rdlen := uint16(data[offset+8])<<8 | uint16(data[offset+9])
		offset += 10

		if offset+int(rdlen) > len(data) {
			return
		}

		if atype == 1 && rdlen == 4 { // A record
			d.result = netip.AddrFrom4([4]byte{
				data[offset], data[offset+1], data[offset+2], data[offset+3],
			})
			d.resolved = true
			return
		}
		offset += int(rdlen)
	}
}

func (d *dnsResolver) buildQuery(name string) []byte {
	// Header (12) + question
	pkt := make([]byte, 0, 64)

	// Header
	pkt = append(pkt, byte(d.queryID>>8), byte(d.queryID))
	pkt = append(pkt, 0x01, 0x00) // flags: RD=1
	pkt = append(pkt, 0x00, 0x01) // QDCOUNT=1
	pkt = append(pkt, 0x00, 0x00) // ANCOUNT=0
	pkt = append(pkt, 0x00, 0x00) // NSCOUNT=0
	pkt = append(pkt, 0x00, 0x00) // ARCOUNT=0

	// Question: encode domain name
	pkt = encodeDNSName(pkt, name)

	// QTYPE: A (1)
	pkt = append(pkt, 0x00, 0x01)
	// QCLASS: IN (1)
	pkt = append(pkt, 0x00, 0x01)

	return pkt
}

// encodeDNSName encodes a domain name into DNS wire format.
func encodeDNSName(buf []byte, name string) []byte {
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			labelLen := i - start
			if labelLen > 0 {
				buf = append(buf, byte(labelLen))
				buf = append(buf, name[start:i]...)
			}
			start = i + 1
		}
	}
	buf = append(buf, 0) // root label
	return buf
}

// skipDNSName advances past a DNS name (handling compression pointers).
func skipDNSName(data []byte, offset int) int {
	for offset < len(data) {
		b := data[offset]
		if b == 0 {
			return offset + 1
		}
		if b&0xC0 == 0xC0 {
			return offset + 2 // pointer
		}
		offset += int(b) + 1
	}
	return -1 // malformed
}
