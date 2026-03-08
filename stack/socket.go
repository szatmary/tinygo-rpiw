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

	// Ring buffers for data
	rxBuf  [RxBufSize]byte
	rxHead uint16
	rxTail uint16

	txBuf  [TxBufSize]byte
	txHead uint16
	txTail uint16

	// TCP-specific state
	tcp tcpState

	// Deadline for blocking operations
	sendDeadline time.Time
	recvDeadline time.Time

	// For listening sockets: pending connection
	pendingConn int // socket fd of accepted connection, -1 if none
}

type tcpState struct {
	state      uint8
	seqNum     uint32 // next sequence number to send (sndNxt)
	ackNum     uint32
	sndUna     uint32 // oldest unACKed sequence number
	remoteWin  uint16
	localWin   uint16
	mss        uint16
	retxTime   time.Time
	retxCount  uint8
	finSent    bool
	finAcked   bool
	finRecvd   bool
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
	if s.rxHead >= s.rxTail {
		return int(s.rxHead - s.rxTail)
	}
	return int(RxBufSize - s.rxTail + s.rxHead)
}

// rxFree returns how many bytes can be written to the rx buffer.
func (s *Socket) rxFree() int {
	return RxBufSize - 1 - s.rxAvailable()
}

// rxWrite writes data into the rx ring buffer.
func (s *Socket) rxWrite(data []byte) int {
	n := 0
	for _, b := range data {
		next := (s.rxHead + 1) % RxBufSize
		if next == s.rxTail {
			break // full
		}
		s.rxBuf[s.rxHead] = b
		s.rxHead = next
		n++
	}
	return n
}

// rxRead reads data from the rx ring buffer.
func (s *Socket) rxRead(buf []byte) int {
	n := 0
	for n < len(buf) && s.rxTail != s.rxHead {
		buf[n] = s.rxBuf[s.rxTail]
		s.rxTail = (s.rxTail + 1) % RxBufSize
		n++
	}
	return n
}

// txAvailable returns how many bytes are pending to send.
func (s *Socket) txAvailable() int {
	if s.txHead >= s.txTail {
		return int(s.txHead - s.txTail)
	}
	return int(TxBufSize - s.txTail + s.txHead)
}

// txFree returns how many bytes can be written to the tx buffer.
func (s *Socket) txFree() int {
	return TxBufSize - 1 - s.txAvailable()
}

// txWrite writes data into the tx ring buffer.
func (s *Socket) txWrite(data []byte) int {
	n := 0
	for _, b := range data {
		next := (s.txHead + 1) % TxBufSize
		if next == s.txTail {
			break // full
		}
		s.txBuf[s.txHead] = b
		s.txHead = next
		n++
	}
	return n
}

// txRead reads data from the tx ring buffer (for sending).
func (s *Socket) txRead(buf []byte) int {
	n := 0
	for n < len(buf) && s.txTail != s.txHead {
		buf[n] = s.txBuf[s.txTail]
		s.txTail = (s.txTail + 1) % TxBufSize
		n++
	}
	return n
}

// txPeek reads data without advancing the tail pointer.
func (s *Socket) txPeek(buf []byte) int {
	n := 0
	tail := s.txTail
	for n < len(buf) && tail != s.txHead {
		buf[n] = s.txBuf[tail]
		tail = (tail + 1) % TxBufSize
		n++
	}
	return n
}

// txAdvance advances the tail pointer by n bytes (after ACK).
func (s *Socket) txAdvance(n int) {
	for i := 0; i < n && s.txTail != s.txHead; i++ {
		s.txTail = (s.txTail + 1) % TxBufSize
	}
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
	// Simple counter starting at 49152
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
