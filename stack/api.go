// Package stack API - the public methods called by the Netdever adapter.
package stack

import (
	"net/netip"
	"time"
)

// Socket creates a new socket. stype is SOCK_STREAM (1) or SOCK_DGRAM (2).
func (s *Stack) Socket(stype int) (int, error) {
	fd, err := s.allocSocket()
	if err != nil {
		return -1, err
	}
	sock := &s.sockets[fd]
	sock.state = sockBound
	switch stype {
	case 1: // SOCK_STREAM
		sock.protocol = protoTCP
		sock.tcp.state = tcpClosed
		sock.tcp.mss = tcpMaxSegSize
	case 2: // SOCK_DGRAM
		sock.protocol = protoUDP
	default:
		sock.reset()
		return -1, errBadSocket
	}
	return fd, nil
}

// Bind binds a socket to a local port.
func (s *Stack) Bind(fd int, port uint16) error {
	sock, err := s.getSocket(fd)
	if err != nil {
		return err
	}
	sock.localPort = port
	sock.localAddr = s.localIP
	return nil
}

// Connect initiates a connection to a remote host.
func (s *Stack) Connect(fd int, addr netip.Addr, port uint16) error {
	sock, err := s.getSocket(fd)
	if err != nil {
		return err
	}

	sock.remoteAddr = addr
	sock.remotePort = port
	sock.localAddr = s.localIP
	if sock.localPort == 0 {
		sock.localPort = s.nextEphemeralPort()
	}

	if sock.protocol == protoUDP {
		sock.state = sockConnected
		return nil
	}

	// TCP: initiate 3-way handshake
	sock.state = sockConnecting
	sock.tcp.state = tcpSynSent
	sock.tcp.seqNum = s.tcpISN()
	sock.tcp.sndUna = sock.tcp.seqNum
	sock.tcp.ackNum = 0

	// Send SYN
	s.sendTCPFlags(sock, tcpSYN)
	sock.tcp.retxTime = s.now().Add(tcpRetxTimeout)

	// Wait for connection
	deadline := s.now().Add(10 * time.Second)
	for s.now().Before(deadline) {
		if err := s.Poll(); err != nil {
			return err
		}
		if sock.state == sockConnected {
			return nil
		}
		if sock.state == sockClosed {
			return errConnRefused
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errTimeout
}

// Listen marks a TCP socket as listening for incoming connections.
func (s *Stack) Listen(fd int) error {
	sock, err := s.getSocket(fd)
	if err != nil {
		return err
	}
	if sock.protocol != protoTCP {
		return errBadSocket
	}
	sock.state = sockListening
	sock.pendingConn = -1
	return nil
}

// Accept waits for and accepts an incoming connection.
func (s *Stack) Accept(fd int, timeout time.Duration) (int, netip.AddrPort, error) {
	sock, err := s.getSocket(fd)
	if err != nil {
		return -1, netip.AddrPort{}, err
	}
	if sock.state != sockListening {
		return -1, netip.AddrPort{}, errBadSocket
	}

	deadline := s.now().Add(timeout)
	for s.now().Before(deadline) {
		if err := s.Poll(); err != nil {
			return -1, netip.AddrPort{}, err
		}
		if sock.pendingConn >= 0 {
			newFD := sock.pendingConn
			sock.pendingConn = -1
			newSock := &s.sockets[newFD]
			remote := netip.AddrPortFrom(newSock.remoteAddr, newSock.remotePort)
			return newFD, remote, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return -1, netip.AddrPort{}, errTimeout
}

// Send writes data to a connected socket.
func (s *Stack) Send(fd int, data []byte, deadline time.Time) (int, error) {
	sock, err := s.getSocket(fd)
	if err != nil {
		return 0, err
	}

	if sock.protocol == protoUDP {
		return len(data), s.sendUDP(sock.localPort, sock.remotePort, sock.remoteAddr, data)
	}

	// TCP: write to tx buffer and trigger output
	if sock.state != sockConnected {
		return 0, errNotConnected
	}

	total := 0
	for total < len(data) {
		// Wait for space in tx buffer
		for sock.txFree() == 0 {
			if !deadline.IsZero() && s.now().After(deadline) {
				return total, errTimeout
			}
			if err := s.Poll(); err != nil {
				return total, err
			}
			s.tcpOutput(sock)
			time.Sleep(time.Millisecond)
		}

		n := sock.txWrite(data[total:])
		total += n
		s.tcpOutput(sock)
	}
	return total, nil
}

// Recv reads data from a connected socket.
func (s *Stack) Recv(fd int, buf []byte, deadline time.Time) (int, error) {
	sock, err := s.getSocket(fd)
	if err != nil {
		return 0, err
	}

	// Wait for data
	for sock.rxAvailable() == 0 {
		// Check if connection is closed
		if sock.state == sockClosed || sock.tcp.state == tcpClosed {
			return 0, errClosed
		}
		if sock.tcp.state == tcpCloseWait && sock.rxAvailable() == 0 {
			return 0, errClosed
		}
		if !deadline.IsZero() && s.now().After(deadline) {
			return 0, errTimeout
		}
		if err := s.Poll(); err != nil {
			return 0, err
		}
		time.Sleep(time.Millisecond)
	}

	n := sock.rxRead(buf)
	return n, nil
}

// Close closes a socket.
func (s *Stack) Close(fd int) error {
	sock, err := s.getSocket(fd)
	if err != nil {
		return err
	}

	if sock.protocol == protoUDP {
		sock.reset()
		return nil
	}

	// TCP close
	switch sock.tcp.state {
	case tcpEstablished:
		sock.tcp.state = tcpFinWait1
		sock.state = sockClosing
		s.sendTCPFlags(sock, tcpFIN|tcpACK)
		sock.tcp.finSent = true
	case tcpCloseWait:
		sock.tcp.state = tcpLastAck
		sock.state = sockClosing
		s.sendTCPFlags(sock, tcpFIN|tcpACK)
		sock.tcp.finSent = true
	default:
		sock.reset()
	}

	return nil
}

// DHCPStart initiates DHCP address acquisition.
func (s *Stack) DHCPStart() error {
	return s.dhcp.Start()
}

// DHCPBound returns true if DHCP has obtained an IP address.
func (s *Stack) DHCPBound() bool {
	return s.dhcp.IsBound()
}

// WaitDHCP blocks until DHCP is bound or timeout.
func (s *Stack) WaitDHCP(timeout time.Duration) error {
	deadline := s.now().Add(timeout)
	for s.now().Before(deadline) {
		if err := s.Poll(); err != nil {
			return err
		}
		if s.dhcp.IsBound() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errDHCPFailed
}

// DNSResolve resolves a hostname to an IP address.
func (s *Stack) DNSResolve(name string, timeout time.Duration) (netip.Addr, error) {
	// Try parsing as IP first
	if addr, err := netip.ParseAddr(name); err == nil {
		return addr, nil
	}
	return s.dns.Resolve(name, timeout)
}

// NTPSync queries an NTP server and returns the current wall-clock time.
func (s *Stack) NTPSync(server netip.Addr, timeout time.Duration) (time.Time, error) {
	return s.ntp.Sync(server, timeout)
}

// SetHostname enables the mDNS responder for "name.local".
// The caller must also add MdnsMulticastMAC to the hardware multicast filter.
func (s *Stack) SetHostname(name string) {
	s.mdns.SetHostname(name)
}

// AddService registers a DNS-SD service for mDNS advertisement.
// Returns false if the service table is full.
func (s *Stack) AddService(svc Service) bool {
	return s.mdns.AddService(svc)
}
