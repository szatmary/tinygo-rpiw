package tinygorpiw

import (
	"net/netip"
	"sync"
	"time"

	"github.com/mszatmary/tinygorpiw/stack"
)

// Connection state machine states.
type connState uint8

const (
	connIdle    connState = 0
	connJoining connState = 1
	connDHCP    connState = 2
	connUp      connState = 3
)

// NetDev implements the TinyGo Netdever interface, bridging the
// CYW43439 driver and TCP/IP stack to standard Go networking.
// All public methods are safe for concurrent use.
type NetDev struct {
	mu    sync.Mutex
	dev   *Device
	stack *stack.Stack
	cfg   Config

	statusFn     func(Event)
	connSt       connState
	connDeadline time.Time
	connRetry    time.Time
}

// Connect initializes the CYW43439, starts background WiFi connection
// and polling, and registers as the TinyGo network device. After Connect
// returns, the background goroutine handles WiFi join, DHCP (or static IP),
// and auto-reconnect. Use cfg.StatusFn to be notified when the link is up.
// Standard net/http works once EventIPAcquired fires.
func Connect(cfg Config) (*NetDev, error) {
	// Default to WPA2 if passphrase is set
	if cfg.Passphrase != "" && cfg.Auth == AuthOpen {
		cfg.Auth = AuthWPA2PSK
	}

	dev := &Device{}
	if err := dev.Init(); err != nil {
		return nil, err
	}

	s := stack.New(dev)
	dev.SetRecvHandler(s.HandleEth)

	nd := &NetDev{
		dev:      dev,
		stack:    s,
		cfg:      cfg,
		statusFn: cfg.StatusFn,
	}

	if cfg.Hostname != "" {
		s.SetHostname(cfg.Hostname)
	}

	registerNetdev(nd)

	go nd.run()

	return nd, nil
}

// run is the background goroutine that polls the network stack
// and maintains the WiFi connection.
func (nd *NetDev) run() {
	for {
		nd.mu.Lock()
		nd.stack.Poll()
		nd.maintainConnection()
		nd.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
}

// maintainConnection is a non-blocking state machine that keeps
// WiFi connected with auto-reconnect.
func (nd *NetDev) maintainConnection() {
	now := time.Now()

	switch nd.connSt {
	case connIdle:
		if !nd.connRetry.IsZero() && now.Before(nd.connRetry) {
			return
		}
		if err := nd.dev.startJoin(nd.cfg); err != nil {
			nd.connRetry = now.Add(5 * time.Second)
			return
		}
		nd.connSt = connJoining
		nd.connDeadline = now.Add(15 * time.Second)
		nd.connRetry = time.Time{}

	case connJoining:
		if nd.dev.IsLinkUp() {
			nd.notify(EventLinkUp)
			if nd.cfg.IP.IsValid() {
				nd.stack.SetAddr(nd.cfg.IP, nd.cfg.Gateway, nd.cfg.Subnet, nd.cfg.DNS)
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
			nd.connSt = connIdle
			nd.connRetry = now.Add(5 * time.Second)
		}

	case connDHCP:
		if !nd.dev.IsLinkUp() {
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

// notify calls the status callback if set. Temporarily releases the
// mutex so the callback may safely call other NetDev methods.
func (nd *NetDev) notify(e Event) {
	if nd.statusFn != nil {
		nd.mu.Unlock()
		nd.statusFn(e)
		nd.mu.Lock()
	}
}

// --- Netdever interface (required by TinyGo net package) ---

func (nd *NetDev) GetHostByName(name string) (netip.Addr, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.DNSResolve(name, 5*time.Second)
}

func (nd *NetDev) Addr() (netip.Addr, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Addr(), nil
}

func (nd *NetDev) Socket(domain int, stype int, protocol int) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Socket(stype)
}

func (nd *NetDev) Bind(sockfd int, ip netip.AddrPort) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Bind(sockfd, ip.Port())
}

func (nd *NetDev) Connect(sockfd int, host string, ip netip.AddrPort) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Connect(sockfd, ip.Addr(), ip.Port())
}

func (nd *NetDev) Listen(sockfd int, backlog int) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Listen(sockfd)
}

func (nd *NetDev) Accept(sockfd int) (int, netip.AddrPort, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Accept(sockfd, 30*time.Second)
}

func (nd *NetDev) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Send(sockfd, buf, deadline)
}

func (nd *NetDev) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Recv(sockfd, buf, deadline)
}

func (nd *NetDev) Close(sockfd int) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.Close(sockfd)
}

func (nd *NetDev) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	return nil
}

// AddService registers a DNS-SD service for mDNS advertisement.
// Returns false if the service table is full.
func (nd *NetDev) AddService(svc stack.Service) bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.AddService(svc)
}

// Ping sends an ICMP echo request and waits for a reply.
// Returns true if a reply was received within the timeout.
func (nd *NetDev) Ping(addr netip.Addr, timeout time.Duration) bool {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	if err := nd.stack.SendPing(addr); err != nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nd.stack.Poll()
		if nd.stack.PingResult() {
			return true
		}
		nd.mu.Unlock()
		time.Sleep(time.Millisecond)
		nd.mu.Lock()
	}
	return false
}

// NTPTime queries an NTP server and returns the current wall-clock time.
func (nd *NetDev) NTPTime(server netip.Addr, timeout time.Duration) (time.Time, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.stack.NTPSync(server, timeout)
}

// HardwareAddr returns the WiFi MAC address.
func (nd *NetDev) HardwareAddr() [6]byte {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.HardwareAddr()
}

// --- Device access (serialized through mutex for dual-core safety) ---

// GPIOSet sets a CYW43439 wireless GPIO pin.
// On Pico W, GPIO0 is the user LED.
func (nd *NetDev) GPIOSet(wlGPIO uint8, value bool) error {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.GPIOSet(wlGPIO, value)
}

// WriteHCI sends an HCI packet to the BT controller.
func (nd *NetDev) WriteHCI(b []byte) (int, error) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.dev.WriteHCI(b)
}

// ReadHCI reads an HCI packet from the BT controller.
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
