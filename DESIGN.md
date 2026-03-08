# tinygorpiw — CYW43439 WiFi Driver for TinyGo

A from-scratch WiFi driver for **Raspberry Pi Pico W** (RP2040) and
**Pico 2 W** (RP2350), written in TinyGo. Includes a custom minimal TCP/IP
stack and exposes standard Go networking via TinyGo's `Netdever` interface.

## Status

- [x] PIO+DMA gSPI bus communication
- [x] Firmware upload (230KB) and CLM regulatory data
- [x] WLAN core startup and HT clock
- [x] WiFi join (WPA2-PSK)
- [x] DHCP (IP, gateway, subnet, DNS)
- [x] ARP resolution (blocking with retry)
- [x] ICMP echo (ping send/reply)
- [x] DNS resolution (A records)
- [x] TCP connect, send, receive (full state machine)
- [x] HTTP GET (end-to-end verified: DNS → TCP → HTTP)
- [x] LED control via CYW43439 GPIO0
- [ ] TinyGo Netdever registration (`netdev.UseNetdev`)
- [ ] RP2350 testing
- [ ] WPA3-SAE support (constants defined, untested)

## Quick Start

```bash
# Requires TinyGo 0.40.1+ and Go 1.25.x
export PATH="$HOME/sdk/go1.25.3/bin:$PATH"

# Copy config template and fill in your WiFi credentials
cp cmd/test/config.go.example cmd/test/config.go
# Edit cmd/test/config.go with your SSID and password

# Flash to Pico W
tinygo flash -target=pico-w -stack-size=16kb ./cmd/test/

# Monitor serial output
go run ./cmd/serial/ /dev/cu.usbmodem11301
```

## Hardware Interface

The CYW43439 connects via a non-standard **half-duplex gSPI** interface.
A single data pin (GPIO24) is shared for MOSI and MISO, so standard hardware
SPI cannot be used. Instead we use the RP2040's **PIO** with DMA via
`tinygo-org/pio`'s `piolib.SPI3w` driver at 25 MHz.

### Pin Assignments

| GPIO | Function | Notes |
|------|----------|-------|
| 23 | WL_ON | WiFi chip power enable |
| 24 | SPI DATA | Half-duplex bidirectional data |
| 25 | SPI CS | Chip select. **Also `machine.LED` — do NOT use as LED!** |
| 29 | SPI CLK | Clock (shared with VSYS ADC) |

> The Pico W user LED is CYW43439 GPIO0, only controllable after chip init.

### gSPI Command Word

Every SPI transaction begins with a 32-bit command word:

```
Bit 31:    Write (1) / Read (0)
Bit 30:    Auto-increment address
Bits 29-28: Function (0=Bus, 1=Backplane, 2=WLAN)
Bits 27-11: Address (17 bits)
Bits 10-0:  Size in bytes (11 bits, max 2048)
```

The chip boots in 16-bit mode — initial transactions require byte-swapping
(`swap16`). After configuring 32-bit mode, normal access is used.
Backplane reads require +1 padding word for response delay.

## Architecture

```
┌─────────────────────────────────────────────┐
│  Application (net.Dial, net/http, etc.)     │
├─────────────────────────────────────────────┤
│  netdev.go — Netdever interface adapter     │
├─────────────────────────────────────────────┤
│  stack/ — TCP/IP (ARP, IP, ICMP, UDP, TCP,  │
│           DHCP, DNS)                        │
├─────────────────────────────────────────────┤
│  wifi.go — Join, Disconnect, SendEth, Poll  │
├─────────────────────────────────────────────┤
│  ioctl.go — IOCTL commands, events, credits │
├─────────────────────────────────────────────┤
│  device.go — Backplane windowing, core mgmt │
├─────────────────────────────────────────────┤
│  bus.go — gSPI encoding, CS, read/write     │
├─────────────────────────────────────────────┤
│  bus_pio.go — PIO+DMA (piolib.SPI3w)        │
└─────────────────────────────────────────────┘
```

### File Layout

| File | Description |
|------|-------------|
| `bus.go` | gSPI command encoding, Status type, spiBus CS management |
| `bus_pio.go` | PIO+DMA hardware init (rp2040/rp2350 build tag) |
| `bus_stub.go` | Stub for non-RP2xxx platforms |
| `pins.go` | Pin assignments and CS configuration |
| `device.go` | Device struct, backplane window, core reset, WLAN read/write |
| `init.go` | 17-step chip initialization, firmware/NVRAM/CLM upload |
| `ioctl.go` | IOCTL send/receive, poll loop, event dispatch |
| `protocol.go` | SDPCM/CDC/BDC/Event header encoding |
| `whd.go` | Register addresses, IOCTL constants, event types |
| `wifi.go` | Join, Disconnect, SendEth, GPIO control |
| `netdev.go` | Netdever adapter (Socket, Connect, Send, Recv, etc.) |
| `firmware_embed.go` | `//go:embed` firmware blobs as `string` (flash, not heap) |
| `debug.go` | InitBusOnly — standalone SPI bus test |
| `stack/stack.go` | Stack core, Ethernet dispatch, Poll |
| `stack/arp.go` | ARP table (16 entries), blocking resolve with retry |
| `stack/ipv4.go` | IPv4 parse/send, checksums, pseudo-header |
| `stack/icmp.go` | ICMP echo request/reply, ping |
| `stack/udp.go` | UDP send/receive, DHCP/DNS dispatch |
| `stack/tcp.go` | TCP state machine, retransmit, MSS negotiation |
| `stack/dhcp.go` | DHCP client (discover/offer/request/ack) |
| `stack/dns.go` | DNS A record resolver |
| `stack/socket.go` | Socket struct, ring buffers (2KB rx + 2KB tx), states |
| `stack/api.go` | Public API: Socket, Connect, Send, Recv, Close, Listen, Accept |
| `stack/errors.go` | Error definitions |
| `cmd/test/` | Interactive test program with serial CLI |
| `cmd/serial/` | Host-side serial monitor (macOS, raw termios) |
| `example/connect/` | Example: init → join → DHCP → HTTP GET |

### Chip Initialization (17 steps)

1. **Hardware init** — Configure PIO+CS pins, then power cycle chip
2. **Bus test** — Read test pattern register (expect 0xFEEDBEAD)
3. **R/W test** — Verify read-write register with known pattern
4. **Bus config** — 32-bit words, high-speed, status enable
5. **Post-config verify** — Re-read test patterns without byte swap
6. **Response delay** — Set backplane response delay for Function 1
7. **Interrupts** — Clear pending, enable default set
8. **ALP clock** — Request and wait for ALP clock
9. **Core setup** — Disable WLAN/SOCSRAM, reset SOCSRAM, disable SRAM_3 remap
10. **Firmware** — Upload 230KB firmware via 64-byte backplane chunks
11. **NVRAM** — Load NVRAM at end of RAM with length magic word
12. **WLAN core** — Reset WLAN ARM core
13. **HT clock** — Wait for HT clock
14. **Interrupt mask** — Configure host interrupt mask, enable F2 packet available
15. **F2 watermark** — Set FIFO watermark
16. **F2 ready** — Wait for Function 2 (WLAN data) ready
17. **Control init** — CLM data, country code, MAC read, iovars (txglom, antdiv,
    ampdu), event mask, WLC_UP, G-mode, band, PM=0 (power save disabled)

### Protocol Layers

- **SDPCM** (12 bytes) — Bus multiplexing. Channels: Control (0), Event (1), Data (2).
  Includes sequence numbers and credit management.
- **CDC** (16 bytes) — Control channel for IOCTL commands/responses.
- **BDC** (4 bytes) — Data channel for Ethernet frames. Version 2, includes
  2-byte padding between SDPCM and BDC for data channel packets.
- **Events** — Async notifications (auth, link, PSK exchange) via BDC + EventHeader
  (10 bytes) + EventMessage (48 bytes, big-endian).

### Memory Management

Designed for ~200KB RAM (RP2040):

- **Firmware** embedded as `string` (flash-resident, no heap copy)
- **Pre-allocated uint32 buffers**: txBuf (2KB), rxBuf (2KB), _iovarBuf (2KB)
- **`unsafe.Slice`** for byte views of uint32 buffers (no allocation)
- **`bp_writestring`** writes flash strings to backplane without copying
- Stack sockets: 8 max, each with 2KB rx + 2KB tx ring buffers

### TCP/IP Stack

| Layer | Features |
|-------|----------|
| ARP | 16-entry table, 5-min TTL, blocking resolve with 2s timeout and retries |
| IPv4 | Parse, send, header checksum, fragment-free |
| ICMP | Echo request/reply (ping) |
| UDP | Send/receive, port dispatch to DHCP/DNS/sockets |
| TCP | Full state machine (SYN/FIN/RST), MSS negotiation, retransmit with exponential backoff, 8 concurrent sockets |
| DHCP | Client: discover → offer → request → ack, lease renewal, gateway ARP preload |
| DNS | A record resolver, blocking with timeout and one retry |

### Key Design Decisions

- **PM=0**: CYW43439 power management disabled — PM2 causes missed unicast packets
- **Proactive gateway ARP**: After DHCP, immediately ARP for gateway to avoid stalls
- **DNS fallback**: If DHCP doesn't provide DNS server, use gateway IP
- **Firmware as string**: `//go:embed` into `string` keeps 230KB in flash
- **Blocking ARP resolve**: Sends request and polls for up to 2s with retries

## Test Commands

| Command | Description |
|---------|-------------|
| `help` | List commands |
| `bustest` | Low-level SPI bus verification |
| `init` | Full chip initialization |
| `mac` | Show MAC address |
| `join SSID PASS` | Connect to WiFi (WPA2) |
| `status` | Show link status |
| `dhcp` | Get IP via DHCP |
| `ip` | Show IP address |
| `ping IP` | Send ICMP echo request |
| `httpget HOST [PATH]` | HTTP GET request (DNS + TCP) |
| `disconnect` | Disconnect WiFi |
| `uptime` | Show uptime |

## References

- [Raspberry Pi Pico W Datasheet](https://datasheets.raspberrypi.com/picow/pico-w-datasheet.pdf)
- [soypat/cyw43439](https://github.com/soypat/cyw43439) — TinyGo reference
- [embassy-rs/cyw43](https://github.com/embassy-rs/embassy) — Rust reference
- [georgerobotics/cyw43-driver](https://github.com/georgerobotics/cyw43-driver) — C SDK reference
