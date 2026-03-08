# tinygorpiw

TinyGo WiFi and Bluetooth driver for the CYW43439 on Raspberry Pi Pico W.

A single call to `Connect()` initializes the hardware, joins WiFi, acquires an
IP address via DHCP, and starts a background goroutine that polls the network
stack and auto-reconnects on link loss. Once connected, standard Go networking
(`net/http`, `net.Dial`, etc.) works out of the box.

## Features

- CYW43439 WiFi (802.11n) and Bluetooth HCI over gSPI
- Minimal TCP/IP stack: ARP, IPv4, ICMP, UDP, TCP, DHCP, DNS, NTP
- mDNS responder with DNS-SD service advertising
- Zero-configuration API — one function call to get online
- Thread-safe — all public methods are protected by a mutex
- Zero-allocation design for embedded use

## Quick start

```go
package main

import (
    "fmt"
    "io"
    "net/http"
    "time"

    wifi "github.com/mszatmary/tinygorpiw"
)

var (
    ssid string
    pass string
)

func main() {
    connected := make(chan struct{}, 1)

    nd, err := wifi.Connect(wifi.Config{
        SSID:       ssid,
        Passphrase: pass,
        Hostname:   "picow",
        StatusFn: func(e wifi.Event) {
            if e == wifi.EventIPAcquired {
                connected <- struct{}{}
            }
        },
    })
    if err != nil {
        panic(err)
    }

    select {
    case <-connected:
    case <-time.After(30 * time.Second):
        panic("timeout")
    }

    ip, _ := nd.Addr()
    fmt.Println("IP:", ip)

    resp, err := http.Get("http://httpbin.org/ip")
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    fmt.Println(string(body))
}
```

Flash with:

```sh
tinygo flash -target=pico-w \
  -ldflags="-X main.ssid=MyNetwork -X main.pass=MyPassword" \
  ./example/connect/
```

## API reference

### Connect

```go
func Connect(cfg Config) (*NetDev, error)
```

Initializes the CYW43439, starts background WiFi join, DHCP (or static IP
configuration), and auto-reconnect. Returns a `*NetDev` that implements the
TinyGo Netdever interface.

If `Passphrase` is set and `Auth` is zero, authentication defaults to WPA2-PSK.
If `IP` is unset, DHCP runs automatically after link-up.

### Config

```go
type Config struct {
    SSID       string        // WiFi network name
    Passphrase string        // WiFi password
    Auth       Auth          // Auth mode (default: WPA2-PSK when passphrase set)
    Hostname   string        // mDNS hostname (advertised as "hostname.local")
    IP         netip.Addr    // Static IP (zero value = use DHCP)
    Gateway    netip.Addr    // Gateway for static IP
    Subnet     netip.Addr    // Subnet mask for static IP
    DNS        netip.Addr    // DNS server for static IP
    StatusFn   func(Event)   // Called on link up, link down, and IP acquired
}
```

### Auth

```go
const (
    AuthOpen    Auth = 0  // Open (no password)
    AuthWPAPSK  Auth = 1  // WPA Personal
    AuthWPA2PSK Auth = 2  // WPA2 Personal
    AuthWPA3SAE Auth = 3  // WPA3 SAE
)
```

### Event

```go
const (
    EventLinkUp     Event = 1  // WiFi association complete
    EventLinkDown   Event = 2  // WiFi link lost (auto-reconnect starts)
    EventIPAcquired Event = 3  // IP address obtained — networking is ready
)
```

### NetDev methods

All methods are safe for concurrent use.

#### Networking (Netdever interface)

```go
func (nd *NetDev) Addr() (netip.Addr, error)
func (nd *NetDev) GetHostByName(name string) (netip.Addr, error)
func (nd *NetDev) Socket(domain, stype, protocol int) (int, error)
func (nd *NetDev) Bind(fd int, addr netip.AddrPort) error
func (nd *NetDev) Connect(fd int, host string, addr netip.AddrPort) error
func (nd *NetDev) Listen(fd int, backlog int) error
func (nd *NetDev) Accept(fd int) (int, netip.AddrPort, error)
func (nd *NetDev) Send(fd int, buf []byte, flags int, deadline time.Time) (int, error)
func (nd *NetDev) Recv(fd int, buf []byte, flags int, deadline time.Time) (int, error)
func (nd *NetDev) Close(fd int) error
func (nd *NetDev) SetSockOpt(fd, level, opt int, value interface{}) error
```

#### mDNS / DNS-SD

```go
func (nd *NetDev) AddService(svc stack.Service) bool
```

Registers a DNS-SD service for advertisement via mDNS. Returns `false` if the
service table is full (max 4 services). Requires `Hostname` to be set in
`Config`.

```go
// stack.Service describes a DNS-SD service.
type stack.Service struct {
    Name string   // Instance name, e.g. "My Accessory"
    Type string   // Service type, e.g. "_hap._tcp"
    Port uint16   // Service port
    TXT  []string // TXT key=value pairs, e.g. ["c#=1", "sf=1"]
}
```

This advertises:

| Record | Name | Data |
|--------|------|------|
| PTR | `_services._dns-sd._udp.local` | `_hap._tcp.local` |
| PTR | `_hap._tcp.local` | `My Accessory._hap._tcp.local` |
| SRV | `My Accessory._hap._tcp.local` | `hostname.local:port` |
| TXT | `My Accessory._hap._tcp.local` | `c#=1`, `sf=1` |
| A | `hostname.local` | IP address |

Example — advertising a HomeKit accessory:

```go
nd, _ := wifi.Connect(wifi.Config{
    SSID: ssid, Passphrase: pass,
    Hostname: "picow",
})

nd.AddService(stack.Service{
    Name: "My Accessory",
    Type: "_hap._tcp",
    Port: 80,
    TXT:  []string{"c#=1", "ff=0", "id=AA:BB:CC:DD:EE:FF", "md=Pico", "pv=1.1", "s#=1", "sf=1", "ci=2"},
})
```

Verify from macOS:

```sh
dns-sd -B _hap._tcp local
dns-sd -L "My Accessory" _hap._tcp local
ping picow.local
```

#### Ping

```go
func (nd *NetDev) Ping(addr netip.Addr, timeout time.Duration) bool
```

Sends an ICMP echo request and waits for a reply. Returns `true` if the host
responded within the timeout.

```go
gateway := netip.MustParseAddr("192.168.1.1")
if nd.Ping(gateway, 2*time.Second) {
    fmt.Println("gateway is reachable")
}
```

#### NTP

```go
func (nd *NetDev) NTPTime(server netip.Addr, timeout time.Duration) (time.Time, error)
```

Queries an NTP server and returns the current wall-clock time. Useful on
microcontrollers that have no real-time clock.

```go
server, _ := nd.GetHostByName("pool.ntp.org")
now, err := nd.NTPTime(server, 5*time.Second)
if err == nil {
    fmt.Println("Time:", now.UTC())
}
```

#### Hardware address

```go
func (nd *NetDev) HardwareAddr() [6]byte
```

Returns the WiFi MAC address assigned to the CYW43439.

```go
mac := nd.HardwareAddr()
fmt.Printf("MAC: %02x:%02x:%02x:%02x:%02x:%02x\n",
    mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
```

#### GPIO

```go
func (nd *NetDev) GPIOSet(wlGPIO uint8, value bool) error
```

Sets a CYW43439 wireless GPIO pin. On Pico W, GPIO 0 is the onboard LED.

```go
nd.GPIOSet(0, true)  // LED on
nd.GPIOSet(0, false) // LED off
```

#### Bluetooth HCI

```go
func (nd *NetDev) WriteHCI(b []byte) (int, error)
func (nd *NetDev) ReadHCI(b []byte) (int, error)
func (nd *NetDev) BufferedHCI() int
```

Raw HCI transport for Bluetooth. The CYW43439 Bluetooth firmware is loaded
during `Connect()`. Use these methods to send/receive HCI commands and events.

## Static IP

Skip DHCP by setting `IP`, `Gateway`, `Subnet`, and `DNS` in `Config`:

```go
wifi.Connect(wifi.Config{
    SSID:       ssid,
    Passphrase: pass,
    IP:         netip.MustParseAddr("192.168.1.100"),
    Gateway:    netip.MustParseAddr("192.168.1.1"),
    Subnet:     netip.MustParseAddr("255.255.255.0"),
    DNS:        netip.MustParseAddr("8.8.8.8"),
})
```

## Building

Requires [TinyGo](https://tinygo.org/) (tested with 0.40.x):

```sh
# Flash directly to Pico W over USB
tinygo flash -target=pico-w ./example/connect/

# Build UF2 file
tinygo build -target=pico-w -o firmware.uf2 ./example/connect/

# With larger stack for complex applications
tinygo flash -target=pico-w -stack-size=16kb ./your/app/
```

Pass WiFi credentials at build time with `-ldflags`:

```sh
tinygo flash -target=pico-w \
  -ldflags="-X main.ssid=MyNetwork -X main.pass=MyPassword" \
  ./example/connect/
```

## License

See [LICENSE](LICENSE).
