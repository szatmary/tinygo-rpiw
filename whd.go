package tinygorpiw

// CYW43439 register addresses and constants.
// Matches pico-sdk cyw43-driver.

// SPI bus registers (Function 0)
const (
	spiBusControl     = 0x0000
	spiIntRegister    = 0x0004
	spiIntEnable      = 0x0006
	spiStatusRegister = 0x0008
	spiReadTest       = 0x0014
	spiTestRW         = 0x0018
	spiRespDelayF1    = 0x001d

	testPattern = 0xFEEDBEAD

	// Bus control bits (at offset 0x0000, 32 bits)
	// Bit layout spans multiple bytes:
	//   byte 0: [0]=WordLength32, [1]=BigEndian, [4]=HiSpeed, [5]=IntPol, [7]=WakeUp
	//   byte 2: [0]=StatusEnable, [1]=IntWithStatus
	//   byte 1: [0:7]=ResponseDelay
	busSetupValue = (1 << 0) | // WordLength32
		(1 << 4) | // HiSpeed
		(1 << 5) | // IntPol high
		(1 << 7) | // WakeUp
		(1 << 17) | // InterruptWithStatus
		(1 << 16) | // StatusEnable
		(0x4 << 8) // ResponseDelay=4

	f2PacketAvailable = 0x0020

	spiF2Watermark               = 32
	busSPIBackplaneReadPaddSize  = 4
)

// Backplane / SDIO registers (Function 1)
const (
	sdioChipClockCSR = 0x1000e
	sdioBackplaneF2Watermark = 0x10008
	sdioPullUp       = 0x1000f

	sbsdioALPAvailReq = 0x08
	sbsdioALPAvail    = 0x40
	sbsdioHTAvailReq  = 0x10
	sbsdioHTAvail     = 0x80
)

// Backplane base addresses
const (
	sdioBaseAddress  = 0x18002000
	wlanArmBase      = 0x18003000
	socsramBase      = 0x18004000
	backplaneAddrMask = 0x7FFF
	wrapperRegOffset = 0x100000

	sdioIntHostMask = sdioBaseAddress + 0x24
	iHMBSWMask      = 0x000000f0

	socsramBankxIndex = socsramBase + 0x10
	socsramBankxPDA   = socsramBase + 0x44
)

// Core IDs and wrapper register offsets
const (
	coreWLAN    = 0
	coreSOCSRAM = 1

	aiIOCtrlOffset    = 0x408
	aiResetCtrlOffset = 0x800
	sicfFGC           = 0x0002
	sicfClockEn       = 0x0001
	aircReset         = 1
)

// CYW43439 chip constants
const (
	chipRAMSize = 512 * 1024
)

// SDPCM channel types
const (
	sdpcmChanControl = 0
	sdpcmChanEvent   = 1
	sdpcmChanData    = 2
)

// IOCTL commands
const (
	ioctlGET = 0
	ioctlSET = 2

	wlcUP         = 2
	wlcSetInfra   = 20
	wlcSetAuth    = 22
	wlcSetSSID    = 26
	wlcDisassoc   = 52
	wlcGetVar     = 262
	wlcSetVar     = 263
	wlcSetAntdiv  = 64
	wlcSetGmode   = 110
	wlcSetWSec    = 134
	wlcSetBand    = 142
	wlcSetPM      = 0x56
	wlcSetWPAAuth = 165
	wlcSetWsecPMK = 268
)

// Security types
const (
	wsecNone = 0
	wsecTKIP = 2
	wsecAES  = 4
)

// WPA auth modes
const (
	wpaAuthDisabled = 0x0000
	wpaAuthWPAPSK   = 0x0004
	wpaAuthWPA2PSK  = 0x0080
	wpaAuthWPA3SAE  = 0x40000
)

// Event types
const (
	evtSetSSID     = 0
	evtJoin        = 1
	evtAuth        = 3
	evtDeauth      = 5
	evtDeauthInd   = 6
	evtAssoc       = 7
	evtAssocInd    = 8
	evtReassocInd  = 10
	evtDisassoc    = 11
	evtDisassocInd = 12
	evtLink        = 16
	evtPSKSup      = 46
)

const (
	evtStatusSuccess = 0
)

// Bluetooth BTSDIO registers and constants
const (
	cywBTBaseAddress = 0x19000000
	bt2wlanPwrupAddr = 0x640894
	bt2wlanPwrupWake = 3

	btCtrlRegAddr      = 0x18000c7c
	hostCtrlRegAddr    = 0x18000d6c
	wlanRAMBaseRegAddr = 0x18000d68

	btsdioRegDataValidBitmask = 1 << 1
	btsdioRegBTAwakeBitmask   = 1 << 8
	btsdioRegWakeBTBitmask    = 1 << 17
	btsdioRegSWReadyBitmask   = 1 << 24
	btsdioRegFWReadyBitmask   = 1 << 24

	btsdioFWBufSize          = 0x1000
	btsdioOffsetHostWriteBuf = 0
	btsdioOffsetHostReadBuf  = btsdioFWBufSize
	btsdioOffsetHost2BTIn    = 0x2000
	btsdioOffsetHost2BTOut   = 0x2004
	btsdioOffsetBT2HostIn    = 0x2008
	btsdioOffsetBT2HostOut   = 0x200C

	// Bluetooth firmware Intel HEX addressing modes
	btfwAddrModeExtended = 1
	btfwAddrModeSegment  = 2
	btfwAddrModeLinear32 = 3

	// Bluetooth firmware Intel HEX line types
	btfwHexLineTypeData                    = 0
	btfwHexLineTypeEndOfData               = 1
	btfwHexLineTypeExtendedSegmentAddress  = 2
	btfwHexLineTypeExtendedAddress         = 4
	btfwHexLineTypeAbsolute32BitAddress    = 5

	// SDIO interrupt for BT
	sdioIntStatus    = 0x20 // offset from sdioBaseAddress
	iHMBFCChange     = 1 << 5
)

