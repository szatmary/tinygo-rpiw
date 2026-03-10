// HTTP server on Pico W using net/http.
//
// Build:
//   tinygo flash -target=pico-w -stack-size=16kb -ldflags="-X main.ssid=MyNetwork -X main.pass=MyPassword" ./example/httpd/
package main

import (
	"fmt"
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
				connected <- struct{}{}
			case wifi.EventLinkDown:
				println("[wifi] link down")
			}
		},
	})
	if err != nil {
		println("wifi:", err.Error())
		select {}
	}

	select {
	case <-connected:
	case <-time.After(30 * time.Second):
		println("wifi timeout")
		select {}
	}

	ip, _ := nd.Addr()
	fmt.Printf("serving http://%s/\n", ip)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ip, _ := nd.Addr()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<h1>Hello from Pico W</h1><p>IP: %s</p>", ip)
	})

	http.ListenAndServe(":80", nil)
}
