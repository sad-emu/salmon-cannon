package main

import (
	"fmt"
	"net"
)

type SalmonFar struct {
	port int
	ln   net.Listener
}

func NewSalmonFar(port int) (*SalmonFar, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	far := &SalmonFar{
		port: port,
		ln:   ln,
	}
	go far.acceptLoop()
	return far, nil
}

func (f *SalmonFar) acceptLoop() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handleConn(conn)
	}
}

func (f *SalmonFar) buildBridgeResponse() []byte {
	bridges := []byte{byte(BridgeTCP), byte(BridgeQUIC)}
	pkt := make([]byte, 0, 2+len(bridges))
	pkt = append(pkt, HeaderRequestBridges)
	pkt = append(pkt, 0)
	pkt = append(pkt, bridges...)
	pkt[1] = byte(len(bridges))
	return pkt
}

func (f *SalmonFar) handleConn(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	msg := buf[:n]
	if len(msg) == 1 && msg[0] == HeaderRequestBridges {
		conn.Write(f.buildBridgeResponse())
	}
}
