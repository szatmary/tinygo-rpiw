package stack

import "errors"

var (
	errPacketTooLarge = errors.New("stack: packet too large")
	errNoSocket       = errors.New("stack: no free socket")
	errBadSocket      = errors.New("stack: invalid socket fd")
	errNotConnected   = errors.New("stack: not connected")
	errConnRefused    = errors.New("stack: connection refused")
	errTimeout        = errors.New("stack: timeout")
	errClosed         = errors.New("stack: connection closed")
	errAddrInUse      = errors.New("stack: address in use")
	errNoRoute        = errors.New("stack: no route to host")
	errDNSFailed      = errors.New("stack: DNS resolution failed")
	errDHCPFailed     = errors.New("stack: DHCP failed")
)
