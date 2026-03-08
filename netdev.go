package tinygorpiw

import (
	"net/netip"
	"sync"
	"time"

	"github.com/mszatmary/tinygorpiw/stack"
)

// AF and SOCK constants matching TinyGo's netdev package.
const (
	AF_INET     = 2
	SOCK_STREAM = 1
	SOCK_DGRAM  = 2
)

// Event represents a network status change.
type Event uint8

const (
	EventLinkUp     Event = 1 // WiFi link established
	EventLinkDown   Event = 2 // WiFi link lost
	EventIPAcquired Event = 3 // IP address obtained via DHCP
)

// Connection state machine states.
type connState uint8

const (
	connIdle    connState = 0 // not connected, waiting to retry
	connJoining connState = 1 // StartJoin sent, waiting for link
	connDHCP    connState = 2 // DHCP started, waiting for bound
	connUp      connState = 3 // fully connected
)

// NetDev implements the TinyGo Netdever interface, bridging the
// CYW43439 driver and TCP/IP stack to standard Go networking.
// All public methods are safe for concurrent use from multiple
// goroutines (required for dual-core RP2040/RP2350).
type NetDev struct {
	mu    sync.Mutex
	dev   *Device
	stack *stack.Stack

	statusFn     func(Event)
	autoSSID     string
	autoOpts     JoinOptions
	autoConn     bool
	connSt       connState
	connDeadline time.Time // timeout for current phase
	connRetry    time.Time // backoff before next attempt
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
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.DNSResolve(name, 5*time.Second)
}

// Addr returns the device's IP address (obtained via DHCP or manual config).
func (nd *NetDev) Addr() (netip.Addr, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Addr(), nil
}

// Socket creates a new socket and returns its file descriptor.
func (nd *NetDev) Socket(domain int, stype int, protocol int) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	if domain != AF_INET {
		return -1, errNotReady
	}
	return nd.stack.Socket(stype)
}

// Bind binds a socket to a local address.
func (nd *NetDev) Bind(sockfd int, ip netip.AddrPort) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Bind(sockfd, ip.Port())
}

// Connect connects a socket to a remote address.
func (nd *NetDev) Connect(sockfd int, host string, ip netip.AddrPort) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Connect(sockfd, ip.Addr(), ip.Port())
}

// Listen marks a socket as listening.
func (nd *NetDev) Listen(sockfd int, backlog int) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Listen(sockfd)
}

// Accept accepts an incoming connection on a listening socket.
func (nd *NetDev) Accept(sockfd int) (int, netip.AddrPort, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Accept(sockfd, 30*time.Second)
}

// Send sends data on a connected socket.
func (nd *NetDev) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Send(sockfd, buf, deadline)
}

// Recv receives data from a connected socket.
func (nd *NetDev) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Recv(sockfd, buf, deadline)
}

// Close closes a socket.
func (nd *NetDev) Close(sockfd int) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Close(sockfd)
}

// SetSockOpt sets socket options.
func (nd *NetDev) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	return nil
}

// Ping sends an ICMP echo request.
func (nd *NetDev) Ping(ip netip.Addr) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.SendPing(ip)
}

// PingResult returns true if a ping reply was received.
func (nd *NetDev) PingResult() bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.PingResult()
}

// DHCP starts DHCP address acquisition.
func (nd *NetDev) DHCP() error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.DHCPStart()
}

// WaitDHCP blocks until DHCP is bound or timeout.
func (nd *NetDev) WaitDHCP(timeout time.Duration) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.WaitDHCP(timeout)
}

// NTPSync queries an NTP server and returns the current wall-clock time.
func (nd *NetDev) NTPSync(server netip.Addr, timeout time.Duration) (time.Time, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.NTPSync(server, timeout)
}

// EnableAutoConnect enables non-blocking automatic WiFi connection with
// infinite retries. Poll advances the connection state machine one step
// at a time — it never blocks. On link loss, reconnection starts
// automatically on the next Poll call.
func (nd *NetDev) EnableAutoConnect(ssid string, opts JoinOptions) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	if opts.Hostname != "" {
		nd.stack.SetHostname(opts.Hostname)
	}
	if opts.StatusFn != nil {
		nd.statusFn = opts.StatusFn
	}
	nd.autoSSID = ssid
	nd.autoOpts = opts
	nd.autoConn = true
	if nd.dev.IsLinkUp() {
		nd.connSt = connUp
	}
}

// Connected returns true if WiFi is associated and DHCP is bound.
func (nd *NetDev) Connected() bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.connSt == connUp
}

// Poll drives the network stack. Must be called regularly.
// When auto-connect is enabled, Poll also advances the non-blocking
// connection state machine (join -> DHCP -> connected).
func (nd *NetDev) Poll() error {
	nd.mu.Lock()
	defer nd.mu.Unlock()

	err := nd.stack.Poll()

	if nd.autoConn {
		nd.maintainConnection()
	}

	return err
}

// Device wrapper methods — serialize Device access through the
// same mutex to prevent concurrent SPI bus access on dual-core.

// GPIOSet sets a CYW43439 wireless GPIO pin.
// On Pico W, GPIO0 is the user LED.
func (nd *NetDev) GPIOSet(wlGPIO uint8, value bool) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.GPIOSet(wlGPIO, value)
}

// WriteHCI sends an HCI packet to the BT controller via the ring buffer.
func (nd *NetDev) WriteHCI(b []byte) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.WriteHCI(b)
}

// ReadHCI reads an HCI packet from the BT controller's ring buffer.
func (nd *NetDev) ReadHCI(b []byte) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.ReadHCI(b)
}

// BufferedHCI returns the number of HCI bytes available to read.
func (nd *NetDev) BufferedHCI() int {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.BufferedHCI()
}

// IsLinkUp returns true if the WiFi link is established.
func (nd *NetDev) IsLinkUp() bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.IsLinkUp()
}

// BTEnabled returns true if Bluetooth is initialized.
func (nd *NetDev) BTEnabled() bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.BTEnabled()
}

// HardwareAddr returns the device MAC address.
func (nd *NetDev) HardwareAddr() [6]byte {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.HardwareAddr()
}

// StartJoin sends WiFi join ioctls without waiting for the link.
func (nd *NetDev) StartJoin(ssid string, opts JoinOptions) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.StartJoin(ssid, opts)
}

// Join connects to a WiFi network and configures IP addressing.
// If opts.IP is set, uses static addressing. Otherwise runs DHCP automatically.
// Blocks until fully connected with an IP address, or timeout.
func (nd *NetDev) Join(ssid string, opts JoinOptions) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	if opts.Hostname != "" {
		nd.stack.SetHostname(opts.Hostname)
	}
	if opts.StatusFn != nil {
		nd.statusFn = opts.StatusFn
	}
	if err := nd.dev.Join(ssid, opts); err != nil {
		return err
	}
	if opts.IP.IsValid() {
		nd.stack.SetAddr(opts.IP, opts.Gateway, opts.Subnet, opts.DNS)
		return nil
	}
	if err := nd.stack.DHCPStart(); err != nil {
		return err
	}
	return nd.stack.WaitDHCP(15 * time.Second)
}

// Disconnect disconnects from the current network.
func (nd *NetDev) Disconnect() error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.Disconnect()
}

// notify calls the status callback. Temporarily releases the mutex so
// the callback may safely call other NetDev methods (e.g. Addr).
func (nd *NetDev) notify(e Event) {
	if nd.statusFn != nil {
		nd.mu.Unlock()
		nd.statusFn(e)
		nd.mu.Lock()
	}
}

// maintainConnection is a non-blocking state machine that keeps
// WiFi connected. Each call does at most one quick state check or
// action — it never blocks.
func (nd *NetDev) maintainConnection() {
	now := time.Now()

	switch nd.connSt {
	case connIdle:
		// Backoff before retrying
		if !nd.connRetry.IsZero() && now.Before(nd.connRetry) {
			return
		}
		if err := nd.dev.StartJoin(nd.autoSSID, nd.autoOpts); err != nil {
			// Ioctl setup failed, retry after backoff
			nd.connRetry = now.Add(5 * time.Second)
			return
		}
		nd.connSt = connJoining
		nd.connDeadline = now.Add(15 * time.Second)
		nd.connRetry = time.Time{}

	case connJoining:
		if nd.dev.IsLinkUp() {
			nd.notify(EventLinkUp)
			if nd.autoOpts.IP.IsValid() {
				nd.stack.SetAddr(nd.autoOpts.IP, nd.autoOpts.Gateway, nd.autoOpts.Subnet, nd.autoOpts.DNS)
				nd.connSt = connUp
				nd.notify(EventIPAcquired)
				return
			}
			nd.connSt = connDHCP
			nd.stack.DHCPStart()
			nd.connDeadline = now.Add(15 * time.Second)
			return
		}
		if now.After(nd.connDeadline) {
			// Join timed out, retry after backoff
			nd.connSt = connIdle
			nd.connRetry = now.Add(5 * time.Second)
		}

	case connDHCP:
		if !nd.dev.IsLinkUp() {
			// Link dropped during DHCP
			nd.connSt = connIdle
			nd.notify(EventLinkDown)
			return
		}
		if nd.stack.DHCPBound() {
			nd.connSt = connUp
			nd.notify(EventIPAcquired)
			return
		}
		if now.After(nd.connDeadline) {
			// DHCP timed out, restart it
			nd.stack.DHCPStart()
			nd.connDeadline = now.Add(15 * time.Second)
		}

	case connUp:
		if !nd.dev.IsLinkUp() {
			nd.connSt = connIdle
			nd.notify(EventLinkDown)
		}
	}
}
