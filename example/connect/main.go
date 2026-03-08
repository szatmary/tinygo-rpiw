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
	connected := make(chan struct{}, 1)

	nd, err := wifi.Connect(wifi.Config{
		SSID:       ssid,
		Passphrase: pass,
		Hostname:   "picow",
		StatusFn: func(e wifi.Event) {
			switch e {
			case wifi.EventIPAcquired:
				fmt.Println("WiFi connected!")
				connected <- struct{}{}
			case wifi.EventLinkDown:
				fmt.Println("WiFi link down, reconnecting...")
			}
		},
	})
	if err != nil {
		panic("wifi: " + err.Error())
	}

	// Wait for connection
	select {
	case <-connected:
	case <-time.After(30 * time.Second):
		panic("wifi: connection timeout")
	}

	ip, _ := nd.Addr()
	fmt.Println("IP:", ip)

	// net/http just works
	resp, err := http.Get("http://httpbin.org/ip")
	if err != nil {
		fmt.Println("HTTP error:", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println("Response:", string(body))

	select {} // keep running
}
