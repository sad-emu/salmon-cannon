package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	msgOpen  = 1
	msgData  = 2
	msgClose = 3
	msgError = 4
)

// Wire format:
// [addrLen:1][addr:addrLen][port:2][sessionID:4][msgType:1][dataLen:2][data:dataLen]

type SalmonTCPPacket struct {
	RemoteAddr string
	RemotePort int
	SessionID  uint32
	MsgType    byte
	Data       []byte
}

// --- serialization helpers ---

func SerializeSalmonTCPPacket(pkt SalmonTCPPacket) ([]byte, error) {
	addrBytes := []byte(pkt.RemoteAddr)
	if len(addrBytes) > 255 {
		return nil, fmt.Errorf("address too long")
	}
	dataLen := len(pkt.Data)
	if dataLen > 65535 {
		return nil, fmt.Errorf("data too long")
	}
	out := make([]byte, 1+len(addrBytes)+2+4+1+2+dataLen)
	out[0] = uint8(len(addrBytes))
	copy(out[1:], addrBytes)
	off := 1 + len(addrBytes)
	binary.BigEndian.PutUint16(out[off:], uint16(pkt.RemotePort))
	off += 2
	binary.BigEndian.PutUint32(out[off:], uint32(pkt.SessionID))
	off += 4
	out[off] = pkt.MsgType
	off += 1
	binary.BigEndian.PutUint16(out[off:], uint16(dataLen))
	off += 2
	copy(out[off:], pkt.Data)
	return out, nil
}

func DeserializeSalmonTCPPacket(data []byte) (SalmonTCPPacket, error) {
	if len(data) < 1 {
		return SalmonTCPPacket{}, fmt.Errorf("data too short")
	}
	addrLen := int(data[0])
	minLen := 1 + addrLen + 2 + 4 + 1 + 2
	if len(data) < minLen {
		return SalmonTCPPacket{}, fmt.Errorf("data too short for header")
	}
	addr := string(data[1 : 1+addrLen])
	off := 1 + addrLen
	port := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	sessionID := binary.BigEndian.Uint32(data[off : off+4])
	off += 4
	msgType := data[off]
	off += 1
	dlen := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	if len(data) < off+dlen {
		return SalmonTCPPacket{}, fmt.Errorf("data too short for payload")
	}
	payload := make([]byte, dlen)
	copy(payload, data[off:off+dlen])
	return SalmonTCPPacket{
		RemoteAddr: addr,
		RemotePort: port,
		SessionID:  sessionID,
		MsgType:    msgType,
		Data:       payload,
	}, nil
}

// --- stream API returned to caller ---

type SalmonTCPStream struct {
	sessionID uint32
	sendCh    chan []byte // client -> far (enqueued)
	recvCh    chan []byte // far -> client
	closeOnce sync.Once
	closed    chan struct{}
	bridge    *SalmonTCPBridge
}

func (s *SalmonTCPStream) Send(ctx context.Context, b []byte) error {
	select {
	case <-s.closed:
		return errors.New("stream closed")
	default:
	}
	// copy chunk so callers can reuse buffer
	chunk := append([]byte(nil), b...)
	select {
	case s.sendCh <- chunk:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return errors.New("stream closed")
	}
}

func (s *SalmonTCPStream) Recv(ctx context.Context) ([]byte, error) {
	select {
	case data, ok := <-s.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s *SalmonTCPStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		// signal bridge to send a CLOSE packet for this session
		s.bridge.enqueueControl(s.sessionID, msgClose, nil, s.bridge.defaultTimeout())
		// remove session map entry in bridge.reader when it sees close or via cleanup
	})
	return nil
}

// --- bridge implementation ---

type sessionEntry struct {
	host    string
	port    int
	SendCh  chan []byte // from stream.Send -> to sender() which writes over conn
	RecvCh  chan []byte // filled by reader()
	closing chan struct{}
}

type SalmonTCPBridge struct {
	conn      net.Conn
	connMu    sync.Mutex
	queue     chan SalmonTCPPacket // all outgoing packets serialized to conn by sender()
	queueOnce sync.Once
	readerWg  sync.WaitGroup
	sessions  map[uint32]*sessionEntry
	sessMu    sync.Mutex
	nextSess  uint32
	closed    chan struct{}
}

// defaultTimeout returns a context timeout duration for control ops.
func (b *SalmonTCPBridge) defaultTimeout() time.Duration {
	return 10 * time.Second
}

func (b *SalmonTCPBridge) ensureQueue() {
	b.queueOnce.Do(func() {
		if b.queue == nil {
			b.queue = make(chan SalmonTCPPacket, 128)
		}
	})
}

// Connect establishes a TCP connection to remote bridge and starts sender/reader.
func (b *SalmonTCPBridge) Connect(remoteAddr string) error {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn != nil {
		return errors.New("already connected")
	}
	conn, err := net.Dial("tcp", remoteAddr)
	if err != nil {
		return err
	}
	b.conn = conn
	b.ensureQueue()
	b.sessions = make(map[uint32]*sessionEntry)
	b.closed = make(chan struct{})
	go b.sender()
	b.readerWg.Add(1)
	go b.reader()
	return nil
}

// OpenStream opens a session, sends OPEN packet and returns a stream.
func (b *SalmonTCPBridge) OpenStream(ctx context.Context, host string, port int) (*SalmonTCPStream, error) {
	// allocate session id
	sid := atomic.AddUint32(&b.nextSess, 1)
	sendCh := make(chan []byte, 32)
	recvCh := make(chan []byte, 32)
	se := &sessionEntry{
		host:    host,
		port:    port,
		SendCh:  sendCh,
		RecvCh:  recvCh,
		closing: make(chan struct{}),
	}
	b.sessMu.Lock()
	b.sessions[sid] = se
	b.sessMu.Unlock()

	// enqueue OPEN with no data
	openPkt := SalmonTCPPacket{
		RemoteAddr: host,
		RemotePort: port,
		SessionID:  sid,
		MsgType:    msgOpen,
		Data:       nil,
	}
	select {
	case b.queue <- openPkt:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// return stream
	stream := &SalmonTCPStream{
		sessionID: sid,
		sendCh:    sendCh,
		recvCh:    recvCh,
		closed:    make(chan struct{}),
		bridge:    b,
	}

	// pump sendCh -> b.queue as Data packets (this preserves chunking & throttling at queue)
	go func() {
		for {
			select {
			case data, ok := <-sendCh:
				if !ok {
					return
				}
				pkt := SalmonTCPPacket{
					RemoteAddr: host,
					RemotePort: port,
					SessionID:  sid,
					MsgType:    msgData,
					Data:       data,
				}
				// block until enqueued or closed
				select {
				case b.queue <- pkt:
				case <-b.closed:
					return
				}
			case <-stream.closed:
				// ensure we send a Close packet
				b.enqueueControl(sid, msgClose, nil, b.defaultTimeout())
				return
			case <-b.closed:
				return
			}
		}
	}()

	return stream, nil
}

// enqueueControl sends control msg (Open/Close/Error) with timeout
func (b *SalmonTCPBridge) enqueueControl(sessionID uint32, typ byte, data []byte, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	pkt := SalmonTCPPacket{
		RemoteAddr: "",
		RemotePort: 0,
		SessionID:  sessionID,
		MsgType:    typ,
		Data:       data,
	}
	select {
	case b.queue <- pkt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closed:
		return errors.New("bridge closed")
	}
}

// sender serializes packets from b.queue to conn
func (b *SalmonTCPBridge) sender() {
	for pkt := range b.queue {
		b.connMu.Lock()
		conn := b.conn
		b.connMu.Unlock()
		if conn == nil {
			// drop packet or handle error - here we simply continue
			continue
		}
		out, err := SerializeSalmonTCPPacket(pkt)
		if err != nil {
			// Optionally handle serialization error per-session (send error back)
			continue
		}
		// Best-effort write with timeout
		conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		_, err = conn.Write(out)
		if err != nil {
			// if write fails, we could notify sessions; for now, close bridge
			b.Close()
			return
		}
	}
}

// reader reads incoming packets and dispatches to sessions
func (b *SalmonTCPBridge) reader() {
	defer b.readerWg.Done()
	buf := make([]byte, 65536)
	for {
		b.connMu.Lock()
		conn := b.conn
		b.connMu.Unlock()
		if conn == nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// continue reading
				continue
			}
			// fatal read error -> close bridge and notify sessions
			b.Close()
			return
		}
		if n == 0 {
			// remote closed
			b.Close()
			return
		}
		pkt, err := DeserializeSalmonTCPPacket(buf[:n])
		if err != nil {
			// ignore malformed
			continue
		}
		// dispatch
		b.sessMu.Lock()
		se, ok := b.sessions[pkt.SessionID]
		b.sessMu.Unlock()
		if !ok {
			// no such session; ignore or send error
			continue
		}
		switch pkt.MsgType {
		case msgData:
			// deliver
			select {
			case se.RecvCh <- pkt.Data:
			default:
				// if recvCh blocked, drop or backpressure; here we drop oldest (non-blocking)
			}
		case msgClose:
			// remote closed session
			close(se.closing)
			// close recvCh so Recv returns EOF
			close(se.RecvCh)
			// cleanup session entry
			b.sessMu.Lock()
			delete(b.sessions, pkt.SessionID)
			b.sessMu.Unlock()
		case msgError:
			// treat like close with potential error message
			close(se.closing)
			close(se.RecvCh)
			b.sessMu.Lock()
			delete(b.sessions, pkt.SessionID)
			b.sessMu.Unlock()
		case msgOpen:
			// unexpected: remote opened a session toward us; usually handled on Listen side
		}
	}
}

// Close tears down bridge and sessions
func (b *SalmonTCPBridge) Close() error {
	b.connMu.Lock()
	if b.conn != nil {
		b.conn.Close()
		b.conn = nil
	}
	b.connMu.Unlock()

	// close queue to stop sender
	if b.queue != nil {
		close(b.queue)
		b.queue = nil
	}
	// mark closed
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}

	// cleanup sessions
	b.sessMu.Lock()
	for sid, se := range b.sessions {
		close(se.closing)
		close(se.RecvCh)
		close(se.SendCh)
		delete(b.sessions, sid)
	}
	b.sessMu.Unlock()
	// wait for reader goroutine
	b.readerWg.Wait()
	return nil
}

// --- Listen side (far) ---
// This replaces your handleIncoming; it supports OPEN/DATA/CLOSE messages and maps them to real remote connections.

func (b *SalmonTCPBridge) Listen(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go b.handleIncoming(conn)
	}
}

func (b *SalmonTCPBridge) handleIncoming(conn net.Conn) {
	defer conn.Close()
	// local map of sessionID -> remoteConn + control
	type remoteSession struct {
		remote net.Conn
		recvCh chan []byte // from remote -> write back to bridge as DATA
		close  chan struct{}
	}
	sessions := make(map[uint32]*remoteSession)
	buf := make([]byte, 65536)

	// reader from bridge connection
	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// keep waiting
				continue
			}
			// connection closed
			for _, rs := range sessions {
				if rs.remote != nil {
					rs.remote.Close()
				}
				close(rs.close)
			}
			return
		}
		pkt, err := DeserializeSalmonTCPPacket(buf[:n])
		if err != nil {
			// write an error packet back if desired
			continue
		}
		switch pkt.MsgType {
		case msgOpen:
			// dial the requested remote target
			target := net.JoinHostPort(pkt.RemoteAddr, fmt.Sprintf("%d", pkt.RemotePort))
			remoteConn, err := net.Dial("tcp", target)
			if err != nil {
				// send error packet back
				errPkt := SalmonTCPPacket{
					RemoteAddr: pkt.RemoteAddr,
					RemotePort: pkt.RemotePort,
					SessionID:  pkt.SessionID,
					MsgType:    msgError,
					Data:       []byte(err.Error()),
				}
				out, _ := SerializeSalmonTCPPacket(errPkt)
				conn.Write(out)
				continue
			}
			rs := &remoteSession{
				remote: remoteConn,
				recvCh: make(chan []byte, 32),
				close:  make(chan struct{}),
			}
			sessions[pkt.SessionID] = rs

			// start goroutine to copy remote->bridge as DATA packets
			go func(sid uint32, rconn net.Conn) {
				rbuf := make([]byte, 65536)
				for {
					nr, err := rconn.Read(rbuf)
					if nr > 0 {
						outPkt := SalmonTCPPacket{
							RemoteAddr: pkt.RemoteAddr,
							RemotePort: pkt.RemotePort,
							SessionID:  sid,
							MsgType:    msgData,
							Data:       append([]byte(nil), rbuf[:nr]...),
						}
						bts, serr := SerializeSalmonTCPPacket(outPkt)
						if serr == nil {
							conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
							_, _ = conn.Write(bts)
						}
					}
					if err != nil {
						// send close or error
						closePkt := SalmonTCPPacket{
							RemoteAddr: pkt.RemoteAddr,
							RemotePort: pkt.RemotePort,
							SessionID:  sid,
							MsgType:    msgClose,
							Data:       nil,
						}
						bts, _ := SerializeSalmonTCPPacket(closePkt)
						conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
						_, _ = conn.Write(bts)
						rconn.Close()
						return
					}
				}
			}(pkt.SessionID, remoteConn)
		case msgData:
			// forward data to remoteConn
			rs, ok := sessions[pkt.SessionID]
			if !ok {
				// unknown session, drop or reply error
				continue
			}
			if rs.remote != nil {
				rs.remote.SetWriteDeadline(time.Now().Add(15 * time.Second))
				_, err := rs.remote.Write(pkt.Data)
				if err != nil {
					// send error or close
					closePkt := SalmonTCPPacket{
						RemoteAddr: pkt.RemoteAddr,
						RemotePort: pkt.RemotePort,
						SessionID:  pkt.SessionID,
						MsgType:    msgClose,
						Data:       nil,
					}
					bts, _ := SerializeSalmonTCPPacket(closePkt)
					conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
					_, _ = conn.Write(bts)
					rs.remote.Close()
					delete(sessions, pkt.SessionID)
				}
			}
		case msgClose:
			// close remote connection and cleanup
			rs, ok := sessions[pkt.SessionID]
			if ok {
				if rs.remote != nil {
					rs.remote.Close()
				}
				delete(sessions, pkt.SessionID)
			}
		case msgError:
			// ignore or log
		}
	}
}
