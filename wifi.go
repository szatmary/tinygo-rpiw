package tinygorpiw

import (
	"encoding/binary"
	"net/netip"
	"time"
	"unsafe"
)

// Auth represents a WiFi authentication mode.
type Auth uint32

const (
	AuthOpen    Auth = 0
	AuthWPAPSK  Auth = 1
	AuthWPA2PSK Auth = 2
	AuthWPA3SAE Auth = 3
)

// Event represents a network status change.
type Event uint8

const (
	EventLinkUp     Event = 1 // WiFi link established
	EventLinkDown   Event = 2 // WiFi link lost
	EventIPAcquired Event = 3 // IP address obtained via DHCP
)

// Config has everything needed to connect to WiFi.
// If Passphrase is set and Auth is zero, Auth defaults to AuthWPA2PSK.
// If IP is unset, DHCP runs automatically.
type Config struct {
	SSID       string
	Passphrase string
	Auth       Auth
	Hostname   string      // mDNS hostname (without ".local" suffix)
	IP         netip.Addr  // Static IP (zero = DHCP)
	Gateway    netip.Addr
	Subnet     netip.Addr
	DNS        netip.Addr
	StatusFn   func(Event) // Connection status callback
}

// APConfig has everything needed to start a WiFi access point.
type APConfig struct {
	SSID       string
	Passphrase string
	Auth       Auth
	Channel    uint8       // WiFi channel (1-13, default 6)
	Hostname   string      // mDNS hostname (without ".local" suffix)
	IP         netip.Addr  // AP IP address (default 192.168.4.1)
	Subnet     netip.Addr  // Subnet mask (default 255.255.255.0)
	StatusFn   func(Event) // Connection status callback
}

// startJoin sends the WiFi join ioctls and returns immediately without
// waiting for the link to come up.
func (d *Device) startJoin(cfg Config) error {
	// Reset connection state
	d.joinOK = false
	d.authOK = false
	d.keyExchangeOK = false
	d.linkUp = false

	// Set infrastructure mode
	if err := d.setIoctl32(wlcSetInfra, 0, 1); err != nil {
		return err
	}
	if err := d.setIoctl32(wlcSetAuth, 0, 0); err != nil {
		return err
	}

	wsec, wpaAuth := authToWSec(cfg.Auth)
	if err := d.setIoctl32(wlcSetWSec, 0, wsec); err != nil {
		return err
	}

	if cfg.Auth != AuthOpen {
		if err := d.setBsscfgIovar32("sup_wpa", 0, 1); err != nil {
			return err
		}
		if err := d.setBsscfgIovar32("sup_wpa2_eapver", 0, 0xFFFFFFFF); err != nil {
			return err
		}
		if err := d.setBsscfgIovar32("sup_wpa_tmo", 0, 2500); err != nil {
			return err
		}
	}

	if err := d.setIoctl32(wlcSetWPAAuth, 0, wpaAuth); err != nil {
		return err
	}

	if cfg.Auth != AuthOpen && cfg.Passphrase != "" {
		if err := d.setPassphrase(cfg.Passphrase); err != nil {
			return err
		}
	}

	var ssidBuf [36]byte
	binary.LittleEndian.PutUint32(ssidBuf[:4], uint32(len(cfg.SSID)))
	copy(ssidBuf[4:], cfg.SSID)
	return d.doIoctl(ioctlSET, wlcSetSSID, 0, ssidBuf[:4+len(cfg.SSID)])
}

// startAP configures the chip as an access point and starts beaconing.
func (d *Device) startAP(cfg APConfig) error {
	d.joinOK = false
	d.authOK = false
	d.keyExchangeOK = false
	d.linkUp = false
	d.apMode = true

	// Set infrastructure mode to AP (0)
	if err := d.setIoctl32(wlcSetInfra, 0, 0); err != nil {
		return err
	}
	if err := d.setIoctl32(wlcSetAuth, 0, 0); err != nil {
		return err
	}

	wsec, wpaAuth := authToWSec(cfg.Auth)
	if err := d.setIoctl32(wlcSetWSec, 0, wsec); err != nil {
		return err
	}
	if err := d.setIoctl32(wlcSetWPAAuth, 0, wpaAuth); err != nil {
		return err
	}

	if cfg.Auth != AuthOpen && cfg.Passphrase != "" {
		if err := d.setPassphrase(cfg.Passphrase); err != nil {
			return err
		}
	}

	// Set channel
	ch := uint32(cfg.Channel)
	if ch == 0 {
		ch = 6
	}
	if err := d.setIovarU32("chanspec", 0x1000|ch); err != nil {
		return err
	}

	// Bring interface up
	if err := d.doIoctl(ioctlSET, wlcUP, 0, nil); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)

	// Set SSID to start beaconing
	var ssidBuf [36]byte
	binary.LittleEndian.PutUint32(ssidBuf[:4], uint32(len(cfg.SSID)))
	copy(ssidBuf[4:], cfg.SSID)
	if err := d.doIoctl(ioctlSET, wlcSetSSID, 0, ssidBuf[:4+len(cfg.SSID)]); err != nil {
		return err
	}

	time.Sleep(100 * time.Millisecond)
	d.linkUp = true
	return nil
}

// setPassphrase sets the WPA/WPA2 passphrase via WLC_SET_WSEC_PMK ioctl.
func (d *Device) setPassphrase(pass string) error {
	// wsec_pmk_t: key_len(2) + flags(2) + key(64) = 68 bytes
	var pmk [68]byte
	binary.LittleEndian.PutUint16(pmk[:2], uint16(len(pass)))
	binary.LittleEndian.PutUint16(pmk[2:4], 1) // flags=1: WSEC_PASSPHRASE
	copy(pmk[4:], pass)
	return d.doIoctl(ioctlSET, wlcSetWsecPMK, 0, pmk[:])
}

// SendEth sends a raw Ethernet frame over WiFi.
func (d *Device) SendEth(frame []byte) error {
	// Data packets require 2 bytes of padding between SDPCM and BDC headers.
	const dataPadding = 2
	totalLen := sdpcmHeaderSize + dataPadding + bdcHeaderSize + len(frame)
	paddedLen := (totalLen + 3) &^ 3
	words := paddedLen / 4
	if words > len(d.txBuf) {
		return errPacketTooLong
	}

	// Wait for credit BEFORE encoding header (seq must not advance yet)
	if err := d.waitCredit(500 * time.Millisecond); err != nil {
		return err
	}

	for i := range d.txBuf[:words] {
		d.txBuf[i] = 0
	}

	buf := unsafe.Slice((*byte)(unsafe.Pointer(&d.txBuf[0])), words*4)

	// SDPCM header for data channel (HeaderLength includes padding)
	sdpcm := SDPCMHeader{
		Size:         uint16(totalLen),
		SizeCom:      ^uint16(totalLen),
		Seq:          d.sdpcmSeq,
		ChanAndFlags: sdpcmChanData,
		HeaderLength: sdpcmHeaderSize + dataPadding,
	}
	sdpcm.Put(buf[:sdpcmHeaderSize])
	d.sdpcmSeq++

	// BDC header (after padding)
	bdcOffset := sdpcmHeaderSize + dataPadding
	bdc := BDCHeader{
		Flags: 0x20, // version 2
	}
	bdc.Put(buf[bdcOffset:])

	// Ethernet frame
	copy(buf[bdcOffset+bdcHeaderSize:], frame)

	return d.wlan_write(d.txBuf[:words], uint32(paddedLen))
}

// Poll processes any pending packets. Call this regularly.
func (d *Device) Poll() error {
	return d.poll()
}

// setIoctl32 sends a SET ioctl with a single uint32 value.
func (d *Device) setIoctl32(cmd uint32, iface uint32, val uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], val)
	return d.doIoctl(ioctlSET, cmd, iface, buf[:])
}

// setBsscfgIovar32 sets a bsscfg iovar with a uint32 value.
func (d *Device) setBsscfgIovar32(name string, bsscfgIdx uint32, val uint32) error {
	buf := d.iovarBytes()
	n := copy(buf, "bsscfg:")
	n += copy(buf[n:], name)
	buf[n] = 0
	n++
	buf[n] = byte(bsscfgIdx)
	buf[n+1] = byte(bsscfgIdx >> 8)
	buf[n+2] = byte(bsscfgIdx >> 16)
	buf[n+3] = byte(bsscfgIdx >> 24)
	n += 4
	buf[n] = byte(val)
	buf[n+1] = byte(val >> 8)
	buf[n+2] = byte(val >> 16)
	buf[n+3] = byte(val >> 24)
	n += 4
	return d.doIoctl(ioctlSET, wlcSetVar, 0, buf[:n])
}

// GPIOSet sets a CYW43439 wireless GPIO pin.
// On Pico W, GPIO0 is the user LED.
func (d *Device) GPIOSet(wlGPIO uint8, value bool) error {
	var buf [8]byte
	mask := uint32(1) << wlGPIO
	var val uint32
	if value {
		val = mask
	}
	binary.LittleEndian.PutUint32(buf[:4], mask)
	binary.LittleEndian.PutUint32(buf[4:], val)
	return d.setIovar("gpioout", buf[:])
}

// AddMulticastMAC adds a MAC address to the CYW43439 multicast filter.
func (d *Device) AddMulticastMAC(mac [6]byte) error {
	buf := d.iovarBytes()
	n := copy(buf, "mcast_list")
	buf[n] = 0
	n++
	buf[n] = 1
	buf[n+1] = 0
	buf[n+2] = 0
	buf[n+3] = 0
	n += 4
	copy(buf[n:], mac[:])
	n += 6
	return d.doIoctl(ioctlSET, wlcSetVar, 0, buf[:n])
}

func authToWSec(auth Auth) (wsec uint32, wpaAuth uint32) {
	switch auth {
	case AuthOpen:
		return wsecNone, wpaAuthDisabled
	case AuthWPAPSK:
		return wsecTKIP, wpaAuthWPAPSK
	case AuthWPA2PSK:
		return wsecAES, wpaAuthWPA2PSK
	case AuthWPA3SAE:
		return wsecAES, wpaAuthWPA3SAE
	default:
		return wsecAES, wpaAuthWPA2PSK
	}
}
