package tinygorpiw

import (
	"time"
	"unsafe"
)

// doIoctl sends an IOCTL command and waits for response.
func (d *Device) doIoctl(kind uint32, cmd uint32, iface uint32, data []byte) error {
	_, err := d.doIoctlGet(kind, cmd, iface, data)
	return err
}

// doIoctlGet sends an IOCTL and returns the response data.
func (d *Device) doIoctlGet(kind uint32, cmd uint32, iface uint32, data []byte) ([]byte, error) {
	d.ioctlID++
	id := d.ioctlID

	// Build SDPCM + CDC headers + payload
	totalLen := sdpcmHeaderSize + cdcHeaderSize + len(data)
	// Pad to 4-byte alignment
	paddedLen := (totalLen + 3) &^ 3

	// Use pre-allocated ioBuf
	words := paddedLen / 4
	if words > len(d.txBuf) {
		return nil, errPacketTooLong
	}

	// Wait for bus credit BEFORE encoding header (seq must not advance yet)
	if err := d.waitCredit(500 * time.Millisecond); err != nil {
		return nil, err
	}

	// Clear buffer
	for i := range d.txBuf[:words] {
		d.txBuf[i] = 0
	}

	// Encode into byte view of txBuf (no allocation)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&d.txBuf[0])), words*4)

	// SDPCM header
	sdpcm := SDPCMHeader{
		Size:         uint16(totalLen),
		SizeCom:      ^uint16(totalLen),
		Seq:          d.sdpcmSeq,
		ChanAndFlags: sdpcmChanControl,
		HeaderLength: sdpcmHeaderSize,
	}
	sdpcm.Put(buf[0:sdpcmHeaderSize])
	d.sdpcmSeq++

	// CDC header
	flags := uint16(kind)
	flags |= uint16(iface) << 12
	cdc := CDCHeader{
		Cmd:    cmd,
		Length: uint32(len(data)),
		Flags:  flags,
		ID:     id,
	}
	cdc.Put(buf[sdpcmHeaderSize : sdpcmHeaderSize+cdcHeaderSize])

	// Payload
	if len(data) > 0 {
		copy(buf[sdpcmHeaderSize+cdcHeaderSize:], data)
	}

	// Send via WLAN function
	if err := d.wlan_write(d.txBuf[:words], uint32(paddedLen)); err != nil {
		return nil, err
	}

	// Wait for response
	return d.waitIoctlResponse(id, 2*time.Second)
}

// setIovar sets an iovar by name. Uses _iovarBuf as scratch (zero allocations).
func (d *Device) setIovar(name string, data []byte) error {
	buf := d.iovarBytes()
	n := copy(buf, name)
	buf[n] = 0
	n++
	copy(buf[n:], data)
	return d.doIoctl(ioctlSET, wlcSetVar, 0, buf[:n+len(data)])
}

// setIovarU32 sets an iovar with a uint32 value.
func (d *Device) setIovarU32(name string, val uint32) error {
	var buf [4]byte
	buf[0] = byte(val)
	buf[1] = byte(val >> 8)
	buf[2] = byte(val >> 16)
	buf[3] = byte(val >> 24)
	return d.setIovar(name, buf[:])
}

// getIovar gets an iovar by name. Uses _iovarBuf as scratch (zero allocations).
func (d *Device) getIovar(name string, respLen int) ([]byte, error) {
	buf := d.iovarBytes()
	n := copy(buf, name)
	buf[n] = 0
	n++
	for i := range buf[n : n+respLen] {
		buf[n+i] = 0
	}
	return d.doIoctlGet(ioctlGET, wlcGetVar, 0, buf[:n+respLen])
}

// iovarBytes returns a byte view of _iovarBuf (no allocation).
func (d *Device) iovarBytes() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&d._iovarBuf[0])), len(d._iovarBuf)*4)
}

// waitCredit waits until bus data credit is available.
func (d *Device) waitCredit(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.sdpcmSeq != d.sdpcmSeqMax {
			return nil
		}
		// Poll to get updated status/credits
		if err := d.poll(); err != nil {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return errNoCredit
}

// waitIoctlResponse polls for an IOCTL response matching the given ID.
func (d *Device) waitIoctlResponse(id uint16, timeout time.Duration) ([]byte, error) {
	d.ioctlPending = true
	d.ioctlRespID = id
	d.ioctlRespBuf = nil

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := d.poll(); err != nil {
			return nil, err
		}
		if !d.ioctlPending {
			return d.ioctlRespBuf, nil
		}
		time.Sleep(time.Millisecond)
	}
	d.ioctlPending = false
	return nil, errTimeout
}

// poll checks for and processes any pending packets from the chip.
func (d *Device) poll() error {
	// Read status register to refresh status
	_, err := d.read32(FuncBus, spiStatusRegister)
	if err != nil {
		return err
	}

	if !d.spi.status.F2PacketAvailable() {
		return nil
	}

	length := int(d.spi.status.F2PacketLength())
	if length == 0 {
		return nil
	}

	// Read the packet
	words := (length + 3) / 4
	if words > len(d.rxBuf) {
		return errPacketTooLong
	}

	if err := d.wlan_read(d.rxBuf[:words], length); err != nil {
		return err
	}

	buf := unsafe.Slice((*byte)(unsafe.Pointer(&d.rxBuf[0])), words*4)
	return d.handleRxPacket(buf[:length])
}

// handleRxPacket dispatches a received packet based on SDPCM channel.
func (d *Device) handleRxPacket(pkt []byte) error {
	if len(pkt) < sdpcmHeaderSize {
		return nil
	}

	var sdpcm SDPCMHeader
	sdpcm.Parse(pkt)

	// Validate size complement
	if sdpcm.Size^sdpcm.SizeCom != 0xFFFF {
		return nil // corrupted
	}

	// Update bus credits (with sanity check matching soypat/embassy-rs).
	if sdpcm.BusDataCredit > 0 {
		max := sdpcm.BusDataCredit
		if (max - d.sdpcmSeq) > 0x40 {
			max = d.sdpcmSeq + 2
		}
		d.sdpcmSeqMax = max
	}

	channel := sdpcm.Channel()
	if int(sdpcm.HeaderLength) > len(pkt) {
		return nil // corrupted header length
	}
	payload := pkt[sdpcm.HeaderLength:]

	switch channel {
	case sdpcmChanControl:
		return d.handleControl(payload)
	case sdpcmChanEvent:
		return d.handleEvent(payload)
	case sdpcmChanData:
		return d.handleData(payload)
	}
	return nil
}

// handleControl processes IOCTL responses.
func (d *Device) handleControl(payload []byte) error {
	if len(payload) < cdcHeaderSize {
		return nil
	}
	var cdc CDCHeader
	cdc.Parse(payload)

	if d.ioctlPending && cdc.ID == d.ioctlRespID {
		d.ioctlPending = false
		if cdc.Status != 0 {
			println("  [ioctl] FAIL cmd=", cdc.Cmd, "len=", cdc.Length, "flags=", cdc.Flags, "id=", cdc.ID, "status=", cdc.Status)
			return errIOCTL
		}
		dataStart := cdcHeaderSize
		dataEnd := dataStart + int(cdc.Length)
		if dataEnd > len(payload) {
			dataEnd = len(payload)
		}
		if dataEnd > dataStart {
			// Copy response into rxBuf (already fully consumed at this point).
			// This avoids aliasing _iovarBuf which is used as scratch by
			// setIovar/getIovar/setBsscfgIovar32.
			respBuf := unsafe.Slice((*byte)(unsafe.Pointer(&d.rxBuf[0])), len(d.rxBuf)*4)
			n := copy(respBuf, payload[dataStart:dataEnd])
			d.ioctlRespBuf = respBuf[:n]
		}
	}
	return nil
}

// handleEvent processes async events from firmware.
func (d *Device) handleEvent(payload []byte) error {
	if len(payload) < bdcHeaderSize {
		return nil
	}
	var bdc BDCHeader
	bdc.Parse(payload)

	eventOffset := bdcHeaderSize + int(bdc.DataOffset)*4
	if eventOffset > len(payload) {
		return nil
	}
	eventData := payload[eventOffset:]

	// Event packets include an Ethernet header (14 bytes) before the
	// BCM event header and message. Minimum: 14 + 10 + 48 = 72 bytes.
	const ethHeaderLen = 14
	if len(eventData) < ethHeaderLen+eventHeaderSize+eventMessageSize {
		return nil
	}
	eventData = eventData[ethHeaderLen:] // skip Ethernet header

	var evtMsg EventMessage
	evtMsg.Parse(eventData[eventHeaderSize:])

	d.processEvent(&evtMsg)
	return nil
}

// processEvent updates device state based on WiFi events.
func (d *Device) processEvent(evt *EventMessage) {
	switch evt.EventType {
	case evtAuth:
		d.authOK = evt.Status == evtStatusSuccess

	case evtAssoc:
		// Association complete

	case evtDeauth, evtDeauthInd:
		d.authOK = false
		d.joinOK = false
		d.keyExchangeOK = false
		d.linkUp = false

	case evtDisassoc, evtDisassocInd:
		d.joinOK = false
		d.keyExchangeOK = false
		d.linkUp = false

	case evtSetSSID:
		d.joinOK = evt.Status == evtStatusSuccess

	case evtLink:
		if evt.Flags&1 != 0 {
			// Link up
			d.linkUp = true
		} else {
			d.linkUp = false
			d.joinOK = false
			d.keyExchangeOK = false
		}

	case evtPSKSup:
		if evt.Status == 6 { // WLC_SUP_KEYED
			d.keyExchangeOK = true
		}
	}
}

// handleData processes received data frames (Ethernet packets).
func (d *Device) handleData(payload []byte) error {
	if len(payload) < bdcHeaderSize {
		return nil
	}
	var bdc BDCHeader
	bdc.Parse(payload)

	ethStart := bdcHeaderSize + int(bdc.DataOffset)*4
	if ethStart >= len(payload) {
		return nil
	}

	ethFrame := payload[ethStart:]
	if d.rcvEth != nil {
		d.rcvEth(ethFrame)
	}
	return nil
}
