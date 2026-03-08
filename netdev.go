package tinygorpiw

import (
	"net/netip"
	"time"

	"github.com/mszatmary/tinygorpiw/stack"
)

// AF and SOCK constants matching TinyGo's netdev package.
const (
	AF_INET     = 2
	SOCK_STREAM = 1
	SOCK_DGRAM  = 2
)

// NetDev implements the TinyGo Netdever interface, bridging the
// CYW43439 driver and TCP/IP stack to standard Go networking.
type NetDev struct {
	dev   *Device
	stack *stack.Stack
}

// NewNetDev creates a new NetDev wrapping the given Device.
func NewNetDev(dev *Device) *NetDev {
	s := stack.New(dev)
	dev.SetRecvHandler(func(frame []byte) {
		s.HandleEth(frame)
	})
	nd := &NetDev{
		dev:   dev,
		stack: s,
	}
	return nd
}

// GetHostByName resolves a hostname to an IP address.
func (nd *NetDev) GetHostByName(name string) (netip.Addr, error) {
	return nd.stack.DNSResolve(name, 5*time.Second)
}

// Addr returns the device's IP address (obtained via DHCP or manual config).
func (nd *NetDev) Addr() (netip.Addr, error) {
	return nd.stack.Addr(), nil
}

// Socket creates a new socket and returns its file descriptor.
func (nd *NetDev) Socket(domain int, stype int, protocol int) (int, error) {
	if domain != AF_INET {
		return -1, errNotReady
	}
	return nd.stack.Socket(stype)
}

// Bind binds a socket to a local address.
func (nd *NetDev) Bind(sockfd int, ip netip.AddrPort) error {
	return nd.stack.Bind(sockfd, ip.Port())
}

// Connect connects a socket to a remote address.
func (nd *NetDev) Connect(sockfd int, host string, ip netip.AddrPort) error {
	return nd.stack.Connect(sockfd, ip.Addr(), ip.Port())
}

// Listen marks a socket as listening.
func (nd *NetDev) Listen(sockfd int, backlog int) error {
	return nd.stack.Listen(sockfd)
}

// Accept accepts an incoming connection on a listening socket.
func (nd *NetDev) Accept(sockfd int) (int, netip.AddrPort, error) {
	return nd.stack.Accept(sockfd, 30*time.Second)
}

// Send sends data on a connected socket.
func (nd *NetDev) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	return nd.stack.Send(sockfd, buf, deadline)
}

// Recv receives data from a connected socket.
func (nd *NetDev) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	return nd.stack.Recv(sockfd, buf, deadline)
}

// Close closes a socket.
func (nd *NetDev) Close(sockfd int) error {
	return nd.stack.Close(sockfd)
}

// SetSockOpt sets socket options.
func (nd *NetDev) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	// Most options are no-ops on bare metal
	return nil
}

// Ping sends an ICMP echo request.
func (nd *NetDev) Ping(ip netip.Addr) error {
	return nd.stack.SendPing(ip)
}

// PingResult returns true if a ping reply was received.
func (nd *NetDev) PingResult() bool {
	return nd.stack.PingResult()
}

// DHCP starts DHCP address acquisition.
func (nd *NetDev) DHCP() error {
	return nd.stack.DHCPStart()
}

// WaitDHCP blocks until DHCP is bound or timeout.
func (nd *NetDev) WaitDHCP(timeout time.Duration) error {
	return nd.stack.WaitDHCP(timeout)
}

// Poll drives the network stack. Must be called regularly.
func (nd *NetDev) Poll() error {
	return nd.stack.Poll()
}
