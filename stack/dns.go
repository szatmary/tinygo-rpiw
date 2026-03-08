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
// Blocks until resolved or timeout. Retries once on timeout.
func (d *dnsResolver) Resolve(name string, timeout time.Duration) (netip.Addr, error) {
	if !d.stack.dnsServer.IsValid() {
		return netip.Addr{}, errDNSFailed
	}

	d.queryID++
	d.active = true
	d.resolved = false

	// Build DNS query into a fixed buffer (queries are small)
	var queryBuf [128]byte
	queryLen := d.buildQuery(name, queryBuf[:])
	if queryLen == 0 {
		d.active = false
		return netip.Addr{}, errDNSFailed
	}

	// Try up to 2 times (initial + 1 retry)
	for range 2 {
		if err := d.stack.sendUDP(dnsPort+1, dnsPort, d.stack.dnsServer, queryBuf[:queryLen]); err != nil {
			d.active = false
			return netip.Addr{}, err
		}

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
	}

	d.active = false
	return netip.Addr{}, errDNSFailed
}

func (d *dnsResolver) handleResponse(data []byte) {
	if len(data) < 12 {
		return
	}

	id := uint16(data[0])<<8 | uint16(data[1])
	if id != d.queryID {
		return
	}

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

// buildQuery writes a DNS query into buf and returns the length written.
// Returns 0 if the name is too long for the buffer.
// No allocations.
func (d *dnsResolver) buildQuery(name string, buf []byte) int {
	// header(12) + name + label-overhead(2) + null(1) + type/class(4) = 19
	if len(name)+19 > len(buf) {
		return 0
	}
	i := 0

	// Header (12 bytes)
	buf[i] = byte(d.queryID >> 8)
	buf[i+1] = byte(d.queryID)
	i += 2
	buf[i] = 0x01 // flags: RD=1
	buf[i+1] = 0x00
	i += 2
	buf[i] = 0x00 // QDCOUNT=1
	buf[i+1] = 0x01
	i += 2
	buf[i] = 0x00 // ANCOUNT=0
	buf[i+1] = 0x00
	i += 2
	buf[i] = 0x00 // NSCOUNT=0
	buf[i+1] = 0x00
	i += 2
	buf[i] = 0x00 // ARCOUNT=0
	buf[i+1] = 0x00
	i += 2

	// Question: encode domain name
	i = encodeDNSName(buf, i, name)
	if i < 0 {
		return 0
	}

	// QTYPE: A (1)
	buf[i] = 0x00
	buf[i+1] = 0x01
	i += 2
	// QCLASS: IN (1)
	buf[i] = 0x00
	buf[i+1] = 0x01
	i += 2

	return i
}

// encodeDNSName encodes a domain name into DNS wire format at buf[offset:].
// Returns the new offset, or -1 if the name doesn't fit in buf.
func encodeDNSName(buf []byte, offset int, name string) int {
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			labelLen := i - start
			if labelLen > 0 {
				if offset+1+labelLen >= len(buf) {
					return -1
				}
				buf[offset] = byte(labelLen)
				offset++
				copy(buf[offset:], name[start:i])
				offset += labelLen
			}
			start = i + 1
		}
	}
	if offset >= len(buf) {
		return -1
	}
	buf[offset] = 0 // root label
	offset++
	return offset
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
