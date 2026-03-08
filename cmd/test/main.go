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

var nd *wifi.NetDev

func main() {
	// Wait for USB serial (timeout after 5s so board works without terminal)
	deadline := time.Now().Add(5 * time.Second)
	for !machine.Serial.DTR() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	println("=== CYW43439 WiFi Test ===")
	println("[init] Connecting...")

	var err error
	nd, err = wifi.Connect(wifi.Config{
		SSID:       wifiSSID,
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
	if err != nil {
		fmt.Printf("[init] ERROR: %s\n", err.Error())
		select {}
	}

	println("[init] Ready")
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

		// Blink LED every 500ms
		tick++
		if tick%500 == 0 {
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

	switch parts[0] {
	case "help":
		println("Commands:")
		println("  ip             - Show IP address")
		println("  httpget HOST [PATH] - HTTP GET request")
		println("  btscan         - Scan for BLE devices")
		println("  btinfo         - Show Bluetooth status")
		println("  uptime         - Show uptime")
	case "ip":
		ip, _ := nd.Addr()
		if ip.IsValid() {
			fmt.Printf("IP: %s\n", ip)
		} else {
			println("No IP yet")
		}
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
	case "btscan":
		cmdBTScan()
	case "btinfo":
		n := nd.BufferedHCI()
		fmt.Printf("HCI buffered: %d\n", n)
	case "uptime":
		fmt.Printf("Uptime: %s\n", time.Since(bootTime))
	default:
		fmt.Printf("Unknown: %s\n", parts[0])
	}
}

var bootTime = time.Now()

func cmdHTTPGet(host, path string) {
	ip, _ := nd.Addr()
	if !ip.IsValid() {
		println("Not connected yet")
		return
	}

	fmt.Printf("[http] Resolving %s...\n", host)
	addr, err := nd.GetHostByName(host)
	if err != nil {
		fmt.Printf("[http] DNS failed: %s\n", err.Error())
		return
	}
	fmt.Printf("[http] %s -> %s\n", host, addr)

	fd, err := nd.Socket(2, 1, 0) // AF_INET, SOCK_STREAM
	if err != nil {
		fmt.Printf("[http] Socket: %s\n", err.Error())
		return
	}

	fmt.Printf("[http] Connecting to %s:80...\n", addr)
	err = nd.Connect(fd, host, netip.AddrPortFrom(addr, 80))
	if err != nil {
		fmt.Printf("[http] Connect: %s\n", err.Error())
		nd.Close(fd)
		return
	}
	println("[http] Connected!")

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

	var buf [512]byte
	for {
		rn, rerr := nd.Recv(fd, buf[:], 0, deadline)
		if rn > 0 {
			machine.Serial.Write(buf[:rn])
		}
		if rerr != nil {
			break
		}
	}
	println()
	nd.Close(fd)
	println("[http] Done")
}

func cmdBTScan() {
	println("[btscan] Resetting HCI...")
	if err := hciSendCmd(0x0C03, nil); err != nil {
		fmt.Printf("[btscan] Reset send: %s\n", err.Error())
		return
	}
	if err := hciWaitCmdComplete(0x0C03, 2*time.Second); err != nil {
		fmt.Printf("[btscan] Reset: %s\n", err.Error())
		return
	}

	println("[btscan] Setting scan params...")
	scanParams := [7]byte{
		0x01, 0x60, 0x00, 0x30, 0x00, 0x00, 0x00,
	}
	if err := hciSendCmd(0x200B, scanParams[:]); err != nil {
		fmt.Printf("[btscan] Params send: %s\n", err.Error())
		return
	}
	if err := hciWaitCmdComplete(0x200B, 2*time.Second); err != nil {
		fmt.Printf("[btscan] Params: %s\n", err.Error())
		return
	}

	println("[btscan] Starting scan (10s)...")
	scanEnable := [2]byte{0x01, 0x01}
	if err := hciSendCmd(0x200C, scanEnable[:]); err != nil {
		fmt.Printf("[btscan] Enable send: %s\n", err.Error())
		return
	}
	if err := hciWaitCmdComplete(0x200C, 2*time.Second); err != nil {
		fmt.Printf("[btscan] Enable: %s\n", err.Error())
		return
	}

	var hciBuf [256]byte
	count := 0
	scanDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(scanDeadline) {
		if nd.BufferedHCI() > 0 {
			n, err := nd.ReadHCI(hciBuf[:])
			if err != nil {
				continue
			}
			if n >= 2 && hciBuf[0] == 0x04 && hciBuf[1] == 0x3E {
				parseAdvReport(hciBuf[:n], &count)
			}
		}
		time.Sleep(time.Millisecond)
	}

	scanDisable := [2]byte{0x00, 0x00}
	hciSendCmd(0x200C, scanDisable[:])
	hciWaitCmdComplete(0x200C, 2*time.Second)

	fmt.Printf("[btscan] Done, %d devices found\n", count)
}

func hciSendCmd(opcode uint16, params []byte) error {
	var buf [64]byte
	buf[0] = 0x01
	buf[1] = byte(opcode)
	buf[2] = byte(opcode >> 8)
	buf[3] = byte(len(params))
	copy(buf[4:], params)
	_, err := nd.WriteHCI(buf[:4+len(params)])
	return err
}

func hciWaitCmdComplete(opcode uint16, timeout time.Duration) error {
	var buf [256]byte
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if nd.BufferedHCI() > 0 {
			n, err := nd.ReadHCI(buf[:])
			if err != nil {
				continue
			}
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

func parseAdvReport(data []byte, count *int) {
	if len(data) < 5 {
		return
	}
	if data[3] != 0x02 {
		return
	}
	numReports := int(data[4])
	pos := 5
	for i := 0; i < numReports && pos < len(data); i++ {
		if pos+9 > len(data) {
			break
		}
		addrType := data[pos+1]
		addr := data[pos+2 : pos+8]
		dataLen := int(data[pos+8])
		pos += 9

		name := ""
		if pos+dataLen <= len(data) {
			name = parseADName(data[pos : pos+dataLen])
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

func parseADName(ad []byte) string {
	for i := 0; i < len(ad); {
		if i+1 >= len(ad) {
			break
		}
		length := int(ad[i])
		if length == 0 || i+1+length > len(ad) {
			break
		}
		if ad[i+1] == 0x08 || ad[i+1] == 0x09 {
			return string(ad[i+2 : i+1+length])
		}
		i += 1 + length
	}
	return ""
}
