package tinygorpiw

import (
	"encoding/binary"
	"errors"
	"time"
	"unsafe"
)

var (
	errBTNotEnabled   = errors.New("cyw43: bluetooth not enabled")
	errBTReadyTimeout = errors.New("cyw43: bt ready timeout")
	errBTWakeTimeout  = errors.New("cyw43: bt wake timeout")
	errBTZeroAddr     = errors.New("cyw43: bt buffer address is zero")
	errBTHCITooLarge  = errors.New("cyw43: hci packet too large")
	errBTHCINoData    = errors.New("cyw43: no hci data available")
)

// btInit powers up the BT processor, uploads firmware, and initializes
// the HCI ring buffers. Must be called after WiFi firmware is loaded
// and the WLAN core is running.
func (d *Device) btInit(firmware string) error {
	// Power up BT processor
	if err := d.bp_write32(cywBTBaseAddress+bt2wlanPwrupAddr, bt2wlanPwrupWake); err != nil {
		return err
	}
	time.Sleep(2 * time.Millisecond)

	// Upload BT firmware (Intel HEX format)
	if err := d.btUploadFirmware(firmware); err != nil {
		return err
	}

	// Wait for BT firmware ready
	if err := d.btWaitCtrlBits(btsdioRegFWReadyBitmask, 300); err != nil {
		return errBTReadyTimeout
	}

	// Initialize ring buffers
	if err := d.btInitBuffers(); err != nil {
		return err
	}

	// Wait for BT awake
	if err := d.btWaitCtrlBits(btsdioRegBTAwakeBitmask, 300); err != nil {
		return errBTWakeTimeout
	}

	// Set host ready
	if err := d.btSetHostReady(); err != nil {
		return err
	}

	// Toggle interrupt to notify BT controller
	return d.btToggleIntr()
}

// btUploadFirmware parses the Intel HEX format BT firmware and writes
// it to the BT processor memory via backplane.
func (d *Device) btUploadFirmware(firmware string) error {
	// Skip version string: 1-byte length + version + 1 padding byte
	if len(firmware) < 3 {
		return errFirmware
	}
	versionLen := int(firmware[0])
	if versionLen+2 > len(firmware) {
		return errFirmware
	}
	firmware = firmware[versionLen+2:]

	// Use _iovarBuf as scratch for aligned writes
	alignedBuf := d.iovarBytes()[:256]

	var hfd btHexFileData
	hfd.addrMode = btfwAddrModeExtended

	var memVal [4]byte

	for {
		var numBytes uint32
		numBytes, firmware = btReadFirmwarePatchLine(firmware, &hfd)
		if numBytes == 0 {
			break
		}

		fwBytes := hfd.data[:numBytes]
		dstAddr := hfd.dstAddr + cywBTBaseAddress
		var idx uint32

		// Handle start alignment
		if dstAddr%4 != 0 {
			padBytes := dstAddr % 4
			paddedAddr := dstAddr &^ 3
			val, _ := d.bp_read32(paddedAddr)
			binary.LittleEndian.PutUint32(memVal[:], val)
			for i := uint32(0); i < padBytes; i++ {
				alignedBuf[idx] = memVal[i]
				idx++
			}
			for i := uint32(0); i < numBytes; i++ {
				alignedBuf[idx] = fwBytes[i]
				idx++
			}
			dstAddr = paddedAddr
		} else {
			for i := uint32(0); i < numBytes; i++ {
				alignedBuf[idx] = fwBytes[i]
				idx++
			}
		}

		// Handle end alignment
		endAddr := dstAddr + idx
		if endAddr%4 != 0 {
			offset := endAddr % 4
			paddedEndAddr := endAddr &^ 3
			val, _ := d.bp_read32(paddedEndAddr)
			binary.LittleEndian.PutUint32(memVal[:], val)
			for i := offset; i < 4; i++ {
				alignedBuf[idx] = memVal[i]
				idx++
			}
		}

		// Write in 64-byte chunks
		const chunkSize = 64
		for off := uint32(0); off < idx; off += chunkSize {
			end := off + chunkSize
			if end > idx {
				end = idx
			}
			if err := d.bp_write(dstAddr+off, alignedBuf[off:end]); err != nil {
				return err
			}
			time.Sleep(time.Millisecond)
		}
	}
	return nil
}

// btInitBuffers reads the ring buffer base address and zeros all pointers.
func (d *Device) btInitBuffers() error {
	addr, err := d.bp_read32(wlanRAMBaseRegAddr)
	if err != nil {
		return err
	}
	if addr == 0 {
		return errBTZeroAddr
	}
	d.btaddr = addr
	if err = d.bp_write32(addr+btsdioOffsetHost2BTIn, 0); err != nil {
		return err
	}
	if err = d.bp_write32(addr+btsdioOffsetHost2BTOut, 0); err != nil {
		return err
	}
	if err = d.bp_write32(addr+btsdioOffsetBT2HostIn, 0); err != nil {
		return err
	}
	return d.bp_write32(addr+btsdioOffsetBT2HostOut, 0)
}

// btWaitCtrlBits polls the BT control register until the given bits are set.
func (d *Device) btWaitCtrlBits(bits uint32, timeoutMs int) error {
	for i := 0; i < timeoutMs/4+3; i++ {
		val, err := d.bp_read32(btCtrlRegAddr)
		if err != nil {
			return err
		}
		if val&bits != 0 {
			return nil
		}
		time.Sleep(4 * time.Millisecond)
	}
	return errTimeout
}

// btSetHostReady sets the SW_READY bit in HOST_CTRL_REG.
func (d *Device) btSetHostReady() error {
	val, err := d.bp_read32(hostCtrlRegAddr)
	if err != nil {
		return err
	}
	return d.bp_write32(hostCtrlRegAddr, val|btsdioRegSWReadyBitmask)
}

// btSetAwake sets or clears the WAKE_BT bit in HOST_CTRL_REG.
func (d *Device) btSetAwake(awake bool) error {
	val, err := d.bp_read32(hostCtrlRegAddr)
	if err != nil {
		return err
	}
	if awake {
		val |= btsdioRegWakeBTBitmask
	} else {
		val &^= btsdioRegWakeBTBitmask
	}
	return d.bp_write32(hostCtrlRegAddr, val)
}

// btToggleIntr toggles the DATA_VALID bit in HOST_CTRL_REG to signal
// the BT controller that data is available or has been consumed.
func (d *Device) btToggleIntr() error {
	val, err := d.bp_read32(hostCtrlRegAddr)
	if err != nil {
		return err
	}
	return d.bp_write32(hostCtrlRegAddr, val^btsdioRegDataValidBitmask)
}

// btBusRequest wakes the BT controller and waits for it to be ready.
func (d *Device) btBusRequest() error {
	if err := d.btSetAwake(true); err != nil {
		return err
	}
	return d.btWaitCtrlBits(btsdioRegBTAwakeBitmask, 300)
}

// WriteHCI sends an HCI packet to the BT controller via the ring buffer.
// The caller provides the raw HCI packet (type byte + payload).
func (d *Device) WriteHCI(b []byte) (int, error) {
	if !d.btEnabled {
		return 0, errBTNotEnabled
	}

	// BTSDIO header: 3-byte length (payload only, excludes type byte) + data
	// Per pico-sdk cybt_shared_bus.c, length field = HCI payload without type.
	// b[0] = HCI type, b[1:] = HCI payload
	if len(b) < 1 {
		return 0, errBTHCITooLarge
	}
	payloadLen := len(b) - 1 // exclude type byte
	alignBufLen := ((payloadLen + 4) + 3) &^ 3

	buf := d.iovarBytes()[:256]
	if len(b) > len(buf)-3 {
		return 0, errBTHCITooLarge
	}
	buf[0] = byte(payloadLen)
	buf[1] = byte(payloadLen >> 8)
	buf[2] = 0
	copy(buf[3:], b)

	if err := d.btBusRequest(); err != nil {
		return 0, err
	}

	addr := d.btaddr + btsdioOffsetHostWriteBuf + d.h2bWritePtr
	if err := d.bp_write(addr, buf[:alignBufLen]); err != nil {
		return 0, err
	}
	d.h2bWritePtr += uint32(alignBufLen)
	if err := d.bp_write32(d.btaddr+btsdioOffsetHost2BTIn, d.h2bWritePtr); err != nil {
		return 0, err
	}
	return len(b), d.btToggleIntr()
}

// ReadHCI reads an HCI packet from the BT controller's ring buffer.
// Returns the HCI packet (type byte + payload) with the BTSDIO header stripped.
func (d *Device) ReadHCI(b []byte) (int, error) {
	if !d.btEnabled {
		return 0, errBTNotEnabled
	}

	// Check available data
	available, err := d.hciAvailable()
	if err != nil {
		return 0, err
	}
	if available < 4 {
		return 0, errBTHCINoData
	}

	// Read 4-byte BTSDIO header to get length
	var hdr [4]byte
	if err := d.hciRawRead(hdr[:]); err != nil {
		return 0, err
	}
	payloadLen := uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16
	hciLen := payloadLen + 1 // +1 for packet type byte in hdr[3]
	roundedLen := (payloadLen + 4 + 3) &^ 3 // total frame aligned

	if int(hciLen) > len(b) {
		return 0, errBTHCITooLarge
	}

	// Read the full frame (header + payload, aligned)
	readBuf := (*[2048]byte)(unsafe.Pointer(&d.rxBuf[0]))
	if err := d.hciReadRingbuf(readBuf[:roundedLen]); err != nil {
		return 0, err
	}

	// Strip 3-byte BTSDIO header, return HCI packet (type + payload)
	copy(b, readBuf[3:3+hciLen])

	if err := d.btToggleIntr(); err != nil {
		return int(hciLen), err
	}
	return int(hciLen), nil
}

// BufferedHCI returns the number of HCI bytes available to read.
func (d *Device) BufferedHCI() int {
	if !d.btEnabled {
		return 0
	}
	available, _ := d.hciAvailable()
	if available < 4 {
		return 0
	}
	// Peek at header to get actual data length
	var hdr [4]byte
	if err := d.hciRawRead(hdr[:]); err != nil {
		return 0
	}
	return int(uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16)
}

// hciAvailable returns the number of bytes available in the BT→Host ring buffer.
func (d *Device) hciAvailable() (uint32, error) {
	writePtr, err := d.bp_read32(d.btaddr + btsdioOffsetBT2HostIn)
	if err != nil {
		return 0, err
	}
	return (writePtr - d.b2hReadPtr) % btsdioFWBufSize, nil
}

// hciRawRead reads from the ring buffer without advancing the read pointer.
func (d *Device) hciRawRead(buf []byte) error {
	addr := d.btaddr + btsdioOffsetHostReadBuf + d.b2hReadPtr
	if d.b2hReadPtr+uint32(len(buf)) > btsdioFWBufSize {
		// Wrap around
		n := btsdioFWBufSize - d.b2hReadPtr
		if err := d.bp_read(addr, buf[:n]); err != nil {
			return err
		}
		return d.bp_read(d.btaddr+btsdioOffsetHostReadBuf, buf[n:])
	}
	return d.bp_read(addr, buf)
}

// hciReadRingbuf reads from the ring buffer and advances the read pointer.
func (d *Device) hciReadRingbuf(buf []byte) error {
	if err := d.hciRawRead(buf); err != nil {
		return err
	}
	newPtr := (d.b2hReadPtr + uint32(len(buf))) % btsdioFWBufSize
	if err := d.bp_write32(d.btaddr+btsdioOffsetBT2HostOut, newPtr); err != nil {
		return err
	}
	d.b2hReadPtr = newPtr
	return nil
}

// btHexFileData holds state while parsing Intel HEX format BT firmware.
type btHexFileData struct {
	addrMode     int32
	hiAddr       uint16
	dstAddr      uint32
	data         [256]byte
}

// btReadFirmwarePatchLine reads one firmware patch line and returns the
// number of data bytes and the remaining firmware string.
func btReadFirmwarePatchLine(fw string, hfd *btHexFileData) (uint32, string) {
	var absBaseAddr32 uint32
	for {
		if len(fw) < 4 {
			return 0, fw
		}
		numBytes := fw[0]
		addr := uint16(fw[1])<<8 | uint16(fw[2])
		lineType := fw[3]
		fw = fw[4:]
		if numBytes == 0 {
			break
		}
		if len(fw) < int(numBytes) {
			return 0, fw
		}
		copy(hfd.data[:numBytes], fw[:numBytes])
		fw = fw[numBytes:]

		switch lineType {
		case btfwHexLineTypeExtendedAddress:
			hfd.hiAddr = uint16(hfd.data[0])<<8 | uint16(hfd.data[1])
			hfd.addrMode = btfwAddrModeExtended
		case btfwHexLineTypeExtendedSegmentAddress:
			hfd.hiAddr = uint16(hfd.data[0])<<8 | uint16(hfd.data[1])
			hfd.addrMode = btfwAddrModeSegment
		case btfwHexLineTypeAbsolute32BitAddress:
			absBaseAddr32 = uint32(hfd.data[0])<<24 | uint32(hfd.data[1])<<16 |
				uint32(hfd.data[2])<<8 | uint32(hfd.data[3])
			hfd.addrMode = btfwAddrModeLinear32
		case btfwHexLineTypeData:
			hfd.dstAddr = uint32(addr)
			switch hfd.addrMode {
			case btfwAddrModeExtended:
				hfd.dstAddr += uint32(hfd.hiAddr) << 16
			case btfwAddrModeSegment:
				hfd.dstAddr += uint32(hfd.hiAddr) << 4
			case btfwAddrModeLinear32:
				hfd.dstAddr += absBaseAddr32
			}
			return uint32(numBytes), fw
		}
	}
	return 0, fw
}
