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
		go f.handleConn(conn)
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

func (f *SalmonFar) handleConn(conn net.Conn) {
	defer conn.Close()

	var remoteAddr string
	var remotePort string

	buf := make([]byte, 64000)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	msg := buf[:n]
	if len(msg) == 1 && msg[0] == HeaderRequestBridges {
		conn.Write(f.buildBridgeResponse())
	} else if len(msg) > 1 && msg[0] == HeaderMeta {
		remoteAddr, remotePort, err := parseMetaPacket(msg)
		if err != nil {
			fmt.Println("HeaderMeta error:", err)
			return
		}
		fmt.Printf("Received meta: url=%s, port=%s\n", remoteAddr, remotePort)
	} else if len(msg) > 1 && msg[0] == HeaderData {
		// Connect to remoteAddr:remotePort and forward data
		if remoteAddr == "" || remotePort == "" {
			fmt.Fprintln(conn, "remoteAddr/remotePort not set via meta packet")
			return
		}
		target := net.JoinHostPort(remoteAddr, remotePort)
		dst, err := net.Dial("tcp", target)
		if err != nil {
			fmt.Fprintf(conn, "failed to connect to %s: %v\n", target, err)
			return
		}
		defer dst.Close()
		// Send the data (excluding the first byte)
		_, err = dst.Write(msg[1:])
		if err != nil {
			fmt.Fprintf(conn, "failed to write to remote: %v\n", err)
			return
		}
		// Read response from remote and send back to conn
		buf := make([]byte, 4096)
		n, err := dst.Read(buf)
		if err != nil {
			fmt.Fprintf(conn, "failed to read from remote: %v\n", err)
			return
		}
		conn.Write(buf[:n])
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
