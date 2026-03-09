// WiFi setup example for Pico W.
//
// On first boot (or when flash config is invalid), starts in AP mode
// with SSID "pico-XXXX" and hosts a configuration page at http://192.168.4.1.
// Once credentials are saved, reboots into station mode and connects
// to the configured network.
//
// Build:
//   tinygo flash -target=pico-w -stack-size=16kb ./example/setup/
package main

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"machine"
	"net/http"
	"time"

	wifi "github.com/mszatmary/tinygorpiw"
)

// Flash layout: magic(4) + crc32(4) + ssid_len(1) + ssid + pass_len(1) + pass
const (
	configMagic = 0x57494649 // "WIFI"
	configSize  = 256        // one write block
)

type config struct {
	SSID string
	Pass string
}

func main() {
	// Wait for USB serial
	deadline := time.Now().Add(3 * time.Second)
	for !machine.Serial.DTR() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	println("=== Pico W Setup ===")

	cfg, ok := loadConfig()
	if ok {
		fmt.Printf("[setup] Found config: SSID=%s\n", cfg.SSID)
		runStation(cfg)
	} else {
		println("[setup] No valid config, starting AP mode")
		runAP()
	}
}

// loadConfig reads and validates the config from flash.
// Layout: magic(4) + crc32(4) + ssid_len(1) + ssid + pass_len(1) + pass
func loadConfig() (config, bool) {
	var buf [configSize]byte
	if _, err := machine.Flash.ReadAt(buf[:], 0); err != nil {
		return config{}, false
	}

	if binary.LittleEndian.Uint32(buf[0:4]) != configMagic {
		return config{}, false
	}
	storedCRC := binary.LittleEndian.Uint32(buf[4:8])

	// Parse payload starting at offset 8
	b := buf[8:]
	if len(b) < 1 {
		return config{}, false
	}
	ssidLen := int(b[0])
	b = b[1:]
	if ssidLen == 0 || ssidLen > len(b) {
		return config{}, false
	}
	ssid := string(b[:ssidLen])
	b = b[ssidLen:]

	if len(b) < 1 {
		return config{}, false
	}
	passLen := int(b[0])
	b = b[1:]
	if passLen > len(b) {
		return config{}, false
	}
	pass := string(b[:passLen])

	// CRC covers everything after the 8-byte header
	payloadEnd := 8 + 1 + ssidLen + 1 + passLen
	if crc32.ChecksumIEEE(buf[8:payloadEnd]) != storedCRC {
		return config{}, false
	}

	return config{SSID: ssid, Pass: pass}, true
}

// saveConfig writes the config to flash.
func saveConfig(cfg config) error {
	var buf [configSize]byte

	// Header
	binary.LittleEndian.PutUint32(buf[0:4], configMagic)
	// CRC placeholder at buf[4:8]

	// Payload
	i := 8
	buf[i] = byte(len(cfg.SSID))
	i++
	i += copy(buf[i:], cfg.SSID)
	buf[i] = byte(len(cfg.Pass))
	i++
	i += copy(buf[i:], cfg.Pass)

	// CRC over payload
	binary.LittleEndian.PutUint32(buf[4:8], crc32.ChecksumIEEE(buf[8:i]))

	if err := machine.Flash.EraseBlocks(0, 1); err != nil {
		return err
	}
	_, err := machine.Flash.WriteAt(buf[:], 0)
	return err
}

// deviceUID returns a short hex string from the flash unique ID.
func deviceUID() string {
	id := machine.DeviceID()
	if len(id) < 4 {
		return "0000"
	}
	return fmt.Sprintf("%02x%02x", id[len(id)-2], id[len(id)-1])
}

// runStation connects to WiFi using saved credentials.
func runStation(cfg config) {
	connected := make(chan struct{}, 1)

	nd, err := wifi.Connect(wifi.Config{
		SSID:       cfg.SSID,
		Passphrase: cfg.Pass,
		Hostname:   "picow",
		StatusFn: func(e wifi.Event) {
			switch e {
			case wifi.EventIPAcquired:
				fmt.Println("[wifi] Connected!")
				connected <- struct{}{}
			case wifi.EventLinkDown:
				fmt.Println("[wifi] Link down, reconnecting...")
			}
		},
	})
	if err != nil {
		fmt.Printf("[wifi] Error: %s\n", err.Error())
		println("[wifi] Falling back to AP mode")
		runAP()
		return
	}

	select {
	case <-connected:
	case <-time.After(30 * time.Second):
		println("[wifi] Timeout, falling back to AP mode")
		_ = nd
		runAP()
		return
	}

	ip, _ := nd.Addr()
	fmt.Printf("[wifi] IP: %s\n", ip)

	// Serve a status page + reset button
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ip, _ := nd.Addr()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, statusPage, ip, cfg.SSID)
	})
	http.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		// Erase config
		machine.Flash.EraseBlocks(0, 1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Config erased!</h1><p>Reboot to enter setup mode.</p></body></html>`)
	})

	fmt.Printf("[http] Serving on http://%s/\n", ip)
	http.ListenAndServe(":80", nil)
}

// runAP starts an access point and serves the setup page.
func runAP() {
	ssid := "pico-" + deviceUID()
	fmt.Printf("[ap] Starting AP: %s\n", ssid)

	_, err := wifi.StartAP(wifi.APConfig{
		SSID:     ssid,
		Hostname: "picow",
	})
	if err != nil {
		fmt.Printf("[ap] Error: %s\n", err.Error())
		select {}
	}

	println("[ap] AP running on 192.168.4.1")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, setupPage)
	})

	http.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		r.ParseForm()
		ssid := r.FormValue("ssid")
		pass := r.FormValue("pass")

		if ssid == "" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h1>Error</h1><p>SSID is required.</p><a href="/">Back</a></body></html>`)
			return
		}

		cfg := config{SSID: ssid, Pass: pass}
		if err := saveConfig(cfg); err != nil {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body><h1>Error</h1><p>%s</p><a href="/">Back</a></body></html>`, err.Error())
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, savedPage, ssid)
	})

	println("[http] Serving setup on http://192.168.4.1/")
	http.ListenAndServe(":80", nil)
}

const setupPage = `<!DOCTYPE html>
<html>
<head>
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Pico W Setup</title>
<style>
body{font-family:sans-serif;max-width:400px;margin:40px auto;padding:0 20px}
input{width:100%;padding:8px;margin:4px 0 16px;box-sizing:border-box;font-size:16px}
button{width:100%;padding:12px;background:#0066cc;color:#fff;border:none;font-size:16px;cursor:pointer;border-radius:4px}
button:hover{background:#0052a3}
label{font-weight:bold}
</style>
</head>
<body>
<h1>Pico W WiFi Setup</h1>
<form method="POST" action="/save">
<label>WiFi Network (SSID)</label>
<input type="text" name="ssid" required>
<label>Password</label>
<input type="password" name="pass">
<button type="submit">Save &amp; Connect</button>
</form>
</body>
</html>`

const savedPage = `<!DOCTYPE html>
<html>
<head>
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Saved</title>
<style>body{font-family:sans-serif;max-width:400px;margin:40px auto;padding:0 20px}</style>
</head>
<body>
<h1>Saved!</h1>
<p>WiFi credentials for <strong>%s</strong> have been saved.</p>
<p>Reboot the device to connect.</p>
</body>
</html>`

const statusPage = `<!DOCTYPE html>
<html>
<head>
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Pico W</title>
<style>
body{font-family:sans-serif;max-width:400px;margin:40px auto;padding:0 20px}
.btn{display:inline-block;padding:12px 24px;background:#cc0000;color:#fff;text-decoration:none;border-radius:4px;margin-top:20px}
.btn:hover{background:#a30000}
</style>
</head>
<body>
<h1>Pico W</h1>
<p><strong>IP:</strong> %s</p>
<p><strong>Network:</strong> %s</p>
<a class="btn" href="/reset">Reset WiFi Config</a>
</body>
</html>`
