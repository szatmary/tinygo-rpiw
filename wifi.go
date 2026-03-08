package tinygorpiw

import (
	"encoding/binary"
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

// JoinOptions configures WiFi connection parameters.
type JoinOptions struct {
	Auth       Auth
	Passphrase string
}

// Join connects to a WiFi network.
// Blocks until connected or timeout.
func (d *Device) Join(ssid string, opts JoinOptions) error {
	// Reset connection state
	d.joinOK = false
	d.authOK = false
	d.keyExchangeOK = false
	d.linkUp = false

	// Set infrastructure mode
	if err := d.setIoctl32(wlcSetInfra, 0, 1); err != nil {
		println("  [join] 1:infra failed")
		return err
	}
	if err := d.setIoctl32(wlcSetAuth, 0, 0); err != nil {
		println("  [join] 2:auth failed")
		return err
	}

	wsec, wpaAuth := authToWSec(opts.Auth)
	if err := d.setIoctl32(wlcSetWSec, 0, wsec); err != nil {
		println("  [join] 3:wsec failed")
		return err
	}

	if opts.Auth != AuthOpen {
		if err := d.setBsscfgIovar32("sup_wpa", 0, 1); err != nil {
			println("  [join] 4:sup_wpa failed")
			return err
		}
		if err := d.setBsscfgIovar32("sup_wpa2_eapver", 0, 0xFFFFFFFF); err != nil {
			println("  [join] 5:sup_wpa2_eapver failed")
			return err
		}
		if err := d.setBsscfgIovar32("sup_wpa_tmo", 0, 2500); err != nil {
			println("  [join] 6:sup_wpa_tmo failed")
			return err
		}
	}

	if err := d.setIoctl32(wlcSetWPAAuth, 0, wpaAuth); err != nil {
		println("  [join] 7:wpa_auth failed")
		return err
	}

	if opts.Auth != AuthOpen && opts.Passphrase != "" {
		if err := d.setPassphrase(opts.Passphrase); err != nil {
			println("  [join] 8:passphrase failed")
			return err
		}
	}

	var ssidBuf [36]byte
	binary.LittleEndian.PutUint32(ssidBuf[:4], uint32(len(ssid)))
	copy(ssidBuf[4:], ssid)
	if err := d.doIoctl(ioctlSET, wlcSetSSID, 0, ssidBuf[:4+len(ssid)]); err != nil {
		println("  [join] 9:ssid failed")
		return err
	}

	// Wait for connection
	return d.waitForLink(15 * time.Second)
}

// Disconnect disconnects from the current network.
func (d *Device) Disconnect() error {
	d.linkUp = false
	d.joinOK = false
	d.authOK = false
	d.keyExchangeOK = false
	return d.doIoctl(ioctlSET, wlcDisassoc, 0, nil)
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

// waitForLink polls events until WiFi is connected or timeout.
func (d *Device) waitForLink(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := d.poll(); err != nil {
			return err
		}
		if d.linkUp {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errJoinFailed
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
// bsscfg iovars expect: 4-byte bsscfg index + 4-byte value.
func (d *Device) setBsscfgIovar32(name string, bsscfgIdx uint32, val uint32) error {
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[:4], bsscfgIdx)
	binary.LittleEndian.PutUint32(buf[4:], val)
	return d.setIovar("bsscfg:"+name, buf[:])
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

