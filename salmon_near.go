package main

import (
	"crypto/tls"
	"io"
	"log"
	"net"
	"salmoncannon/bridge"
	"salmoncannon/config"
	"strconv"
	"sync"

	"slices"

	quic "github.com/quic-go/quic-go"
)

func initNear(cfg *config.SalmonBridgeConfig, near *SalmonNear) {
	log.Printf("NEAR: Initializing near side SOCKS listener for bridge %s", cfg.Name)
	listenAddr := cfg.SocksListenAddress + ":" + strconv.Itoa(cfg.SocksListenPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("NEAR: Failed to listen on %s: %v", listenAddr, err)
	}
	log.Printf("NEAR: SOCKS proxy listening on %s", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("NEAR: Local SOCKS TCP accept error: %v", err)
			continue
		}
		go near.HandleRequest(conn)
	}
}

func initHTTPNear(cfg *config.SalmonBridgeConfig, near *SalmonNear) {
	if cfg.HttpListenPort <= 0 {
		return
	}
	addr := cfg.SocksListenAddress + ":" + strconv.Itoa(cfg.HttpListenPort)
	log.Printf("NEAR: Initializing HTTP proxy listener for bridge %s on %s", cfg.Name, addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("NEAR: Failed to listen HTTP on %s: %v", addr, err)
	}
	log.Printf("NEAR: HTTP proxy listening on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("NEAR: HTTP accept error: %v", err)
			continue
		}
		go near.HandleHTTP(conn)
	}
}

func relayConnData(src net.Conn, dst net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	// Signal channel to coordinate shutdown
	srcDone := make(chan struct{})
	dstDone := make(chan struct{})

	// Copy src -> dst
	go func() {
		defer wg.Done()
		io.Copy(dst, src)
		close(srcDone)
		// Try to signal the other direction by closing write side if supported
		if conn, ok := dst.(interface{ CloseWrite() error }); ok {
			conn.CloseWrite()
		}
	}()

	// Copy dst -> src
	go func() {
		defer wg.Done()
		io.Copy(src, dst)
		close(dstDone)
		// Try to signal the other direction by closing write side if supported
		if conn, ok := src.(interface{ CloseWrite() error }); ok {
			conn.CloseWrite()
		}
	}()

	// Wait for BOTH directions to complete
	wg.Wait()

	// Close both connections
	src.Close()
	dst.Close()
}

type SalmonNear struct {
	currentBridge *bridge.SalmonBridge
	bridgeName    string
	config        *config.SalmonBridgeConfig
}

func NewSalmonNear(config *config.SalmonBridgeConfig) (*SalmonNear, error) {
	bridgeAddress := config.FarIp
	bridgePort := config.FarPort

	qcfg := &quic.Config{
		MaxIdleTimeout:                 config.IdleTimeout.Duration(),
		InitialStreamReceiveWindow:     uint64(1024 * 1024 * 21),
		MaxStreamReceiveWindow:         uint64(config.MaxRecieveBufferSize),
		InitialConnectionReceiveWindow: uint64(1024 * 1024 * 7),
		MaxConnectionReceiveWindow:     uint64(config.MaxRecieveBufferSize / 2),
		InitialPacketSize:              uint16(config.InitialPacketSize),
		MaxIncomingStreams:             maxConnections,
		MaxIncomingUniStreams:          maxConnections,
	}

	sl := bridge.NewSharedLimiter(int64(config.TotalBandwidthLimit))

	tlscfg := &tls.Config{
		InsecureSkipVerify: true, // for prototype
		NextProtos:         []string{config.Name},
	}

	salmonBridge := bridge.NewSalmonBridge(config.Name, bridgeAddress, bridgePort,
		tlscfg, qcfg, sl, config.Connect, config.InterfaceName, config.AllowedOutAddresses)

	near := &SalmonNear{
		currentBridge: salmonBridge,
		bridgeName:    config.Name,
		config:        config,
	}

	return near, nil
}

func (n *SalmonNear) shouldBlockNearConn(nearHostFull string) bool {
	if len(n.config.AllowedInAddresses) == 0 {
		return false
	}
	nearAddr, _, _ := net.SplitHostPort(nearHostFull)
	return !slices.Contains(n.config.AllowedInAddresses, nearAddr)
}

func (n *SalmonNear) HandleRequest(conn net.Conn) {
	globalConnMonitor.IncSOCKS()
	defer func() {
		conn.Close()
		globalConnMonitor.DecSOCKS()
	}()
	//log.Printf("NEAR: Bridge %s accepted connection from %s", n.bridgeName, conn.RemoteAddr())
	if n.shouldBlockNearConn(conn.RemoteAddr().String()) {
		log.Printf("NEAR: Bridge %s recieved request unallowed near IP: %s", n.bridgeName, conn.RemoteAddr())
		return
	}

	host, port, err := HandleSocksHandshake(conn, n.bridgeName)
	if err != nil {
		log.Printf("NEAR: Bridge %s Failed to handle SOCKS handshake: %v", n.bridgeName, err)
		return
	}

	// 4. Open a streaming session to far
	stream, err := n.currentBridge.NewNearConn(host, port)
	if err != nil {
		conn.Write(replyFail)
		log.Printf("NEAR: Bridge %s Failed to open stream to far: %v", n.bridgeName, err)
		return
	}
	defer func() {
		stream.Close()
		log.Printf("NEAR: Bridge %s closed stream to %s:%d", n.bridgeName, host, port)
	}()

	// 5. Reply: success
	conn.Write(replySuccess)

	relayConnData(conn, stream)
}

// HandleHTTP implements a minimal HTTP CONNECT proxy
func (n *SalmonNear) HandleHTTP(conn net.Conn) {
	globalConnMonitor.IncHTTP()
	defer func() {
		conn.Close()
		globalConnMonitor.DecHTTP()
	}()
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
	defer func() {
		stream.Close()
		//log.Printf("NEAR: Bridge %s closed HTTP stream to %s:%d", n.bridgeName, host, port)
	}()
	// respond OK
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	relayConnData(conn, stream)
}
