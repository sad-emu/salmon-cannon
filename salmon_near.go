package main

import (
	"crypto/tls"
	"io"
	"log"
	"net"
	"salmoncannon/bridge"
	"salmoncannon/config"

	quic "github.com/quic-go/quic-go"
)

type SalmonNear struct {
	currentBridge *bridge.SalmonBridge
	bridgeName    string
}

func NewSalmonNear(config *config.SalmonBridgeConfig) (*SalmonNear, error) {
	bridgeAddress := config.FarIp
	bridgePort := config.FarPort

	qcfg := &quic.Config{
		MaxIdleTimeout:                 config.IdleTimeout.Duration(),
		InitialStreamReceiveWindow:     uint64(1024 * 1024 * 50 * 8),
		MaxStreamReceiveWindow:         uint64(1024 * 1024 * 700 * 8),
		InitialConnectionReceiveWindow: uint64(1024 * 1024 * 50 * 8),
		MaxConnectionReceiveWindow:     uint64(1024 * 1024 * 700 * 8),
		InitialPacketSize:              uint16(config.InitialPacketSize),
	}

	sl := bridge.NewSharedLimiter(int64(config.TotalBandwidthLimit))

	tlscfg := &tls.Config{
		InsecureSkipVerify: true, // for prototype
		NextProtos:         []string{config.Name},
	}

	salmonBridge := bridge.NewSalmonBridge(config.Name, bridgeAddress, bridgePort, tlscfg, qcfg, sl, config.Connect)

	near := &SalmonNear{
		currentBridge: salmonBridge,
		bridgeName:    config.Name,
	}

	return near, nil
}

// func NewSalmonNearFromFar(salmonFar *SalmonFar) *SalmonNear {

// 	salmonBridge := salmonFar.farBridge

// 	near := &SalmonNear{
// 		currentBridge: salmonBridge,
// 	}

// 	return near
// }

func (n *SalmonNear) HandleRequest(conn net.Conn) {
	defer conn.Close()

	// 1. Read greeting
	buf := make([]byte, maxMethods+2)
	readb, err := conn.Read(buf)
	if err != nil || readb < handshakeMinLen {
		return
	}
	if buf[0] != socksVersion5 {
		log.Printf("NEAR: Bridge %s recieved unsupported SOCKS version: %d", n.bridgeName, buf[0])
		return // Only SOCKS5
	}

	// 2. Send handshake response: no auth
	conn.Write(handshakeNoAuth)

	// 3. Read request
	readb, err = conn.Read(buf)
	if err != nil || readb < requestMinLen {
		return
	}
	if buf[0] != socksVersion5 {
		return
	}

	var host string
	var port int

	switch buf[1] {
	case socksCmdConnect:
		switch buf[3] {
		case socksAddrTypeIPv4:
			if readb < 4+ipv4Len+portLen {
				return
			}
			host = net.IP(buf[4 : 4+ipv4Len]).String()
			port = int(buf[4+ipv4Len])<<8 | int(buf[5+ipv4Len])

		case socksAddrTypeDomain:
			dlen := int(buf[4])
			if readb < 5+dlen+portLen {
				return
			}
			host = string(buf[5 : 5+dlen])
			port = int(buf[5+dlen])<<8 | int(buf[6+dlen])

		case socksAddrTypeIPv6:
			if readb < 4+ipv6Len+portLen {
				return
			}
			host = net.IP(buf[4 : 4+ipv6Len]).String()
			port = int(buf[4+ipv6Len])<<8 | int(buf[5+ipv6Len])

		default:
			return
		}
	}

	// This is really noisy
	// log.Printf("NEAR: New request on bridge %s to connect to %s:%d", n.bridgeName, host, port)

	// 4. Open a streaming session to far
	stream, err := n.currentBridge.NewNearConn(host, port)
	if err != nil {
		conn.Write(replyFail)
		log.Printf("NEAR: Bridge %s Failed to open stream to far: %v", n.bridgeName, err)
		return
	}
	defer stream.Close()

	// 5. Reply: success
	conn.Write(replySuccess)

	// 6. Relay data
	go func() { io.Copy(stream, conn) }()
	io.Copy(conn, stream)
}

// HandleHTTP implements a minimal HTTP CONNECT proxy
func (n *SalmonNear) HandleHTTP(conn net.Conn) {
	defer conn.Close()
	// Minimal parse: read first line
	buf := make([]byte, 4096)
	nread, err := conn.Read(buf)
	if err != nil {
		return
	}
	lineEnd := -1
	for i := 0; i < nread-1; i++ {
		if buf[i] == '\r' && buf[i+1] == '\n' {
			lineEnd = i
			break
		}
	}
	if lineEnd < 0 {
		return
	}
	line := string(buf[:lineEnd])
	// Expect: CONNECT host:port HTTP/1.1
	var method, target, proto string
	_, _ = method, proto
	// naive split
	parts := make([]string, 0, 3)
	start := 0
	for i := 0; i <= len(line); i++ {
		if i == len(line) || line[i] == ' ' {
			if i > start {
				parts = append(parts, line[start:i])
			}
			start = i + 1
		}
	}
	if len(parts) < 2 || parts[0] != "CONNECT" {
		conn.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\n\r\n"))
		return
	}
	target = parts[1]
	// parse host:port
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}
	// drain remaining headers until CRLFCRLF
	// simplistic: if more bytes were read beyond first line, keep them in a buffer to forward after connect
	// For CONNECT, there should be only headers and then raw tunnel.

	// Open QUIC stream to far
	// parse port
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}
	stream, err := n.currentBridge.NewNearConn(host, port)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer stream.Close()
	// respond OK
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	// pipe
	go func() { io.Copy(stream, conn) }()
	io.Copy(conn, stream)
}
