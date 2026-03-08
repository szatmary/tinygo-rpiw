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

	// Wait for USB serial (timeout after 5s so board works without terminal)
	deadline := time.Now().Add(5 * time.Second)
	for !machine.Serial.DTR() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	println("=== CYW43439 WiFi Test ===")

	// Init and enable auto-connect
	println("[auto] Running init...")
	cmdInit()
	if nd != nil {
		nd.EnableAutoConnect(wifiSSID, wifi.JoinOptions{
			Auth:       wifi.AuthWPA2PSK,
			Passphrase: wifiPass,
			Hostname:   "picow",
			StatusFn: func(e wifi.Event) {
				switch e {
				case wifi.EventLinkUp:
					println("[status] WiFi link up")
				case wifi.EventLinkDown:
					println("[status] WiFi link down — reconnecting...")
				case wifi.EventIPAcquired:
					ip, _ := nd.Addr()
					fmt.Printf("[status] IP acquired: %s\n", ip)
				}
			},
		})
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
		if nd != nil && tick%500 == 0 {
			ledOn = !ledOn
			nd.GPIOSet(0, ledOn)
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
		println("  ntp [SERVER]   - Sync time via NTP")
		println("  btscan         - Scan for BLE devices")
		println("  btinfo         - Show Bluetooth status")
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
	case "ntp":
		server := "pool.ntp.org"
		if len(parts) >= 2 {
			server = parts[1]
		}
		cmdNTP(server)
	case "btscan":
		cmdBTScan()
	case "btinfo":
		if nd == nil {
			println("Not initialized")
		} else {
			n := nd.BufferedHCI()
			fmt.Printf("BT enabled: %v, HCI buffered: %d\n", nd.BTEnabled(), n)
		}
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
	println("[init] Calling dev.Init()...")
	if err := dev.Init(); err != nil {
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
	if nd == nil {
		println("Run 'init' first")
		return
	}
	mac := nd.HardwareAddr()
	fmt.Printf("MAC: %02x:%02x:%02x:%02x:%02x:%02x\n",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

func cmdJoin(ssid, pass string) {
	if nd == nil {
		println("Run 'init' first")
		return
	}
	fmt.Printf("[join] Connecting to '%s'...\n", ssid)
	start := time.Now()

	err := nd.Join(ssid, wifi.JoinOptions{
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
	if nd == nil {
		println("Not initialized")
		return
	}
	if nd.IsLinkUp() {
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
	if !nd.IsLinkUp() {
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
	if !nd.IsLinkUp() {
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

	// Send HTTP request (build in fixed buffer to avoid string concat allocation)
	var reqBuf [256]byte
	n := copy(reqBuf[:], "GET ")
	n += copy(reqBuf[n:], path)
	n += copy(reqBuf[n:], " HTTP/1.0\r\nHost: ")
	n += copy(reqBuf[n:], host)
	n += copy(reqBuf[n:], "\r\n\r\n")
	deadline := time.Now().Add(10 * time.Second)
	_, err = nd.Send(fd, reqBuf[:n], 0, deadline)
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

func cmdNTP(server string) {
	if nd == nil {
		println("Run init/join/dhcp first")
		return
	}
	if !nd.IsLinkUp() {
		println("WiFi not connected")
		return
	}
	fmt.Printf("[ntp] Resolving %s...\n", server)
	ip, err := nd.GetHostByName(server)
	if err != nil {
		fmt.Printf("[ntp] DNS failed: %s\n", err.Error())
		return
	}
	fmt.Printf("[ntp] Querying %s...\n", ip)
	t, err := nd.NTPSync(ip, 5*time.Second)
	if err != nil {
		fmt.Printf("[ntp] ERROR: %s\n", err.Error())
		return
	}
	fmt.Printf("[ntp] %d-%02d-%02d %02d:%02d:%02d UTC\n",
		t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second())
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

func cmdBTScan() {
	if nd == nil || !nd.BTEnabled() {
		println("BT not initialized")
		return
	}

	// HCI Reset
	println("[btscan] Resetting HCI...")
	if err := hciSendCmd(0x0C03, nil); err != nil {
		fmt.Printf("[btscan] Reset send: %s\n", err.Error())
		return
	}
	if err := hciWaitCmdComplete(0x0C03, 2*time.Second); err != nil {
		fmt.Printf("[btscan] Reset: %s\n", err.Error())
		return
	}

	// LE Set Scan Parameters: active scan, 60ms interval, 30ms window
	println("[btscan] Setting scan params...")
	scanParams := [7]byte{
		0x01,       // active scan
		0x60, 0x00, // interval: 96 * 0.625ms = 60ms
		0x30, 0x00, // window: 48 * 0.625ms = 30ms
		0x00, // own address: public
		0x00, // filter: accept all
	}
	if err := hciSendCmd(0x200B, scanParams[:]); err != nil {
		fmt.Printf("[btscan] Params send: %s\n", err.Error())
		return
	}
	if err := hciWaitCmdComplete(0x200B, 2*time.Second); err != nil {
		fmt.Printf("[btscan] Params: %s\n", err.Error())
		return
	}

	// LE Set Scan Enable
	println("[btscan] Starting scan (10s)...")
	scanEnable := [2]byte{0x01, 0x01} // enable, filter duplicates
	if err := hciSendCmd(0x200C, scanEnable[:]); err != nil {
		fmt.Printf("[btscan] Enable send: %s\n", err.Error())
		return
	}
	if err := hciWaitCmdComplete(0x200C, 2*time.Second); err != nil {
		fmt.Printf("[btscan] Enable: %s\n", err.Error())
		return
	}

	// Read advertising reports for 10 seconds
	var hciBuf [256]byte
	count := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if nd != nil {
			nd.Poll()
		}
		if nd.BufferedHCI() > 0 {
			n, err := nd.ReadHCI(hciBuf[:])
			if err != nil {
				continue
			}
			if n >= 2 && hciBuf[0] == 0x04 && hciBuf[1] == 0x3E {
				// LE Meta Event
				parseAdvReport(hciBuf[:n], &count)
			}
		}
		time.Sleep(time.Millisecond)
	}

	// Disable scan
	scanDisable := [2]byte{0x00, 0x00}
	hciSendCmd(0x200C, scanDisable[:])
	hciWaitCmdComplete(0x200C, 2*time.Second)

	fmt.Printf("[btscan] Done, %d devices found\n", count)
}

// hciSendCmd sends an HCI command packet.
func hciSendCmd(opcode uint16, params []byte) error {
	var buf [64]byte
	buf[0] = 0x01 // HCI command
	buf[1] = byte(opcode)
	buf[2] = byte(opcode >> 8)
	buf[3] = byte(len(params))
	copy(buf[4:], params)
	_, err := nd.WriteHCI(buf[:4+len(params)])
	return err
}

// hciWaitCmdComplete waits for a Command Complete event matching the opcode.
func hciWaitCmdComplete(opcode uint16, timeout time.Duration) error {
	var buf [256]byte
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if nd != nil {
			nd.Poll()
		}
		if nd.BufferedHCI() > 0 {
			n, err := nd.ReadHCI(buf[:])
			if err != nil {
				continue
			}
			// Event packet: [0x04, event_code, param_len, ...]
			// Command Complete: event=0x0E, params: [num_cmds, opcode_lo, opcode_hi, status]
			if n >= 6 && buf[0] == 0x04 && buf[1] == 0x0E {
				evtOpcode := uint16(buf[4]) | uint16(buf[5])<<8
				if evtOpcode == opcode {
					status := buf[6]
					if status != 0 {
						fmt.Printf("[hci] cmd 0x%04x status=%d\n", opcode, status)
					}
					return nil
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("hci cmd 0x%04x timeout", opcode)
}

// parseAdvReport parses LE Advertising Report events.
func parseAdvReport(data []byte, count *int) {
	// data: [0x04, 0x3E, param_len, subevent, ...]
	if len(data) < 5 {
		return
	}
	subevent := data[3]
	if subevent != 0x02 {
		return
	}
	numReports := int(data[4])
	pos := 5
	for i := 0; i < numReports && pos < len(data); i++ {
		if pos+9 > len(data) {
			break
		}
		// evtType := data[pos]
		addrType := data[pos+1]
		addr := data[pos+2 : pos+8]
		dataLen := int(data[pos+8])
		pos += 9

		// Parse name from AD structures
		name := ""
		if pos+dataLen <= len(data) {
			adData := data[pos : pos+dataLen]
			name = parseADName(adData)
		}
		pos += dataLen

		rssi := int8(0)
		if pos < len(data) {
			rssi = int8(data[pos])
			pos++
		}

		typeStr := "pub"
		if addrType == 1 {
			typeStr = "rnd"
		}
		*count++
		fmt.Printf("  %02x:%02x:%02x:%02x:%02x:%02x (%s) RSSI:%d",
			addr[5], addr[4], addr[3], addr[2], addr[1], addr[0],
			typeStr, rssi)
		if name != "" {
			fmt.Printf(" %s", name)
		}
		println()
	}
}

// parseADName extracts the Complete/Short Local Name from AD structures.
func parseADName(ad []byte) string {
	for i := 0; i < len(ad); {
		if i+1 >= len(ad) {
			break
		}
		length := int(ad[i])
		if length == 0 || i+1+length > len(ad) {
			break
		}
		adType := ad[i+1]
		// 0x08 = Shortened Local Name, 0x09 = Complete Local Name
		if adType == 0x08 || adType == 0x09 {
			return string(ad[i+2 : i+1+length])
		}
		i += 1 + length
	}
	return ""
}

func cmdDisconnect() {
	if nd == nil {
		println("Not initialized")
		return
	}
	if err := nd.Disconnect(); err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		return
	}
	println("Disconnected")
}
