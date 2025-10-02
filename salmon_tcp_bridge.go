package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
)

// SerializeSalmonTCPPacket serializes a SalmonTCPPacket to bytes.
func SerializeSalmonTCPPacket(pkt SalmonTCPPacket) ([]byte, error) {
	addrBytes := []byte(pkt.RemoteAddr)
	if len(addrBytes) > 255 {
		return nil, fmt.Errorf("address too long")
	}
	dataLen := len(pkt.Data)
	if dataLen > 65535 {
		return nil, fmt.Errorf("data too long")
	}
	out := make([]byte, 1+len(addrBytes)+2+2+dataLen)
	out[0] = uint8(len(addrBytes))
	copy(out[1:], addrBytes)
	off := 1 + len(addrBytes)
	binary.BigEndian.PutUint16(out[off:], uint16(pkt.remotePort))
	off += 2
	binary.BigEndian.PutUint16(out[off:], uint16(dataLen))
	off += 2
	copy(out[off:], pkt.Data)
	return out, nil
}

// DeserializeSalmonTCPPacket parses bytes into a SalmonTCPPacket.
func DeserializeSalmonTCPPacket(data []byte) (SalmonTCPPacket, error) {
	if len(data) < 1 {
		return SalmonTCPPacket{}, fmt.Errorf("data too short")
	}
	addrLen := int(data[0])
	if len(data) < 1+addrLen+2+2 {
		return SalmonTCPPacket{}, fmt.Errorf("data too short for address/port/len")
	}
	addr := string(data[1 : 1+addrLen])
	off := 1 + addrLen
	port := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	dlen := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	if len(data) < off+dlen {
		return SalmonTCPPacket{}, fmt.Errorf("data too short for payload")
	}
	payload := make([]byte, dlen)
	copy(payload, data[off:off+dlen])
	return SalmonTCPPacket{
		RemoteAddr: addr,
		remotePort: port,
		Data:       payload,
	}, nil
}

type SalmonTCPPacket struct {
	RemoteAddr string
	remotePort int
	Data       []byte
}

type tcpRequest struct {
	sPacket  SalmonTCPPacket
	respChan chan []byte
	errChan  chan error
}

type SalmonTcpBridge struct {
	conn      net.Conn
	queue     chan *tcpRequest
	queueOnce sync.Once
	queueWg   sync.WaitGroup
	mu        sync.Mutex
}

// Connect establishes a TCP connection to another remote instance.
func (b *SalmonTcpBridge) Connect(remoteAddr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		return errors.New("already connected")
	}
	conn, err := net.Dial("tcp", remoteAddr)
	if err != nil {
		return err
	}
	b.conn = conn
	b.queue = make(chan *tcpRequest, 32)
	b.queueWg.Add(1)
	go b.sender()
	return nil
}

// ForwardTCP queues data and waits for the TCP response before returning.
func (b *SalmonTcpBridge) ForwardTCP(ctx context.Context, data SalmonTCPPacket) ([]byte, error) {
	b.queueOnce.Do(func() {
		if b.queue == nil {
			b.queue = make(chan *tcpRequest, 32)
		}
	})
	req := &tcpRequest{
		sPacket:  data,
		respChan: make(chan []byte, 1),
		errChan:  make(chan error, 1),
	}
	select {
	case b.queue <- req:
		// ok
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case resp := <-req.respChan:
		return resp, nil
	case err := <-req.errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *SalmonTcpBridge) sender() {
	defer b.queueWg.Done()
	for req := range b.queue {
		b.mu.Lock()
		conn := b.conn
		b.mu.Unlock()
		if conn == nil {
			req.errChan <- errors.New("no connection")
			continue
		}

		// Serialize and send the packet
		dataOut, err := SerializeSalmonTCPPacket(req.sPacket)
		if err != nil {
			req.errChan <- err
			continue
		}

		_, err = conn.Write(dataOut)
		if err != nil {
			req.errChan <- err
			continue
		}
		// Read response (simple protocol: read up to 4096 bytes)
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			req.errChan <- err
			continue
		}
		req.respChan <- buf[:n]
	}
}

// Close closes the TCP connection and queue.
func (b *SalmonTcpBridge) Close() error {
	b.mu.Lock()
	if b.conn != nil {
		b.conn.Close()
		b.conn = nil
	}
	if b.queue != nil {
		close(b.queue)
		b.queue = nil
	}
	b.mu.Unlock()
	b.queueWg.Wait()
	return nil
}

// Listen accepts incoming connections and handles requests from remote bridges.
func (b *SalmonTcpBridge) Listen(listener net.Listener, handler func([]byte) ([]byte, error)) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go b.handleIncoming(conn, handler)
	}
}

// handleIncoming reads a request, processes it, and writes the response.
func (b *SalmonTcpBridge) handleIncoming(conn net.Conn, handler func([]byte) ([]byte, error)) {
	defer conn.Close()
	for {
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		req := buf[:n]
		pkt, err := DeserializeSalmonTCPPacket(req)
		if err != nil {
			conn.Write([]byte("error: " + err.Error()))
			return
		}
		// Make the TCP connection using the info provided
		remoteAddr := net.JoinHostPort(pkt.RemoteAddr, fmt.Sprintf("%d", pkt.remotePort))
		remoteConn, err := net.Dial("tcp", remoteAddr)
		if err != nil {
			conn.Write([]byte("error: " + err.Error()))
			return
		}
		_, err = remoteConn.Write(pkt.Data)
		if err != nil {
			remoteConn.Close()
			conn.Write([]byte("error: " + err.Error()))
			return
		}
		// Read response from remote
		respBuf := make([]byte, 4096)
		respN, err := remoteConn.Read(respBuf)
		remoteConn.Close()
		if err != nil && respN == 0 {
			conn.Write([]byte("error: " + err.Error()))
			return
		}
		respPkt := SalmonTCPPacket{
			RemoteAddr: pkt.RemoteAddr,
			remotePort: pkt.remotePort,
			Data:       respBuf[:respN],
		}
		respBytes, err := SerializeSalmonTCPPacket(respPkt)
		if err != nil {
			conn.Write([]byte("error: " + err.Error()))
			return
		}
		_, err = conn.Write(respBytes)
		if err != nil {
			return
		}
	}
}
