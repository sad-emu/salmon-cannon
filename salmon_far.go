package main

import (
	"fmt"
	"net"
)

type SalmonFar struct {
	port           int
	ln             net.Listener
	allowedBridges []BridgeType // acceptable bridges in order of preference
}

// TODO - for bridge types it should start listeners for them
// near should be able to make requests through them

func NewSalmonFar(port int, allowedBridges []BridgeType) (*SalmonFar, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	far := &SalmonFar{
		port:           port,
		ln:             ln,
		allowedBridges: allowedBridges,
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
		go f.handleMetadataConn(conn)
	}
}

func (f *SalmonFar) buildBridgeResponse() []byte {
	bridges := make([]byte, len(f.allowedBridges))
	for i, b := range f.allowedBridges {
		bridges[i] = byte(b)
	}
	pkt := make([]byte, 0, 2+len(bridges))
	pkt = append(pkt, HeaderResponseBridges)
	pkt = append(pkt, 0)
	pkt = append(pkt, bridges...)
	pkt[1] = byte(len(bridges))
	return pkt
}

// Function to handle metadata requests so SalmonNear understand available bridges
func (f *SalmonFar) handleMetadataConn(conn net.Conn) {
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

// parseMetaPacket parses a HeaderMeta packet and returns the url and port as strings, or an error.
func parseMetaPacket(msg []byte) (string, string, error) {
	idx := 1
	if len(msg) < idx+1 {
		return "", "", fmt.Errorf("missing urlLen")
	}
	urlLen := int(msg[idx])
	idx++
	if len(msg) < idx+urlLen {
		return "", "", fmt.Errorf("missing url bytes")
	}
	remoteAddr := string(msg[idx : idx+urlLen])
	idx += urlLen
	if len(msg) < idx+1 {
		return "", "", fmt.Errorf("missing portLen")
	}
	portLen := int(msg[idx])
	idx++
	if len(msg) < idx+portLen {
		return "", "", fmt.Errorf("missing port bytes")
	}
	remotePort := string(msg[idx : idx+portLen])
	return remoteAddr, remotePort, nil
}
