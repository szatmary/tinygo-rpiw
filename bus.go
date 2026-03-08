package tinygorpiw

type Function = uint32

// gSPI function numbers
const (
	FuncBus       Function = 0
	FuncBackplane Function = 1
	FuncWLAN      Function = 2
)

// cmd_word builds a gSPI 32-bit command word.
// Bit layout per CYW43439 datasheet:
//
//	[31]    = write (1) / read (0)
//	[30]    = auto-increment
//	[29:28] = function
//	[27:11] = address (17 bits)
//	[10:0]  = size (11 bits)
func cmd_word(write, autoInc bool, fn Function, addr uint32, sz uint32) uint32 {
	var cmd uint32
	if write {
		cmd |= 1 << 31
	}
	if autoInc {
		cmd |= 1 << 30
	}
	cmd |= (fn & 0x3) << 28
	cmd |= (addr & 0x1FFFF) << 11
	cmd |= sz & 0x7FF
	return cmd
}

// swap16 swaps the two 16-bit halves of a 32-bit word.
// Required for pre-configuration bus transactions (chip boots in 16-bit mode).
func swap16(v uint32) uint32 {
	return (v >> 16) | (v << 16)
}

// Status word returned by CYW43439 after each gSPI transaction.
type Status uint32

func (s Status) DataNotAvailable() bool  { return s&(1<<0) != 0 }
func (s Status) Underflow() bool         { return s&(1<<1) != 0 }
func (s Status) Overflow() bool          { return s&(1<<2) != 0 }
func (s Status) F2Interrupt() bool       { return s&(1<<3) != 0 }
func (s Status) F3Interrupt() bool       { return s&(1<<4) != 0 }
func (s Status) F2RxReady() bool         { return s&(1<<5) != 0 }
func (s Status) F3RxReady() bool         { return s&(1<<6) != 0 }
func (s Status) HostCmdDataErr() bool    { return s&(1<<7) != 0 }
func (s Status) F2PacketAvailable() bool { return s&(1<<8) != 0 }
func (s Status) F2PacketLength() uint16  { return uint16((s >> 9) & 0x7FF) }

// cmdBus is the low-level SPI transport interface.
type cmdBus interface {
	CmdRead(cmd uint32, buf []uint32) error
	CmdWrite(cmd uint32, buf []uint32) error
	LastStatus() uint32
}

// outputPin is a function that sets a GPIO pin high (true) or low (false).
type outputPin func(bool)

// spiBus wraps a cmdBus with chip-select management.
type spiBus struct {
	spi    cmdBus
	cs     outputPin
	status Status
}

func (d *spiBus) csEnable(b bool) {
	d.cs(!b) // CS is active low
}

func (d *spiBus) cmd_read(cmd uint32, buf []uint32) (Status, error) {
	d.csEnable(true)
	err := d.spi.CmdRead(cmd, buf)
	d.csEnable(false)
	d.status = Status(d.spi.LastStatus())
	return d.status, err
}

func (d *spiBus) cmd_write(cmd uint32, buf []uint32) (Status, error) {
	d.csEnable(true)
	err := d.spi.CmdWrite(cmd, buf)
	d.csEnable(false)
	d.status = Status(d.spi.LastStatus())
	return d.status, err
}

// read32_swapped reads a 32-bit value with byte-swapped command/response.
// Used for initial bus transactions before 32-bit mode is configured.
func (d *spiBus) read32_swapped(fn Function, addr uint32) uint32 {
	cmd := swap16(cmd_word(false, true, fn, addr, 4))
	buf := [1]uint32{0}
	d.cmd_read(cmd, buf[:])
	return swap16(buf[0])
}

// write32_swapped writes a 32-bit value with byte-swapped command/data.
func (d *spiBus) write32_swapped(fn Function, addr uint32, val uint32) {
	cmd := swap16(cmd_word(true, true, fn, addr, 4))
	buf := [1]uint32{swap16(val)}
	d.cmd_write(cmd, buf[:])
}

// readn reads 1-4 bytes. For backplane reads, adds 1 padding word.
func (d *Device) readn(fn Function, addr, size uint32) (uint32, error) {
	cmd := cmd_word(false, true, fn, addr, size)
	var padding uint32
	if fn == FuncBackplane {
		padding = 1
	}
	_, err := d.spi.cmd_read(cmd, d.rwBuf[:1+padding])
	return d.rwBuf[padding], err
}

// writen writes 1-4 bytes.
func (d *Device) writen(fn Function, addr, val, size uint32) error {
	cmd := cmd_word(true, true, fn, addr, size)
	d.rwBuf[0] = val
	d.rwBuf[1] = 0
	_, err := d.spi.cmd_write(cmd, d.rwBuf[:1])
	return err
}

func (d *Device) read32(fn Function, addr uint32) (uint32, error) {
	return d.readn(fn, addr, 4)
}
func (d *Device) write32(fn Function, addr uint32, val uint32) error {
	return d.writen(fn, addr, val, 4)
}
func (d *Device) read8(fn Function, addr uint32) (uint8, error) {
	v, err := d.readn(fn, addr, 1)
	return uint8(v), err
}
func (d *Device) write8(fn Function, addr uint32, val uint8) error {
	return d.writen(fn, addr, uint32(val), 1)
}
func (d *Device) read16(fn Function, addr uint32) (uint16, error) {
	v, err := d.readn(fn, addr, 2)
	return uint16(v), err
}
func (d *Device) write16(fn Function, addr uint32, val uint16) error {
	return d.writen(fn, addr, uint32(val), 2)
}

