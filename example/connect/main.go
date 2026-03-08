// Example: Connect to WiFi and make an HTTP GET request.
//
// Build with:
//   tinygo flash -target=pico-w -ldflags="-X main.ssid=MyNetwork -X main.pass=MyPassword" ./example/connect/
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
	// Initialize the CYW43439
	dev := &wifi.Device{}
	if err := dev.Init(wifi.DefaultConfig()); err != nil {
		panic("wifi init: " + err.Error())
	}

	fmt.Println("WiFi initialized, connecting...")

	// Create network device (thread-safe wrapper)
	nd := wifi.NewNetDev(dev)

	// Connect to WiFi
	if err := nd.Join(ssid, wifi.JoinOptions{
		Auth:       wifi.AuthWPA2PSK,
		Passphrase: pass,
	}); err != nil {
		panic("wifi join: " + err.Error())
	}

	fmt.Println("WiFi connected!")
	mac := nd.HardwareAddr()
	fmt.Printf("MAC: %02x:%02x:%02x:%02x:%02x:%02x\n",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])

	// Start DHCP
	if err := nd.DHCP(); err != nil {
		panic("dhcp start: " + err.Error())
	}
	if err := nd.WaitDHCP(15 * time.Second); err != nil {
		panic("dhcp: " + err.Error())
	}

	ip, _ := nd.Addr()
	fmt.Println("IP:", ip)

	// Register as TinyGo netdev
	// netdev.UseNetdev(nd)

	// Now standard net/http works!
	resp, err := http.Get("http://httpbin.org/ip")
	if err != nil {
		fmt.Println("HTTP error:", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println("Response:", string(body))

	// Keep running
	for {
		nd.Poll()
		time.Sleep(10 * time.Millisecond)
	}
}
