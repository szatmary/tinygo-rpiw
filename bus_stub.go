//go:build !(rp2040 || rp2350)

package tinygorpiw

import "errors"

type stubBus struct {
	status uint32
}

func initHardware() (cmdBus, outputPin, error) {
	return nil, nil, errors.New("cyw43: PIO bus not available on this platform")
}

func (s *stubBus) CmdRead(cmd uint32, buf []uint32) error  { return errors.New("cyw43: stub") }
func (s *stubBus) CmdWrite(cmd uint32, buf []uint32) error { return errors.New("cyw43: stub") }
func (s *stubBus) LastStatus() uint32                      { return s.status }
