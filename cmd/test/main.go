// Interactive WiFi test program for Pico W.
// Type "help" for commands.
//
// Build:
//   tinygo flash -target=pico-w -stack-size=16kb ./cmd/test/
package main

import (
	"fmt"
	"machine"
	"net/netip"
	"strings"
	"time"

	wifi "github.com/mszatmary/tinygorpiw"
)

var (
	dev *wifi.Device
	nd  *wifi.NetDev
)

func main() {
	// NOTE: On Pico W, machine.LED (GPIO25) is the WiFi SPI CS pin!
	// Do NOT configure it as an LED output. The Pico W user LED is
	// controlled via CYW43439 GPIO0, only available after WiFi init.

	// Wait for USB serial
	for !machine.Serial.DTR() {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	println("=== CYW43439 WiFi Test ===")

	// Auto-run: init + join
	println("[auto] Running init...")
	cmdInit()
	if dev != nil {
		println("[auto] Running join...")
		cmdJoin(wifiSSID, wifiPass)
		if dev.IsLinkUp() {
			println("[auto] Running DHCP...")
			cmdDHCP()
			// Test connectivity immediately
			println("[auto] Pinging gateway...")
			cmdPing("192.168.0.1")
		}
	}

	println("Type 'help' for commands")
	prompt()

	var lineBuf [256]byte
	lineLen := 0
	tick := 0
	ledOn := true

	for {
		if machine.Serial.Buffered() > 0 {
			b, err := machine.Serial.ReadByte()
			if err != nil {
				continue
			}
			machine.Serial.WriteByte(b)

			if b == '\r' || b == '\n' {
				println()
				if lineLen > 0 {
					cmd := string(lineBuf[:lineLen])
					handleCommand(cmd)
					lineLen = 0
				}
				prompt()
			} else if b == 127 || b == 8 {
				if lineLen > 0 {
					lineLen--
					machine.Serial.WriteByte(' ')
					machine.Serial.WriteByte(8)
				}
			} else if lineLen < len(lineBuf)-1 {
				lineBuf[lineLen] = b
				lineLen++
			}
		}

		if nd != nil {
			nd.Poll()
		}

		// Blink LED every 500ms
		tick++
		if dev != nil && tick%500 == 0 {
			ledOn = !ledOn
			dev.GPIOSet(0, ledOn)
		}

		time.Sleep(time.Millisecond)
	}
}

func prompt() {
	print("> ")
}

func handleCommand(line string) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return
	}
	cmd := parts[0]

	switch cmd {
	case "help":
		println("Commands:")
		println("  init           - Initialize CYW43439 chip")
		println("  bustest        - Low-level SPI bus test")
		println("  mac            - Show MAC address")
		println("  join SSID PASS - Connect to WiFi (WPA2)")
		println("  status         - Show link status")
		println("  dhcp           - Get IP via DHCP")
		println("  ip             - Show IP address")
		println("  httpget HOST [PATH] - HTTP GET request")
		println("  disconnect     - Disconnect WiFi")
		println("  uptime         - Show uptime")
	case "bustest":
		cmdBusTest()
	case "init":
		cmdInit()
	case "mac":
		cmdMAC()
	case "join":
		if len(parts) < 3 {
			println("Usage: join SSID PASSPHRASE")
			return
		}
		cmdJoin(parts[1], strings.Join(parts[2:], " "))
	case "status":
		cmdStatus()
	case "dhcp":
		cmdDHCP()
	case "ip":
		cmdIP()
	case "httpget":
		if len(parts) < 2 {
			println("Usage: httpget <host> [path]")
			return
		}
		path := "/"
		if len(parts) >= 3 {
			path = parts[2]
		}
		cmdHTTPGet(parts[1], path)
	case "ping":
		if len(parts) < 2 {
			println("Usage: ping <ip>")
			return
		}
		cmdPing(parts[1])
	case "disconnect":
		cmdDisconnect()
	case "uptime":
		fmt.Printf("Uptime: %s\n", time.Since(bootTime))
	default:
		fmt.Printf("Unknown: %s\n", cmd)
	}
}

var bootTime = time.Now()

func cmdBusTest() {
	println("[bustest] Testing SPI bus...")
	d := &wifi.Device{}
	err := d.InitBusOnly()
	if err != nil {
		fmt.Printf("[bustest] FAILED: %s\n", err.Error())
	} else {
		println("[bustest] PASSED!")
	}
}

func cmdInit() {
	println("[init] Starting...")
	start := time.Now()

	dev = &wifi.Device{}
	cfg := wifi.DefaultConfig()
	fmt.Printf("[init] Firmware=%d bytes CLM=%d bytes\n", len(cfg.Firmware), len(cfg.CLM))

	println("[init] Calling dev.Init()...")
	if err := dev.Init(cfg); err != nil {
		fmt.Printf("[init] ERROR: %s\n", err.Error())
		dev = nil
		return
	}

	fmt.Printf("[init] OK in %s\n", time.Since(start))

	// Turn on LED (CYW43439 GPIO0 = Pico W user LED)
	dev.GPIOSet(0, true)

	nd = wifi.NewNetDev(dev)
	println("[init] Network stack ready")
}

func cmdMAC() {
	if dev == nil {
		println("Run 'init' first")
		return
	}
	mac := dev.HardwareAddr()
	fmt.Printf("MAC: %02x:%02x:%02x:%02x:%02x:%02x\n",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

func cmdJoin(ssid, pass string) {
	if dev == nil {
		println("Run 'init' first")
		return
	}
	fmt.Printf("[join] Connecting to '%s'...\n", ssid)
	start := time.Now()

	err := dev.Join(ssid, wifi.JoinOptions{
		Auth:       wifi.AuthWPA2PSK,
		Passphrase: pass,
	})
	if err != nil {
		fmt.Printf("[join] ERROR: %s\n", err.Error())
		return
	}
	fmt.Printf("[join] Connected in %s\n", time.Since(start))
}

func cmdStatus() {
	if dev == nil {
		println("Not initialized")
		return
	}
	if dev.IsLinkUp() {
		println("Link: UP")
	} else {
		println("Link: DOWN")
	}
}

func cmdDHCP() {
	if nd == nil {
		println("Run 'init' first")
		return
	}
	if !dev.IsLinkUp() {
		println("WiFi not connected, run 'join' first")
		return
	}
	println("[dhcp] Starting...")
	if err := nd.DHCP(); err != nil {
		fmt.Printf("[dhcp] ERROR: %s\n", err.Error())
		return
	}
	println("[dhcp] Waiting for lease...")
	if err := nd.WaitDHCP(15 * time.Second); err != nil {
		fmt.Printf("[dhcp] ERROR: %s\n", err.Error())
		return
	}
	ip, _ := nd.Addr()
	fmt.Printf("[dhcp] IP: %s\n", ip)
}

func cmdIP() {
	if nd == nil {
		println("Not initialized")
		return
	}
	ip, _ := nd.Addr()
	if ip.IsValid() {
		fmt.Printf("IP: %s\n", ip)
	} else {
		println("No IP (run 'dhcp')")
	}
}

func cmdHTTPGet(host, path string) {
	if nd == nil {
		println("Run 'init' first")
		return
	}
	if !dev.IsLinkUp() {
		println("WiFi not connected")
		return
	}

	// Resolve hostname
	fmt.Printf("[http] Resolving %s...\n", host)
	ip, err := nd.GetHostByName(host)
	if err != nil {
		fmt.Printf("[http] DNS failed: %s\n", err.Error())
		return
	}
	fmt.Printf("[http] %s -> %s\n", host, ip)

	// Open TCP socket
	fd, err := nd.Socket(wifi.AF_INET, wifi.SOCK_STREAM, 0)
	if err != nil {
		fmt.Printf("[http] Socket: %s\n", err.Error())
		return
	}

	// Connect
	fmt.Printf("[http] Connecting to %s:80...\n", ip)
	err = nd.Connect(fd, host, netip.AddrPortFrom(ip, 80))
	if err != nil {
		fmt.Printf("[http] Connect: %s\n", err.Error())
		nd.Close(fd)
		return
	}
	println("[http] Connected!")

	// Send HTTP request
	req := "GET " + path + " HTTP/1.0\r\nHost: " + host + "\r\n\r\n"
	deadline := time.Now().Add(10 * time.Second)
	_, err = nd.Send(fd, []byte(req), 0, deadline)
	if err != nil {
		fmt.Printf("[http] Send: %s\n", err.Error())
		nd.Close(fd)
		return
	}

	// Read response
	var buf [512]byte
	for {
		n, err := nd.Recv(fd, buf[:], 0, deadline)
		if n > 0 {
			machine.Serial.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	println()
	nd.Close(fd)
	println("[http] Done")
}

func cmdPing(ipStr string) {
	if nd == nil {
		println("Run init/join/dhcp first")
		return
	}
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		fmt.Printf("Bad IP: %s\n", ipStr)
		return
	}
	fmt.Printf("[ping] Sending to %s...\n", ip)
	if err := nd.Ping(ip); err != nil {
		fmt.Printf("[ping] Send error: %s\n", err.Error())
		return
	}
	println("[ping] Sent, waiting for reply...")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nd.Poll()
		if nd.PingResult() {
			println("[ping] Reply received!")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	println("[ping] Timeout - no reply")
}

func cmdDisconnect() {
	if dev == nil {
		println("Not initialized")
		return
	}
	if err := dev.Disconnect(); err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		return
	}
	println("Disconnected")
}
