package tinygorpiw

import (
	"compress/zlib"
	"errors"
	"io"
	"time"
	"unsafe"
)

func (d *Device) Init() error {
	// 1. Init hardware (PIO, CS, power cycle)
	bus, cs, err := initHardware()
	if err != nil {
		return err
	}
	d.spi.spi = bus
	d.spi.cs = cs
	d.sdpcmSeqMax = 1
	d.backplaneWindow = 0xaaaa_aaaa

	// 2. Init bus: read test pattern (pre-config, using swapped words)
	retries := 128
	for {
		got := d.spi.read32_swapped(FuncBus, spiReadTest)
		if got == testPattern {
			break
		}
		retries--
		if retries <= 0 {
			return errors.New("cyw43: spi test pattern failed")
		}
	}

	// 3. Test R/W register (swapped)
	const rwTestPattern = 0x12345678
	d.spi.write32_swapped(FuncBus, spiTestRW, rwTestPattern)
	got := d.spi.read32_swapped(FuncBus, spiTestRW)
	if got != rwTestPattern {
		return errors.New("cyw43: spi rw test failed")
	}

	// 4. Configure bus: 32-bit words, high-speed, status enable, etc.
	d.spi.write32_swapped(FuncBus, spiBusControl, busSetupValue)

	// 5. Verify test pattern with configured bus (non-swapped now)
	got2, err := d.read32(FuncBus, spiReadTest)
	if err != nil || got2 != testPattern {
		return errors.New("cyw43: spi post-config test failed")
	}
	got2, err = d.read32(FuncBus, spiTestRW)
	if err != nil || got2 != rwTestPattern {
		return errors.New("cyw43: spi post-config rw test failed")
	}

	// 6. Set backplane response delay
	d.write8(FuncBus, spiRespDelayF1, busSPIBackplaneReadPaddSize)

	// 7. Clear and configure interrupts
	d.write8(FuncBus, spiIntRegister, 0x0F)     // clear pending
	d.write16(FuncBus, spiIntRegister, 0x00F6)  // enable default set

	// 8. Enable ALP clock
	d.write8(FuncBackplane, sdioChipClockCSR, sbsdioALPAvailReq)
	for {
		v, _ := d.read8(FuncBackplane, sdioChipClockCSR)
		if v&sbsdioALPAvail != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	d.write8(FuncBackplane, sdioChipClockCSR, 0) // clear ALP request

	// 9. Disable cores and upload firmware
	d.core_disable(coreWLAN)
	d.core_disable(coreSOCSRAM)
	d.core_reset(coreSOCSRAM)

	// Disable remap for SRAM_3 (4343x specific)
	d.bp_write32(socsramBankxIndex, 3)
	d.bp_write32(socsramBankxPDA, 0)

	if err := d.bp_writezlib(0, fwWiFi); err != nil {
		return err
	}

	// 10. Load NVRAM
	nvramLen := (uint32(len(nvramData)) + 3) &^ 3
	if err := d.bp_writestring(chipRAMSize-4-nvramLen, nvramData); err != nil {
		return err
	}
	nvramLenWords := nvramLen / 4
	nvramLenMagic := (^nvramLenWords << 16) | nvramLenWords
	d.bp_write32(chipRAMSize-4, nvramLenMagic)

	// 11. Start WLAN core
	d.core_reset(coreWLAN)
	if !d.core_is_up(coreWLAN) {
		return errors.New("cyw43: core not up after reset")
	}

	// 12. Wait for HT clock
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		v, _ := d.read8(FuncBackplane, sdioChipClockCSR)
		if v&sbsdioHTAvail != 0 {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("cyw43: ht clock timeout")
		}
		time.Sleep(time.Millisecond)
	}

	// 13. Set up interrupt mask
	d.bp_write32(sdioIntHostMask, iHMBSWMask)
	d.write16(FuncBus, spiIntEnable, f2PacketAvailable)

	// 14. Lower F2 watermark
	d.write8(FuncBackplane, sdioBackplaneF2Watermark, spiF2Watermark)

	// 15. Wait for F2 ready
	deadline = time.Now().Add(500 * time.Millisecond)
	for !d.status().F2RxReady() {
		if time.Now().After(deadline) {
			return errors.New("cyw43: F2 not ready")
		}
		d.read32(FuncBus, spiStatusRegister) // refresh status
		time.Sleep(time.Millisecond)
	}

	// 16. Clear pulls, start HT clock
	d.write8(FuncBackplane, sdioPullUp, 0)
	d.read8(FuncBackplane, sdioPullUp)

	d.write8(FuncBackplane, sdioChipClockCSR, sbsdioHTAvailReq)
	deadline = time.Now().Add(64 * time.Millisecond)
	for {
		v, err := d.read8(FuncBackplane, sdioChipClockCSR)
		if err != nil {
			return err
		}
		if v&sbsdioHTAvail != 0 {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("cyw43: ht clock timeout 2")
		}
		time.Sleep(time.Millisecond)
	}

	// 17. Init Bluetooth — decompress directly to []byte, pass without copy
	btFW, err := decompressZlib(fwBT)
	if err != nil {
		return err
	}
	if err := d.btInit(btFW); err != nil {
		return err
	}
	d.btEnabled = true

	// 18. Init control (CLM, country, MAC, events)
	clmData, err := decompressZlib(fwCLM)
	if err != nil {
		return err
	}
	return d.initControl(clmData)
}

func (d *Device) initControl(clm []byte) error {
	if err := d.loadCLM(clm); err != nil {
		return err
	}

	// Set country XX
	var countryBuf [12]byte
	countryBuf[0] = 'X'
	countryBuf[1] = 'X'
	countryBuf[4] = 0xff
	countryBuf[5] = 0xff
	countryBuf[6] = 0xff
	countryBuf[7] = 0xff
	countryBuf[8] = 'X'
	countryBuf[9] = 'X'
	if err := d.setIovar("country", countryBuf[:]); err != nil {
		return err
	}

	// Get MAC address
	mac, err := d.getIovar("cur_etheraddr", 6)
	if err != nil {
		return err
	}
	copy(d.mac[:], mac[:6])

	// Disable TX glomming
	if err := d.setIovarU32("bus:txglom", 0); err != nil {
		return err
	}

	// Set antenna to chip antenna
	if err := d.setIoctl32(wlcSetAntdiv, 0, 0); err != nil {
		return err
	}

	// AMPDU settings
	if err := d.setIovarU32("ampdu_ba_wsize", 8); err != nil {
		return err
	}
	if err := d.setIovarU32("ampdu_mpdu", 4); err != nil {
		return err
	}

	// Set event mask
	if err := d.setEventMask(); err != nil {
		return err
	}

	// Bring interface up
	if err := d.doIoctl(ioctlSET, wlcUP, 0, nil); err != nil {
		return err
	}

	time.Sleep(100 * time.Millisecond)

	// Set G-mode (auto) and band (any)
	if err := d.setIoctl32(wlcSetGmode, 0, 1); err != nil {
		return err
	}
	if err := d.setIoctl32(wlcSetBand, 0, 0); err != nil {
		return err
	}

	time.Sleep(100 * time.Millisecond)

	// Disable power management so unicast packets are not missed
	var pmBuf [4]byte
	pmBuf[0] = 0 // PM_NONE
	d.doIoctl(ioctlSET, wlcSetPM, 0, pmBuf[:])

	// Enable mDNS multicast MAC filter (01:00:5E:00:00:FB)
	d.AddMulticastMAC([6]byte{0x01, 0x00, 0x5E, 0x00, 0x00, 0xFB})

	return nil
}

func (d *Device) loadCLM(clm []byte) error {
	const chunkSize = 1024
	// Use _iovarBuf as scratch space to avoid allocations.
	// Layout: "clmload\0" + 12-byte header + chunk data
	buf := (*[2048]byte)(unsafe.Pointer(&d._iovarBuf[0]))
	nameLen := len("clmload") + 1 // includes null terminator
	copy(buf[:nameLen], "clmload\x00")

	offset := 0
	for offset < len(clm) {
		end := offset + chunkSize
		if end > len(clm) {
			end = len(clm)
		}
		chunk := clm[offset:end]

		// Download header flags per CYW43 protocol:
		// 0x1000 = download handler version (always set)
		// 0x0002 = DL_BEGIN
		// 0x0004 = DL_END
		var flag uint16 = 0x1000
		if offset == 0 {
			flag |= 0x0002 // DL_BEGIN
		}
		if end >= len(clm) {
			flag |= 0x0004 // DL_END
		}

		// Download header layout (12 bytes):
		//   [0:2]  flags (uint16 LE)
		//   [2:4]  type (uint16 LE, 2=CLM)
		//   [4:8]  length (uint32 LE, chunk size)
		//   [8:12] CRC (uint32 LE, 0=no CRC)
		hdr := buf[nameLen : nameLen+12]
		hdr[0] = byte(flag)
		hdr[1] = byte(flag >> 8)
		hdr[2] = 2 // type = CLM
		hdr[3] = 0
		hdr[4] = byte(len(chunk))
		hdr[5] = byte(len(chunk) >> 8)
		hdr[6] = byte(len(chunk) >> 16)
		hdr[7] = byte(len(chunk) >> 24)
		hdr[8] = 0 // CRC = 0
		hdr[9] = 0
		hdr[10] = 0
		hdr[11] = 0

		copy(buf[nameLen+12:], chunk)
		totalLen := nameLen + 12 + len(chunk)

		if err := d.doIoctl(ioctlSET, wlcSetVar, 0, buf[:totalLen]); err != nil {
			return err
		}
		offset = end
	}

	// Verify CLM load status
	resp, err := d.getIovar("clmload_status", 4)
	if err != nil {
		return err
	}
	if len(resp) >= 4 {
		status := uint32(resp[0]) | uint32(resp[1])<<8 | uint32(resp[2])<<16 | uint32(resp[3])<<24
		if status != 0 {
			return errors.New("cyw43: clmload_status non-zero")
		}
	}

	return nil
}

func (d *Device) setEventMask() error {
	// bsscfg:event_msgs data = 4-byte bsscfg index + event mask
	var buf [4 + 24]byte
	// buf[0:4] = bsscfg index 0 (zero)
	events := [...]uint32{
		evtSetSSID, evtJoin, evtAuth, evtDeauth, evtDeauthInd,
		evtAssoc, evtAssocInd, evtReassocInd, evtDisassoc, evtDisassocInd,
		evtLink, evtPSKSup,
	}
	for _, ev := range events {
		if ev/8 < 24 {
			buf[4+ev/8] |= 1 << (ev % 8)
		}
	}
	return d.setIovar("bsscfg:event_msgs", buf[:])
}

// bp_writezlib decompresses zlib data and streams it to the backplane
// in 64-byte chunks. The zlib reader uses ~40KB of transient RAM for
// the deflate window; this is freed after init completes.
func (d *Device) bp_writezlib(addr uint32, compressed string) error {
	r, err := zlib.NewReader(&stringReader{s: compressed})
	if err != nil {
		return err
	}
	defer r.Close()

	var chunk [64]byte
	for {
		n, readErr := r.Read(chunk[:])
		if n > 0 {
			if err := d.bp_write(addr, chunk[:n]); err != nil {
				return err
			}
			addr += uint32(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}
	return nil
}

// decompressZlib decompresses zlib data to a byte slice.
// Used for CLM (~7KB) and BT firmware (~10KB) during init.
func decompressZlib(compressed string) ([]byte, error) {
	r, err := zlib.NewReader(&stringReader{s: compressed})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// stringReader implements io.Reader over a string without importing "strings".
type stringReader struct {
	s string
	i int
}

func (r *stringReader) Read(b []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(b, r.s[r.i:])
	r.i += n
	return n, nil
}

const nvramData = "manfid=0x2d0\x00" +
	"prodid=0x0727\x00" +
	"vendid=0x14e4\x00" +
	"devid=0x43e2\x00" +
	"boardtype=0x0887\x00" +
	"boardrev=0x1100\x00" +
	"boardnum=22\x00" +
	"macaddr=00:A0:50:b5:59:5e\x00" +
	"sromrev=11\x00" +
	"boardflags=0x00404001\x00" +
	"boardflags3=0x04000000\x00" +
	"xtalfreq=37400\x00" +
	"nocrc=1\x00" +
	"ag0=255\x00" +
	"aa2g=1\x00" +
	"ccode=ALL\x00" +
	"pa0itssit=0x0000\x00" +
	"extpagain2g=0\x00" +
	"pa2ga0=-168,6649,-778\x00" +
	"AvVmid_c0=0x0,0x1B\x00" +
	"cckpwroffset0=5\x00" +
	"maxp2ga0=84\x00" +
	"txpwrbckof=6\x00" +
	"cckbw202gpo=0\x00" +
	"legofdmbw202gpo=0x66111111\x00" +
	"mcsbw202gpo=0x77711111\x00" +
	"propbw202gpo=0xdd\x00" +
	"ofdmdigfilttype=18\x00" +
	"ofdmdigfilttypebe=18\x00" +
	"papdmode=4\x00" +
	"papdvalidtest=1\x00" +
	"pacalidx2g=45\x00" +
	"papdepsoffset=-30\x00" +
	"papdendidx=58\x00" +
	"ltecxmux=0\x00" +
	"ltecxpadnum=0x0102\x00" +
	"ltecxfnsel=0x44\x00" +
	"ltecxgcigpio=0x01\x00" +
	"il0macaddr=00:90:4c:c5:12:38\x00" +
	"wl0id=0x431b\x00" +
	"deadman_to=0xffffffff\x00" +
	"muxenab=0x100\x00" +
	"spurconfig=0x3\x00" +
	"glitch_based_crsmin=1\x00" +
	"btc_mode=1\x00" +
	"\x00"
