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
	if err := dev.Init(); err != nil {
		panic("wifi init: " + err.Error())
	}

	fmt.Println("WiFi initialized, connecting...")

	// Create network device (thread-safe wrapper)
	nd := wifi.NewNetDev(dev)

	// Connect to WiFi (DHCP runs automatically)
	if err := nd.Join(ssid, wifi.JoinOptions{
		Auth:       wifi.AuthWPA2PSK,
		Passphrase: pass,
	}); err != nil {
		panic("wifi join: " + err.Error())
	}

	ip, _ := nd.Addr()
	mac := nd.HardwareAddr()
	fmt.Printf("Connected! MAC: %02x:%02x:%02x:%02x:%02x:%02x IP: %s\n",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5], ip)

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
