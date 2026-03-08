package stack

import (
	"net/netip"
	"time"
)

// Socket states
const (
	sockFree       = 0
	sockBound      = 1
	sockListening  = 2
	sockConnecting = 3
	sockConnected  = 4
	sockClosing    = 5
	sockClosed     = 6
)

// Protocol constants
const (
	protoTCP = 6
	protoUDP = 17
)

// Socket represents a network socket with pre-allocated buffers.
type Socket struct {
	state    uint8
	protocol uint8

	localPort  uint16
	remotePort uint16
	localAddr  netip.Addr
	remoteAddr netip.Addr

	// Ring buffers using full uint16 range for head/tail.
	// Empty: head == tail. Full: head - tail == BufSize.
	// Mask with rxMask/txMask when indexing into the array.
	rxBuf  [RxBufSize]byte
	rxHead uint16
	rxTail uint16

	txBuf  [TxBufSize]byte
	txHead uint16
	txTail uint16

	// TCP-specific state
	tcp tcpState

	// For listening sockets: pending connection
	pendingConn int // socket fd of accepted connection, -1 if none
}

type tcpState struct {
	state     uint8
	seqNum    uint32 // next sequence number to send (sndNxt)
	ackNum    uint32
	sndUna    uint32 // oldest unACKed sequence number
	remoteWin uint16
	localWin  uint16
	mss       uint16
	retxTime  time.Time
	retxCount uint8
	finSent   bool
	finAcked  bool
	finRecvd  bool
}

// TCP connection states
const (
	tcpClosed      = 0
	tcpSynSent     = 1
	tcpSynReceived = 2
	tcpEstablished = 3
	tcpFinWait1    = 4
	tcpFinWait2    = 5
	tcpCloseWait   = 6
	tcpLastAck     = 7
	tcpTimeWait    = 8
)

// rxAvailable returns how many bytes are available to read.
func (s *Socket) rxAvailable() int {
	return int(s.rxHead - s.rxTail)
}

// rxFree returns how many bytes can be written to the rx buffer.
func (s *Socket) rxFree() int {
	return RxBufSize - int(s.rxHead-s.rxTail)
}

// rxWrite writes data into the rx ring buffer using batch copy.
func (s *Socket) rxWrite(data []byte) int {
	free := s.rxFree()
	n := len(data)
	if n > free {
		n = free
	}
	if n == 0 {
		return 0
	}

	head := s.rxHead & rxMask
	// First segment: head to end of buffer
	first := int(RxBufSize - head)
	if first > n {
		first = n
	}
	copy(s.rxBuf[head:], data[:first])
	// Second segment: wrap around to beginning
	if first < n {
		copy(s.rxBuf[:], data[first:n])
	}
	s.rxHead += uint16(n)
	return n
}

// rxRead reads data from the rx ring buffer using batch copy.
func (s *Socket) rxRead(buf []byte) int {
	avail := s.rxAvailable()
	n := len(buf)
	if n > avail {
		n = avail
	}
	if n == 0 {
		return 0
	}

	tail := s.rxTail & rxMask
	first := int(RxBufSize - tail)
	if first > n {
		first = n
	}
	copy(buf[:first], s.rxBuf[tail:])
	if first < n {
		copy(buf[first:n], s.rxBuf[:])
	}
	s.rxTail += uint16(n)
	return n
}

// txAvailable returns how many bytes are pending to send.
func (s *Socket) txAvailable() int {
	return int(s.txHead - s.txTail)
}

// txFree returns how many bytes can be written to the tx buffer.
func (s *Socket) txFree() int {
	return TxBufSize - int(s.txHead-s.txTail)
}

// txWrite writes data into the tx ring buffer using batch copy.
func (s *Socket) txWrite(data []byte) int {
	free := s.txFree()
	n := len(data)
	if n > free {
		n = free
	}
	if n == 0 {
		return 0
	}

	head := s.txHead & txMask
	first := int(TxBufSize - head)
	if first > n {
		first = n
	}
	copy(s.txBuf[head:], data[:first])
	if first < n {
		copy(s.txBuf[:], data[first:n])
	}
	s.txHead += uint16(n)
	return n
}

// txPeek reads data from the tx buffer without advancing the tail.
// Uses batch copy for efficiency.
func (s *Socket) txPeek(buf []byte) int {
	avail := s.txAvailable()
	n := len(buf)
	if n > avail {
		n = avail
	}
	if n == 0 {
		return 0
	}

	tail := s.txTail & txMask
	first := int(TxBufSize - tail)
	if first > n {
		first = n
	}
	copy(buf[:first], s.txBuf[tail:])
	if first < n {
		copy(buf[first:n], s.txBuf[:])
	}
	return n
}

// txAdvance advances the tail pointer by n bytes (after ACK).
func (s *Socket) txAdvance(n int) {
	s.txTail += uint16(n)
}

// reset clears the socket to free state.
func (s *Socket) reset() {
	s.state = sockFree
	s.protocol = 0
	s.localPort = 0
	s.remotePort = 0
	s.localAddr = netip.Addr{}
	s.remoteAddr = netip.Addr{}
	s.rxHead = 0
	s.rxTail = 0
	s.txHead = 0
	s.txTail = 0
	s.tcp = tcpState{}
	s.pendingConn = -1
}

// allocSocket finds and allocates a free socket, returns its index.
func (st *Stack) allocSocket() (int, error) {
	for i := range st.sockets {
		if st.sockets[i].state == sockFree {
			st.sockets[i].reset()
			return i, nil
		}
	}
	return -1, errNoSocket
}

// getSocket returns the socket for the given fd, or error.
func (st *Stack) getSocket(fd int) (*Socket, error) {
	if fd < 0 || fd >= MaxSockets {
		return nil, errBadSocket
	}
	s := &st.sockets[fd]
	if s.state == sockFree {
		return nil, errBadSocket
	}
	return s, nil
}

// nextEphemeralPort returns the next available ephemeral port.
func (st *Stack) nextEphemeralPort() uint16 {
	for port := uint16(49152); port < 65535; port++ {
		used := false
		for i := range st.sockets {
			if st.sockets[i].state != sockFree && st.sockets[i].localPort == port {
				used = true
				break
			}
		}
		if !used {
			return port
		}
	}
	return 49152
}
