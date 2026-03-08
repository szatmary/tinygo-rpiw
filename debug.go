package tinygorpiw

import "fmt"

// InitBusOnly initializes just the PIO bus and performs SPI test pattern reads.
func (d *Device) InitBusOnly() error {
	bus, cs, err := initHardware()
	if err != nil {
		return fmt.Errorf("initHardware: %w", err)
	}
	d.spi.spi = bus
	d.spi.cs = cs
	d.backplaneWindow = 0xaaaa_aaaa
	d.sdpcmSeqMax = 1

	// Test 1: read with CS toggling (normal path)
	fmt.Println("  Test 1: read32_swapped with CS")
	for i := 0; i < 3; i++ {
		got := d.spi.read32_swapped(FuncBus, spiReadTest)
		fmt.Printf("    attempt %d: 0x%08x (want 0x%08x)\n", i, got, uint32(testPattern))
		if got == testPattern {
			fmt.Println("  PASS!")
			return nil
		}
	}

	// Test 2: try raw CmdRead without our CS toggling
	// (maybe SPI3w handles CS internally)
	fmt.Println("  Test 2: raw CmdRead without CS")
	cmd := swap16(cmd_word(false, true, FuncBus, spiReadTest, 4))
	var buf [1]uint32
	for i := 0; i < 3; i++ {
		buf[0] = 0
		d.spi.spi.CmdRead(cmd, buf[:])
		got := swap16(buf[0])
		st := d.spi.spi.LastStatus()
		fmt.Printf("    attempt %d: 0x%08x status=0x%08x\n", i, got, st)
		if got == testPattern {
			fmt.Println("  PASS! (SPI3w handles CS internally)")
			return nil
		}
	}

	// Test 3: print the cmd word we're sending
	rawCmd := cmd_word(false, true, FuncBus, spiReadTest, 4)
	swappedCmd := swap16(rawCmd)
	fmt.Printf("  cmd_word raw=0x%08x swapped=0x%08x\n", rawCmd, swappedCmd)

	return fmt.Errorf("test pattern never matched")
}
