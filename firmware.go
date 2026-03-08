package tinygorpiw

import _ "embed"

//go:embed firmware/43439A0bt.zlib
var fwWiFi string

//go:embed firmware/43439A0_clmbt.zlib
var fwCLM string

//go:embed firmware/btfw.zlib
var fwBT string

// DefaultConfig returns a Config with WiFi+Bluetooth firmware (zlib-compressed).
// Firmware is decompressed during init and streamed to the chip.
func DefaultConfig() Config {
	return Config{
		Firmware:   fwWiFi,
		CLM:        fwCLM,
		BTFirmware: fwBT,
	}
}
