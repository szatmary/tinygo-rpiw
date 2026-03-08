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
	dhcpOptSubnetMask    = 1
	dhcpOptRouter        = 3
	dhcpOptDNS           = 6
	dhcpOptRequestedIP   = 50
	dhcpOptLeaseTime     = 51
	dhcpOptMsgType       = 53
	dhcpOptServerID      = 54
	dhcpOptParamReqList  = 55
	dhcpOptEnd           = 255
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
	stack    *Stack
	state    dhcpState
	xid      uint32
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
				// Lease expired, start over
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

	// Verify it's a reply (op=2) and matches our XID
	if data[0] != 2 {
		return
	}
	xid := uint32(data[4])<<24 | uint32(data[5])<<16 | uint32(data[6])<<8 | uint32(data[7])
	if xid != d.xid {
		return
	}

	// Your IP address (yiaddr)
	yiaddr := netip.AddrFrom4([4]byte{data[16], data[17], data[18], data[19]})

	// Parse options starting at offset 240 (after magic cookie)
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
			// Fall back to router as DNS if not provided
			if !dns.IsValid() && router.IsValid() {
				dns = router
			}
			d.stack.SetAddr(yiaddr, router, subnet, dns)
			// Proactively ARP for gateway so first TCP/DNS doesn't stall
			if router.IsValid() {
				d.stack.sendARPRequest(router)
			}
		}
	case dhcpNAK:
		d.state = dhcpInit
	}
}

func (d *dhcpClient) sendDiscover() error {
	pkt := d.buildPacket(dhcpDiscover, netip.Addr{}, netip.Addr{})
	d.retxTime = d.stack.now().Add(4 * time.Second)

	broadcast := netip.AddrFrom4([4]byte{255, 255, 255, 255})
	return d.stack.sendUDP(dhcpClientPort, dhcpServerPort, broadcast, pkt)
}

func (d *dhcpClient) sendRequest() error {
	pkt := d.buildPacket(dhcpRequest, d.offeredIP, d.serverIP)
	d.retxTime = d.stack.now().Add(4 * time.Second)

	broadcast := netip.AddrFrom4([4]byte{255, 255, 255, 255})
	return d.stack.sendUDP(dhcpClientPort, dhcpServerPort, broadcast, pkt)
}

func (d *dhcpClient) buildPacket(msgType uint8, requestedIP, serverID netip.Addr) []byte {
	// BOOTP header (236 bytes) + magic cookie (4) + options
	pkt := make([]byte, 300)

	pkt[0] = 1    // op: BOOTREQUEST
	pkt[1] = 1    // htype: Ethernet
	pkt[2] = 6    // hlen: MAC address length
	pkt[3] = 0    // hops
	pkt[10] = 0x80 // flags: BROADCAST (server must reply via broadcast)

	// XID
	pkt[4] = byte(d.xid >> 24)
	pkt[5] = byte(d.xid >> 16)
	pkt[6] = byte(d.xid >> 8)
	pkt[7] = byte(d.xid)

	// Client MAC
	mac := d.stack.mac
	copy(pkt[28:34], mac[:])

	// Magic cookie
	pkt[236] = 0x63
	pkt[237] = 0x82
	pkt[238] = 0x53
	pkt[239] = 0x63

	// Options
	i := 240

	// Message type
	pkt[i] = dhcpOptMsgType
	pkt[i+1] = 1
	pkt[i+2] = msgType
	i += 3

	// Requested IP (for REQUEST)
	if requestedIP.IsValid() {
		pkt[i] = dhcpOptRequestedIP
		pkt[i+1] = 4
		ip4 := requestedIP.As4()
		copy(pkt[i+2:i+6], ip4[:])
		i += 6
	}

	// Server ID (for REQUEST)
	if serverID.IsValid() {
		pkt[i] = dhcpOptServerID
		pkt[i+1] = 4
		ip4 := serverID.As4()
		copy(pkt[i+2:i+6], ip4[:])
		i += 6
	}

	// Parameter request list
	pkt[i] = dhcpOptParamReqList
	pkt[i+1] = 3
	pkt[i+2] = dhcpOptSubnetMask
	pkt[i+3] = dhcpOptRouter
	pkt[i+4] = dhcpOptDNS
	i += 5

	// End
	pkt[i] = dhcpOptEnd
	i++

	return pkt[:i]
}

// IsBound returns true if DHCP has obtained an IP address.
func (d *dhcpClient) IsBound() bool {
	return d.state == dhcpBound
}
