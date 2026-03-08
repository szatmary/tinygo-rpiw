// Package stack implements a minimal TCP/IP stack for microcontrollers.
// It is allocation-free after initialization and supports a fixed number
// of concurrent sockets.
package stack

import (
	"net/netip"
	"time"
)

const (
	MaxSockets  = 8
	MTU         = 1500
	RxBufSize   = 2048
	TxBufSize   = 2048
	ARPTableLen = 16
)

// Ring buffer masks (sizes must be powers of 2).
const (
	rxMask = RxBufSize - 1
	txMask = TxBufSize - 1
)

// Ethernet header constants
const (
	ethHeaderSize = 14
	ethTypeIPv4   = 0x0800
	ethTypeARP    = 0x0806
)

// txTransportOffset is the byte offset in txPkt where transport-layer
// headers (TCP/UDP/ICMP) should be written. Transport layers write
// directly into txPkt at this offset, then sendIPv4 fills in the
// IP and Ethernet headers and sends the frame. This eliminates all
// intermediate copies and heap allocations in the send path.
const txTransportOffset = ethHeaderSize + ipv4HeaderSize // 34

// NetIF is the interface to the network hardware.
type NetIF interface {
	SendEth(pkt []byte) error
	HardwareAddr() [6]byte
	Poll() error
}

// Stack is a minimal TCP/IP stack.
type Stack struct {
	dev       NetIF
	mac       [6]byte
	localIP   netip.Addr
	gateway   netip.Addr
	subnet    netip.Addr
	dnsServer netip.Addr

	sockets [MaxSockets]Socket
	arp     arpTable

	// Packet buffer for building outgoing frames.
	// Layout: [eth hdr 14][ip hdr 20][transport hdr + payload]
	txPkt [MTU + ethHeaderSize]byte

	// DHCP state
	dhcp dhcpClient

	// DNS state
	dns dnsResolver

	// NTP state
	ntp ntpClient

	// mDNS responder
	mdns mdnsResponder

	// DHCP server (AP mode)
	dhcpSrv dhcpServer

	// ICMP ping state
	pingRecvd bool

	// Ephemeral port counter
	ephemeralPort uint16

	// TCP initial sequence number counter
	tcpISNVal uint32

	// Monotonic time source for retransmissions
	now func() time.Time
}

// New creates a new TCP/IP stack bound to the given network interface.
func New(dev NetIF) *Stack {
	s := &Stack{
		dev:       dev,
		mac:       dev.HardwareAddr(),
		now:           time.Now,
		ephemeralPort: 49152,
		tcpISNVal:     0x12345678,
	}
	s.arp.init()
	for i := range s.sockets {
		s.sockets[i].state = sockFree
	}
	s.dhcp.init(s)
	s.dns.init(s)
	s.ntp.init(s)
	s.mdns.init(s)
	s.dhcpSrv.init(s)
	return s
}

// SetAddr manually configures the IP address, gateway, subnet, and DNS.
func (s *Stack) SetAddr(ip, gateway, subnet, dns netip.Addr) {
	s.localIP = ip
	s.gateway = gateway
	s.subnet = subnet
	s.dnsServer = dns
}

// Addr returns the current IP address.
func (s *Stack) Addr() netip.Addr {
	return s.localIP
}

// HandleEth processes a received Ethernet frame.
// This should be registered as the device's receive callback.
func (s *Stack) HandleEth(frame []byte) {
	if len(frame) < ethHeaderSize {
		return
	}
	etherType := uint16(frame[12])<<8 | uint16(frame[13])
	payload := frame[ethHeaderSize:]

	switch etherType {
	case ethTypeARP:
		s.handleARP(frame)
	case ethTypeIPv4:
		s.handleIPv4(payload)
	}
}

// Poll drives the stack: checks for incoming packets, retransmissions, timers.
// Must be called regularly.
func (s *Stack) Poll() error {
	if err := s.dev.Poll(); err != nil {
		return err
	}

	now := s.now()

	// Drive TCP retransmissions and timeouts
	for i := range s.sockets {
		sock := &s.sockets[i]
		if sock.protocol == protoTCP && sock.state != sockFree {
			s.tcpTimer(sock, now)
		}
	}

	// Drive DHCP
	s.dhcp.poll(now)

	return nil
}

// sendEthFrame sends an Ethernet frame with the given destination MAC and type.
// Used for ARP packets. IP packets bypass this and write directly to txPkt.
func (s *Stack) sendEthFrame(dst [6]byte, ethType uint16, payload []byte) error {
	if ethHeaderSize+len(payload) > len(s.txPkt) {
		return errPacketTooLarge
	}
	copy(s.txPkt[0:6], dst[:])
	copy(s.txPkt[6:12], s.mac[:])
	s.txPkt[12] = byte(ethType >> 8)
	s.txPkt[13] = byte(ethType)
	copy(s.txPkt[ethHeaderSize:], payload)
	return s.dev.SendEth(s.txPkt[:ethHeaderSize+len(payload)])
}

// sameSubnet returns true if the IP is on the same subnet as the stack.
func (s *Stack) sameSubnet(ip netip.Addr) bool {
	if !s.localIP.IsValid() || !s.subnet.IsValid() {
		return false
	}
	local := s.localIP.As4()
	remote := ip.As4()
	mask := s.subnet.As4()
	for i := range 4 {
		if local[i]&mask[i] != remote[i]&mask[i] {
			return false
		}
	}
	return true
}

// tcpISN generates an initial sequence number.
func (s *Stack) tcpISN() uint32 {
	s.tcpISNVal += 64000
	return s.tcpISNVal
}
