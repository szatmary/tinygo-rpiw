package tinygorpiw

import _ "embed"

//go:embed firmware/43439A0.bin
var fwWiFi string

//go:embed firmware/43439A0_clm.bin
var fwCLM string

// DefaultConfig returns a Config with embedded firmware.
// Strings are used to avoid heap-copying the firmware blobs.
func DefaultConfig() Config {
	return Config{
		Firmware: fwWiFi,
		CLM:      fwCLM,
	}
}
