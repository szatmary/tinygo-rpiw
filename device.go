package tinygorpiw

import (
	"errors"
	"time"
	"unsafe"
)

var (
	errInitFailed    = errors.New("cyw43: init failed")
	errTimeout       = errors.New("cyw43: timeout")
	errFirmware      = errors.New("cyw43: firmware load failed")
	errNotReady      = errors.New("cyw43: not ready")
	errIOCTL         = errors.New("cyw43: ioctl failed")
	errJoinFailed    = errors.New("cyw43: join failed")
	errNoCredit      = errors.New("cyw43: no bus credit")
	errPacketTooLong = errors.New("cyw43: packet too long")
)

// Device represents a CYW43439 WiFi chip.
type Device struct {
	pwr outputPin
	spi spiBus

	lastStatusGet time.Time
	backplaneWindow uint32
	ioctlID       uint16
	sdpcmSeq      uint8
	sdpcmSeqMax   uint8

	mac            [6]byte
	joinOK         bool
	authOK         bool
	keyExchangeOK  bool
	linkUp         bool

	ioctlPending  bool
	ioctlRespID   uint16
	ioctlRespBuf  []byte

	rcvEth func([]byte)

	rwBuf      [2]uint32
	txBuf      [2048 / 4]uint32
	rxBuf      [2048 / 4]uint32
	_iovarBuf  [2048 / 4]uint32
}

func (d *Device) IsLinkUp() bool       { return d.linkUp }
func (d *Device) HardwareAddr() [6]byte { return d.mac }
func (d *Device) SetRecvHandler(handler func([]byte)) { d.rcvEth = handler }

// status returns the current gSPI bus status.
func (d *Device) status() Status {
	return d.spi.status
}

// Backplane access

func (d *Device) backplane_setwindow(addr uint32) error {
	const (
		addrHi  = 0x1000c
		addrMid = 0x1000b
		addrLo  = 0x1000a
	)
	current := d.backplaneWindow
	addr = addr &^ backplaneAddrMask
	if addr == current {
		return nil
	}
	if (addr & 0xff000000) != (current & 0xff000000) {
		if err := d.write8(FuncBackplane, addrHi, uint8(addr>>24)); err != nil {
			d.backplaneWindow = 0xaaaa_aaaa
			return err
		}
	}
	if (addr & 0x00ff0000) != (current & 0x00ff0000) {
		if err := d.write8(FuncBackplane, addrMid, uint8(addr>>16)); err != nil {
			d.backplaneWindow = 0xaaaa_aaaa
			return err
		}
	}
	if (addr & 0x0000ff00) != (current & 0x0000ff00) {
		if err := d.write8(FuncBackplane, addrLo, uint8(addr>>8)); err != nil {
			d.backplaneWindow = 0xaaaa_aaaa
			return err
		}
	}
	d.backplaneWindow = addr
	return nil
}

func (d *Device) bp_read8(addr uint32) (uint8, error) {
	return d.backplane_readn_u8(addr, 1)
}
func (d *Device) bp_write8(addr uint32, val uint8) error {
	return d.backplane_writen(addr, uint32(val), 1)
}
func (d *Device) bp_read32(addr uint32) (uint32, error) {
	return d.backplane_readn(addr, 4)
}
func (d *Device) bp_write32(addr, val uint32) error {
	return d.backplane_writen(addr, val, 4)
}
func (d *Device) bp_read16(addr uint32) (uint16, error) {
	v, err := d.backplane_readn(addr, 2)
	return uint16(v), err
}
func (d *Device) bp_write16(addr uint32, val uint16) error {
	return d.backplane_writen(addr, uint32(val), 2)
}

func (d *Device) backplane_readn(addr, size uint32) (uint32, error) {
	err := d.backplane_setwindow(addr)
	if err != nil {
		return 0, err
	}
	localAddr := addr & backplaneAddrMask
	if size == 4 {
		localAddr |= 0x08000 // 32-bit access flag
	}
	return d.readn(FuncBackplane, localAddr, size)
}

func (d *Device) backplane_readn_u8(addr, size uint32) (uint8, error) {
	v, err := d.backplane_readn(addr, size)
	return uint8(v), err
}

func (d *Device) backplane_writen(addr, val, size uint32) error {
	err := d.backplane_setwindow(addr)
	if err != nil {
		return err
	}
	localAddr := addr & backplaneAddrMask
	if size == 4 {
		localAddr |= 0x08000
	}
	return d.writen(FuncBackplane, localAddr, val, size)
}

// bp_write writes bulk data to the backplane.
func (d *Device) bp_write(addr uint32, data []byte) error {
	const maxTxSize = 64 // BUS_SPI_MAX_BACKPLANE_TRANSFER_SIZE
	buf := d._iovarBuf[:maxTxSize/4+1]
	buf8 := (*[2048]byte)(unsafe.Pointer(&buf[0]))

	for len(data) > 0 {
		windowOffset := addr & backplaneAddrMask
		windowRemaining := uint32(0x8000) - windowOffset
		length := uint32(len(data))
		if length > maxTxSize {
			length = maxTxSize
		}
		if length > windowRemaining {
			length = windowRemaining
		}
		copy(buf8[:length], data[:length])

		err := d.backplane_setwindow(addr)
		if err != nil {
			return err
		}
		cmd := cmd_word(true, true, FuncBackplane, windowOffset, length)
		d.spi.cmd_write(cmd, buf[:(length+3)/4+1])

		addr += length
		data = data[length:]
	}
	return nil
}

// bp_writestring writes string data (in flash) to backplane without heap allocation.
func (d *Device) bp_writestring(addr uint32, data string) error {
	const maxTxSize = 64
	buf := d._iovarBuf[:maxTxSize/4+1]
	buf8 := (*[2048]byte)(unsafe.Pointer(&buf[0]))

	for len(data) > 0 {
		windowOffset := addr & backplaneAddrMask
		windowRemaining := uint32(0x8000) - windowOffset
		length := uint32(len(data))
		if length > maxTxSize {
			length = maxTxSize
		}
		if length > windowRemaining {
			length = windowRemaining
		}
		copy(buf8[:length], data[:length])

		err := d.backplane_setwindow(addr)
		if err != nil {
			return err
		}
		cmd := cmd_word(true, true, FuncBackplane, windowOffset, length)
		d.spi.cmd_write(cmd, buf[:(length+3)/4+1])

		addr += length
		data = data[length:]
	}
	return nil
}

// bp_read reads bulk data from the backplane.
func (d *Device) bp_read(addr uint32, data []byte) error {
	const maxTxSize = 64
	alignedLen := (uint32(len(data)) + 3) &^ 3
	data = data[:alignedLen]
	var buf [maxTxSize/4 + 1]uint32
	buf8 := (*[maxTxSize + 4]byte)(unsafe.Pointer(&buf[0]))

	for len(data) > 0 {
		windowOffset := addr & backplaneAddrMask
		windowRemaining := uint32(0x8000) - windowOffset
		length := uint32(len(data))
		if length > maxTxSize {
			length = maxTxSize
		}
		if length > windowRemaining {
			length = windowRemaining
		}

		err := d.backplane_setwindow(addr)
		if err != nil {
			return err
		}
		cmd := cmd_word(false, true, FuncBackplane, windowOffset, length)
		d.spi.cmd_read(cmd, buf[:(length+3)/4+1])

		// Skip first word (response delay padding)
		copy(data[:length], buf8[4:4+length])
		addr += length
		data = data[length:]
	}
	return nil
}

// Core management

func coreaddress(coreID uint8) uint32 {
	switch coreID {
	case coreWLAN:
		return wrapperRegOffset + wlanArmBase
	case coreSOCSRAM:
		return wrapperRegOffset + socsramBase
	default:
		panic("bad core id")
	}
}

func (d *Device) core_disable(coreID uint8) error {
	base := coreaddress(coreID)

	d.bp_read8(base + aiResetCtrlOffset) // dummy read
	r, _ := d.bp_read8(base + aiResetCtrlOffset)
	if r&aircReset != 0 {
		return nil // already in reset
	}
	d.bp_write8(base+aiIOCtrlOffset, 0)
	d.bp_read8(base + aiIOCtrlOffset) // dummy read
	time.Sleep(time.Millisecond)

	d.bp_write8(base+aiResetCtrlOffset, aircReset)
	r, _ = d.bp_read8(base + aiResetCtrlOffset)
	if r&aircReset != 0 {
		return nil
	}
	return errors.New("core disable failed")
}

func (d *Device) core_reset(coreID uint8) error {
	if err := d.core_disable(coreID); err != nil {
		return err
	}
	base := coreaddress(coreID)

	d.bp_write8(base+aiIOCtrlOffset, sicfFGC|sicfClockEn)
	d.bp_read8(base + aiIOCtrlOffset) // dummy read

	d.bp_write8(base+aiResetCtrlOffset, 0)
	time.Sleep(time.Millisecond)

	d.bp_write8(base+aiIOCtrlOffset, sicfClockEn)
	d.bp_read8(base + aiIOCtrlOffset) // dummy read
	time.Sleep(time.Millisecond)
	return nil
}

func (d *Device) core_is_up(coreID uint8) bool {
	base := coreaddress(coreID)
	reg, _ := d.bp_read8(base + aiIOCtrlOffset)
	if reg&(sicfFGC|sicfClockEn) != sicfClockEn {
		return false
	}
	reg, _ = d.bp_read8(base + aiResetCtrlOffset)
	return reg&aircReset == 0
}

// WLAN read/write

func (d *Device) wlan_read(buf []uint32, lenInBytes int) error {
	cmd := cmd_word(false, true, FuncWLAN, 0, uint32(lenInBytes))
	lenU32 := (lenInBytes + 3) / 4
	_, err := d.spi.cmd_read(cmd, buf[:lenU32])
	d.lastStatusGet = time.Now()
	return err
}

func (d *Device) wlan_write(data []uint32, plen uint32) error {
	cmd := cmd_word(true, true, FuncWLAN, 0, plen)
	_, err := d.spi.cmd_write(cmd, data)
	d.lastStatusGet = time.Now()
	return err
}
