package stack

import "net/netip"

const maxDHCPLeases = 8

type dhcpLease struct {
	mac  [6]byte
	ip   netip.Addr
	used bool
}

type dhcpServer struct {
	stack   *Stack
	enabled bool
	leases  [maxDHCPLeases]dhcpLease
	baseIP  netip.Addr // First assignable IP (e.g., 192.168.4.2)
	subnet  netip.Addr
	gateway netip.Addr // AP's own IP
}

func (d *dhcpServer) init(s *Stack) {
	d.stack = s
}

// Start enables the DHCP server with the given AP parameters.
func (d *dhcpServer) Start(gateway, subnet netip.Addr) {
	d.enabled = true
	d.gateway = gateway
	d.subnet = subnet
	// Compute base IP: gateway + 1
	g := gateway.As4()
	g[3]++
	d.baseIP = netip.AddrFrom4(g)
}

// handlePacket processes an incoming DHCP client message.
func (d *dhcpServer) handlePacket(data []byte) {
	if !d.enabled || len(data) < 240 {
		return
	}
	if data[0] != 1 { // only BOOTREQUEST
		return
	}

	xid := [4]byte{data[4], data[5], data[6], data[7]}
	var clientMAC [6]byte
	copy(clientMAC[:], data[28:34])

	// Parse magic cookie
	if uint32(data[236])<<24|uint32(data[237])<<16|uint32(data[238])<<8|uint32(data[239]) != dhcpMagicCookie {
		return
	}

	var msgType uint8
	var requestedIP netip.Addr
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
		case dhcpOptRequestedIP:
			if optLen >= 4 {
				requestedIP = netip.AddrFrom4([4]byte{val[0], val[1], val[2], val[3]})
			}
		}
		i += 2 + optLen
	}

	switch msgType {
	case dhcpDiscover:
		ip := d.allocate(clientMAC)
		if ip.IsValid() {
			d.sendOffer(xid, clientMAC, ip)
		}
	case dhcpRequest:
		ip := d.lookup(clientMAC)
		if !ip.IsValid() && requestedIP.IsValid() {
			ip = d.allocate(clientMAC)
		}
		if ip.IsValid() {
			d.sendACK(xid, clientMAC, ip)
		}
	}
}

// allocate finds or creates a lease for the given MAC.
func (d *dhcpServer) allocate(mac [6]byte) netip.Addr {
	// Check existing lease
	for i := range d.leases {
		if d.leases[i].used && d.leases[i].mac == mac {
			return d.leases[i].ip
		}
	}
	// Find free slot
	base := d.baseIP.As4()
	for i := range d.leases {
		if !d.leases[i].used {
			ip := base
			ip[3] += byte(i)
			d.leases[i] = dhcpLease{mac: mac, ip: netip.AddrFrom4(ip), used: true}
			return d.leases[i].ip
		}
	}
	return netip.Addr{} // full
}

// lookup finds an existing lease for the given MAC.
func (d *dhcpServer) lookup(mac [6]byte) netip.Addr {
	for i := range d.leases {
		if d.leases[i].used && d.leases[i].mac == mac {
			return d.leases[i].ip
		}
	}
	return netip.Addr{}
}

// sendOffer sends a DHCP offer to the client.
func (d *dhcpServer) sendOffer(xid [4]byte, clientMAC [6]byte, ip netip.Addr) {
	d.sendReply(dhcpOffer, xid, clientMAC, ip)
}

// sendACK sends a DHCP ACK to the client.
func (d *dhcpServer) sendACK(xid [4]byte, clientMAC [6]byte, ip netip.Addr) {
	d.sendReply(dhcpACK, xid, clientMAC, ip)
}

// sendReply builds and sends a DHCP server reply (offer or ACK).
func (d *dhcpServer) sendReply(msgType uint8, xid [4]byte, clientMAC [6]byte, clientIP netip.Addr) {
	var buf [300]byte

	buf[0] = 2 // op: BOOTREPLY
	buf[1] = 1 // htype: Ethernet
	buf[2] = 6 // hlen
	buf[3] = 0 // hops

	// XID
	copy(buf[4:8], xid[:])

	// yiaddr (your IP address)
	ip4 := clientIP.As4()
	copy(buf[16:20], ip4[:])

	// siaddr (server IP)
	gw4 := d.gateway.As4()
	copy(buf[20:24], gw4[:])

	// chaddr (client MAC)
	copy(buf[28:34], clientMAC[:])

	// Magic cookie
	buf[236] = 0x63
	buf[237] = 0x82
	buf[238] = 0x53
	buf[239] = 0x63

	i := 240

	// Option: message type
	buf[i] = dhcpOptMsgType
	buf[i+1] = 1
	buf[i+2] = msgType
	i += 3

	// Option: subnet mask
	buf[i] = dhcpOptSubnetMask
	buf[i+1] = 4
	sn4 := d.subnet.As4()
	copy(buf[i+2:i+6], sn4[:])
	i += 6

	// Option: router
	buf[i] = dhcpOptRouter
	buf[i+1] = 4
	copy(buf[i+2:i+6], gw4[:])
	i += 6

	// Option: DNS (use gateway as DNS)
	buf[i] = dhcpOptDNS
	buf[i+1] = 4
	copy(buf[i+2:i+6], gw4[:])
	i += 6

	// Option: lease time (1 hour)
	buf[i] = dhcpOptLeaseTime
	buf[i+1] = 4
	buf[i+2] = 0x00
	buf[i+3] = 0x00
	buf[i+4] = 0x0E
	buf[i+5] = 0x10
	i += 6

	// Option: server identifier
	buf[i] = dhcpOptServerID
	buf[i+1] = 4
	copy(buf[i+2:i+6], gw4[:])
	i += 6

	// End
	buf[i] = dhcpOptEnd
	i++

	broadcast := netip.AddrFrom4([4]byte{255, 255, 255, 255})
	d.stack.sendUDP(dhcpServerPort, dhcpClientPort, broadcast, buf[:i])
}
