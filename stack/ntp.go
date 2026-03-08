package stack

import (
	"net/netip"
	"time"
)

const (
	ntpPort       = 123
	ntpPacketSize = 48
	// Seconds between NTP epoch (1900-01-01) and Unix epoch (1970-01-01).
	ntpEpochOffset = 2208988800
)

type ntpClient struct {
	stack    *Stack
	active   bool
	synced   bool
	unixSecs int64
}

func (n *ntpClient) init(s *Stack) {
	n.stack = s
}

// Sync sends an SNTP request to the given server and blocks until a
// response is received or timeout expires. Returns the current time.
// Zero allocations (48-byte request built on stack).
func (n *ntpClient) Sync(server netip.Addr, timeout time.Duration) (time.Time, error) {
	n.active = true
	n.synced = false

	// Build SNTP client request (48 bytes)
	var pkt [ntpPacketSize]byte
	// LI=0 (no warning), VN=4 (NTPv4), Mode=3 (client) → 0b00_100_011
	pkt[0] = 0x23

	if err := n.stack.sendUDP(ntpPort+1, ntpPort, server, pkt[:]); err != nil {
		n.active = false
		return time.Time{}, err
	}

	deadline := n.stack.now().Add(timeout)
	for n.stack.now().Before(deadline) {
		if err := n.stack.Poll(); err != nil {
			n.active = false
			return time.Time{}, err
		}
		if n.synced {
			n.active = false
			return time.Unix(n.unixSecs, 0), nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	n.active = false
	return time.Time{}, errTimeout
}

// handleResponse parses an NTP server response.
func (n *ntpClient) handleResponse(data []byte) {
	if len(data) < ntpPacketSize {
		return
	}
	// Transmit Timestamp seconds at bytes 40-43 (big-endian, NTP epoch).
	ntpSecs := uint32(data[40])<<24 | uint32(data[41])<<16 |
		uint32(data[42])<<8 | uint32(data[43])
	if ntpSecs < ntpEpochOffset {
		return // invalid
	}
	n.unixSecs = int64(ntpSecs - ntpEpochOffset)
	n.synced = true
}
