package tinygorpiw

import "errors"

// InitBusOnly initializes just the PIO bus and performs SPI test pattern reads.
func (d *Device) InitBusOnly() error {
	bus, cs, err := initHardware()
	if err != nil {
		return errors.New("cyw43: initHardware failed")
	}
	d.spi.spi = bus
	d.spi.cs = cs
	d.backplaneWindow = 0xaaaa_aaaa
	d.sdpcmSeqMax = 1

	println("  SPI bus test: reading test pattern...")
	for i := 0; i < 3; i++ {
		got := d.spi.read32_swapped(FuncBus, spiReadTest)
		if got == testPattern {
			println("  PASS!")
			return nil
		}
		println("  attempt", i, "got", got, "want", testPattern)
	}

	return errors.New("cyw43: test pattern never matched")
}
