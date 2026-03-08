package stack

import (
	"net/netip"
	"time"
)

const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpACK      = 5
	dhcpNAK      = 6

	dhcpClientPort = 68
	dhcpServerPort = 67

	dhcpMagicCookie = 0x63825363
)

// DHCP option codes
const (
	dhcpOptSubnetMask   = 1
	dhcpOptRouter       = 3
	dhcpOptDNS          = 6
	dhcpOptRequestedIP  = 50
	dhcpOptLeaseTime    = 51
	dhcpOptMsgType      = 53
	dhcpOptServerID     = 54
	dhcpOptParamReqList = 55
	dhcpOptEnd          = 255
)

type dhcpState uint8

const (
	dhcpInit dhcpState = iota
	dhcpSelecting
	dhcpRequesting
	dhcpBound
	dhcpRenewing
)

type dhcpClient struct {
	stack     *Stack
	state     dhcpState
	xid       uint32
	offeredIP netip.Addr
	serverIP  netip.Addr
	leaseTime time.Duration
	renewTime time.Time
	retxTime  time.Time
	retxCount uint8
}

func (d *dhcpClient) init(s *Stack) {
	d.stack = s
	d.state = dhcpInit
	d.xid = 0xDEADBEEF
}

// Start initiates DHCP discovery.
func (d *dhcpClient) Start() error {
	d.state = dhcpSelecting
	d.retxCount = 0
	return d.sendDiscover()
}

func (d *dhcpClient) poll(now time.Time) {
	switch d.state {
	case dhcpSelecting, dhcpRequesting:
		if !d.retxTime.IsZero() && now.After(d.retxTime) {
			d.retxCount++
			if d.retxCount > 5 {
				d.state = dhcpInit
				return
			}
			if d.state == dhcpSelecting {
				d.sendDiscover()
			} else {
				d.sendRequest()
			}
		}
	case dhcpBound:
		if !d.renewTime.IsZero() && now.After(d.renewTime) {
			d.state = dhcpRenewing
			d.sendRequest()
		}
	case dhcpRenewing:
		if !d.retxTime.IsZero() && now.After(d.retxTime) {
			d.retxCount++
			if d.retxCount > 3 {
				d.state = dhcpInit
				d.Start()
				return
			}
			d.sendRequest()
		}
	}
}

func (d *dhcpClient) handlePacket(data []byte) {
	if len(data) < 240 {
		return
	}

	if data[0] != 2 {
		return
	}
	xid := uint32(data[4])<<24 | uint32(data[5])<<16 | uint32(data[6])<<8 | uint32(data[7])
	if xid != d.xid {
		return
	}

	yiaddr := netip.AddrFrom4([4]byte{data[16], data[17], data[18], data[19]})

	if uint32(data[236])<<24|uint32(data[237])<<16|uint32(data[238])<<8|uint32(data[239]) != dhcpMagicCookie {
		return
	}

	var msgType uint8
	var subnet, router, dns, serverID netip.Addr
	var leaseSeconds uint32

	opts := data[240:]
	for i := 0; i < len(opts); {
		opt := opts[i]
		if opt == dhcpOptEnd {
			break
		}
		if opt == 0 {
			i++
			continue
		}
		if i+1 >= len(opts) {
			break
		}
		optLen := int(opts[i+1])
		if i+2+optLen > len(opts) {
			break
		}
		val := opts[i+2 : i+2+optLen]

		switch opt {
		case dhcpOptMsgType:
			if optLen >= 1 {
				msgType = val[0]
			}
		case dhcpOptSubnetMask:
			if optLen >= 4 {
				subnet = netip.AddrFrom4([4]byte{val[0], val[1], val[2], val[3]})
			}
		case dhcpOptRouter:
			if optLen >= 4 {
				router = netip.AddrFrom4([4]byte{val[0], val[1], val[2], val[3]})
			}
		case dhcpOptDNS:
			if optLen >= 4 {
				dns = netip.AddrFrom4([4]byte{val[0], val[1], val[2], val[3]})
			}
		case dhcpOptServerID:
			if optLen >= 4 {
				serverID = netip.AddrFrom4([4]byte{val[0], val[1], val[2], val[3]})
			}
		case dhcpOptLeaseTime:
			if optLen >= 4 {
				leaseSeconds = uint32(val[0])<<24 | uint32(val[1])<<16 | uint32(val[2])<<8 | uint32(val[3])
			}
		}
		i += 2 + optLen
	}

	switch msgType {
	case dhcpOffer:
		if d.state == dhcpSelecting {
			d.offeredIP = yiaddr
			d.serverIP = serverID
			d.state = dhcpRequesting
			d.retxCount = 0
			d.sendRequest()
		}
	case dhcpACK:
		if d.state == dhcpRequesting || d.state == dhcpRenewing {
			d.state = dhcpBound
			d.leaseTime = time.Duration(leaseSeconds) * time.Second
			d.renewTime = d.stack.now().Add(d.leaseTime / 2)
			if !dns.IsValid() && router.IsValid() {
				dns = router
			}
			d.stack.SetAddr(yiaddr, router, subnet, dns)
			if router.IsValid() {
				d.stack.sendARPRequest(router)
			}
		}
	case dhcpNAK:
		d.state = dhcpInit
	}
}

func (d *dhcpClient) sendDiscover() error {
	var buf [300]byte
	n := d.buildPacket(dhcpDiscover, netip.Addr{}, netip.Addr{}, buf[:])
	d.retxTime = d.stack.now().Add(4 * time.Second)

	broadcast := netip.AddrFrom4([4]byte{255, 255, 255, 255})
	return d.stack.sendUDP(dhcpClientPort, dhcpServerPort, broadcast, buf[:n])
}

func (d *dhcpClient) sendRequest() error {
	var buf [300]byte
	n := d.buildPacket(dhcpRequest, d.offeredIP, d.serverIP, buf[:])
	d.retxTime = d.stack.now().Add(4 * time.Second)

	broadcast := netip.AddrFrom4([4]byte{255, 255, 255, 255})
	return d.stack.sendUDP(dhcpClientPort, dhcpServerPort, broadcast, buf[:n])
}

// buildPacket writes a DHCP packet into buf and returns the length.
// No allocations.
func (d *dhcpClient) buildPacket(msgType uint8, requestedIP, serverID netip.Addr, buf []byte) int {
	// Clear the buffer
	for i := range buf {
		buf[i] = 0
	}

	buf[0] = 1    // op: BOOTREQUEST
	buf[1] = 1    // htype: Ethernet
	buf[2] = 6    // hlen: MAC address length
	buf[3] = 0    // hops
	buf[10] = 0x80 // flags: BROADCAST

	// XID
	buf[4] = byte(d.xid >> 24)
	buf[5] = byte(d.xid >> 16)
	buf[6] = byte(d.xid >> 8)
	buf[7] = byte(d.xid)

	// Client MAC
	mac := d.stack.mac
	copy(buf[28:34], mac[:])

	// Magic cookie
	buf[236] = 0x63
	buf[237] = 0x82
	buf[238] = 0x53
	buf[239] = 0x63

	// Options
	i := 240

	buf[i] = dhcpOptMsgType
	buf[i+1] = 1
	buf[i+2] = msgType
	i += 3

	if requestedIP.IsValid() {
		buf[i] = dhcpOptRequestedIP
		buf[i+1] = 4
		ip4 := requestedIP.As4()
		copy(buf[i+2:i+6], ip4[:])
		i += 6
	}

	if serverID.IsValid() {
		buf[i] = dhcpOptServerID
		buf[i+1] = 4
		ip4 := serverID.As4()
		copy(buf[i+2:i+6], ip4[:])
		i += 6
	}

	buf[i] = dhcpOptParamReqList
	buf[i+1] = 3
	buf[i+2] = dhcpOptSubnetMask
	buf[i+3] = dhcpOptRouter
	buf[i+4] = dhcpOptDNS
	i += 5

	buf[i] = dhcpOptEnd
	i++

	return i
}

// IsBound returns true if DHCP has obtained an IP address.
func (d *dhcpClient) IsBound() bool {
	return d.state == dhcpBound
}
