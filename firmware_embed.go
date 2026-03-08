package tinygorpiw

import _ "embed"

//go:embed firmware/43439A0.bin
var fwWiFi string

//go:embed firmware/43439A0_clm.bin
var fwCLM string

//go:embed firmware/43439A0bt.bin
var fwWiFiBT string

//go:embed firmware/43439A0_clmbt.bin
var fwCLMBT string

//go:embed firmware/btfw.bin
var fwBT string

// DefaultConfig returns a Config with WiFi-only firmware.
// Strings are used to avoid heap-copying the firmware blobs.
func DefaultConfig() Config {
	return Config{
		Firmware: fwWiFi,
		CLM:      fwCLM,
	}
}

// DefaultBluetoothConfig returns a Config with WiFi+Bluetooth firmware.
func DefaultBluetoothConfig() Config {
	return Config{
		Firmware:   fwWiFiBT,
		CLM:        fwCLMBT,
		BTFirmware: fwBT,
	}
}
