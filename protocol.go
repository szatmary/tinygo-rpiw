package tinygorpiw

import "encoding/binary"

// SDPCMHeader is the SDPCM bus protocol header (12 bytes).
// Wraps every packet on the bus between host and CYW43439.
type SDPCMHeader struct {
	Size          uint16
	SizeCom       uint16 // ~Size (complement for error detection)
	Seq           uint8
	ChanAndFlags  uint8
	NextLength    uint8
	HeaderLength  uint8
	WirelessFlow  uint8
	BusDataCredit uint8
	Reserved      [2]uint8
}

const sdpcmHeaderSize = 12

func (h *SDPCMHeader) Channel() uint8 {
	return h.ChanAndFlags & 0x0F
}

func (h *SDPCMHeader) Put(buf []byte) {
	binary.LittleEndian.PutUint16(buf[0:2], h.Size)
	binary.LittleEndian.PutUint16(buf[2:4], h.SizeCom)
	buf[4] = h.Seq
	buf[5] = h.ChanAndFlags
	buf[6] = h.NextLength
	buf[7] = h.HeaderLength
	buf[8] = h.WirelessFlow
	buf[9] = h.BusDataCredit
	buf[10] = h.Reserved[0]
	buf[11] = h.Reserved[1]
}

func (h *SDPCMHeader) Parse(buf []byte) {
	h.Size = binary.LittleEndian.Uint16(buf[0:2])
	h.SizeCom = binary.LittleEndian.Uint16(buf[2:4])
	h.Seq = buf[4]
	h.ChanAndFlags = buf[5]
	h.NextLength = buf[6]
	h.HeaderLength = buf[7]
	h.WirelessFlow = buf[8]
	h.BusDataCredit = buf[9]
	h.Reserved[0] = buf[10]
	h.Reserved[1] = buf[11]
}

// CDCHeader is the Control Data Channel header (16 bytes).
// Used for IOCTL commands and responses.
type CDCHeader struct {
	Cmd    uint32
	Length uint32
	Flags  uint16
	ID     uint16
	Status uint32
}

const cdcHeaderSize = 16

func (h *CDCHeader) Put(buf []byte) {
	binary.LittleEndian.PutUint32(buf[0:4], h.Cmd)
	binary.LittleEndian.PutUint32(buf[4:8], h.Length)
	binary.LittleEndian.PutUint16(buf[8:10], h.Flags)
	binary.LittleEndian.PutUint16(buf[10:12], h.ID)
	binary.LittleEndian.PutUint32(buf[12:16], h.Status)
}

func (h *CDCHeader) Parse(buf []byte) {
	h.Cmd = binary.LittleEndian.Uint32(buf[0:4])
	h.Length = binary.LittleEndian.Uint32(buf[4:8])
	h.Flags = binary.LittleEndian.Uint16(buf[8:10])
	h.ID = binary.LittleEndian.Uint16(buf[10:12])
	h.Status = binary.LittleEndian.Uint32(buf[12:16])
}

// BDCHeader is the Broadcom Data Channel header (4 bytes).
// Wraps Ethernet data frames.
type BDCHeader struct {
	Flags      uint8
	Priority   uint8
	Flags2     uint8
	DataOffset uint8
}

const bdcHeaderSize = 4

func (h *BDCHeader) Put(buf []byte) {
	buf[0] = h.Flags
	buf[1] = h.Priority
	buf[2] = h.Flags2
	buf[3] = h.DataOffset
}

func (h *BDCHeader) Parse(buf []byte) {
	h.Flags = buf[0]
	h.Priority = buf[1]
	h.Flags2 = buf[2]
	h.DataOffset = buf[3]
}

// EventHeader wraps async events from the firmware.
type EventHeader struct {
	Subtype uint16
	Length  uint16
	Version uint8
	OUI     [3]uint8
	UserSubtype uint16
}

const eventHeaderSize = 10

func (h *EventHeader) Parse(buf []byte) {
	h.Subtype = binary.LittleEndian.Uint16(buf[0:2])
	h.Length = binary.LittleEndian.Uint16(buf[2:4])
	h.Version = buf[4]
	h.OUI[0] = buf[5]
	h.OUI[1] = buf[6]
	h.OUI[2] = buf[7]
	h.UserSubtype = binary.LittleEndian.Uint16(buf[8:10])
}

// EventMessage contains the event details (48 bytes).
type EventMessage struct {
	Version   uint16
	Flags     uint16
	EventType uint32
	Status    uint32
	Reason    uint32
	AuthType  uint32
	DataLen   uint32
	Addr      [6]uint8
	IFName    [16]uint8
	IFIdx     uint8
	BSScfgIdx uint8
}

const eventMessageSize = 48

func (h *EventMessage) Parse(buf []byte) {
	h.Version = binary.BigEndian.Uint16(buf[0:2])
	h.Flags = binary.BigEndian.Uint16(buf[2:4])
	h.EventType = binary.BigEndian.Uint32(buf[4:8])
	h.Status = binary.BigEndian.Uint32(buf[8:12])
	h.Reason = binary.BigEndian.Uint32(buf[12:16])
	h.AuthType = binary.BigEndian.Uint32(buf[16:20])
	h.DataLen = binary.BigEndian.Uint32(buf[20:24])
	copy(h.Addr[:], buf[24:30])
	copy(h.IFName[:], buf[30:46])
	h.IFIdx = buf[46]
	h.BSScfgIdx = buf[47]
}
