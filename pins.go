//go:build rp2040 || rp2350

package tinygorpiw

import "machine"

const (
	PinWLOn   = machine.GPIO23 // WiFi chip enable
	PinWLData = machine.GPIO24 // Half-duplex SPI data (shared MOSI/MISO)
	PinWLCS   = machine.GPIO25 // SPI chip select
	PinWLClk  = machine.GPIO29 // SPI clock
)

func configureCS() outputPin {
	PinWLCS.Configure(machine.PinConfig{Mode: machine.PinOutput})
	PinWLCS.High()
	return PinWLCS.Set
}
