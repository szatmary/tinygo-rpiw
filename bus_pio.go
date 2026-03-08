//go:build rp2040 || rp2350

package tinygorpiw

import (
	"machine"
	"time"

	pio "github.com/tinygo-org/pio/rp2-pio"
	"github.com/tinygo-org/pio/rp2-pio/piolib"
)

const spiBAUD = 25_000_000 - 1

type pioBus struct {
	piolib.SPI3w
}

func initHardware() (cmdBus, outputPin, error) {
	// Configure CS pin first (before power cycle, matching soypat/C SDK order)
	cs := configureCS()

	// Configure power pin
	PinWLOn.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Claim PIO state machine and set up SPI before power cycle
	// so data/clock pins are in a known state when chip powers on
	sm, err := pio.PIO0.ClaimStateMachine()
	if err != nil {
		return nil, nil, err
	}

	spi, err := piolib.NewSPI3w(sm, PinWLData, PinWLClk, spiBAUD)
	if err != nil {
		return nil, nil, err
	}
	spi.EnableStatus(true)
	if err := spi.EnableDMA(true); err != nil {
		return nil, nil, err
	}

	// Power cycle WiFi chip AFTER SPI pins are configured
	PinWLOn.Low()
	time.Sleep(20 * time.Millisecond)
	PinWLOn.High()
	time.Sleep(250 * time.Millisecond)

	bus := &pioBus{SPI3w: *spi}
	return bus, cs, nil
}
