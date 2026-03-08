package tinygorpiw

import _ "embed"

//go:embed firmware/43439A0bt.zlib
var fwWiFi string

//go:embed firmware/43439A0_clmbt.zlib
var fwCLM string

//go:embed firmware/btfw.zlib
var fwBT string
