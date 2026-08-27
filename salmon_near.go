package main

import (
	"io"
	"log"
	"net"
	"salmoncannon/bridge"
	"salmoncannon/config"
	"salmoncannon/connections"
	"salmoncannon/limiter"
	"salmoncannon/socks"
	"salmoncannon/status"
	"strconv"
	"sync"
	"time"

	"slices"
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

func relayConnData(src net.Conn, dst net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	// Signal channel to coordinate shutdown
	done := make(chan struct{})

	// Copy src -> dst
	go func() {
		defer wg.Done()
		io.Copy(dst, src)
		// Signal other goroutine to stop by setting deadline
		dst.SetReadDeadline(time.Now())
		src.SetWriteDeadline(time.Now())
		// Try to signal the other direction by closing write side if supported
		if conn, ok := dst.(interface{ CloseWrite() error }); ok {
			conn.CloseWrite()
		}
	}()

	// Copy dst -> src
	go func() {
		defer wg.Done()
		io.Copy(src, dst)
		// Signal other goroutine to stop by setting deadline
		src.SetReadDeadline(time.Now())
		dst.SetWriteDeadline(time.Now())
		// Try to signal the other direction by closing write side if supported
		if conn, ok := src.(interface{ CloseWrite() error }); ok {
			conn.CloseWrite()
		}
	}()

	// Wait for BOTH directions to complete
	wg.Wait()
	close(done)

	// Close both connections
	src.Close()
	dst.Close()
}

type SalmonNear struct {
	currentBridge *bridge.SalmonBridge
	bridgeName    string
	config        *config.SalmonBridgeConfig
	httpProxy     *salmonHTTPProxy
}

func (n *SalmonNear) runStatusChecks(intervalMs int) {
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		<-ticker.C
		n.currentBridge.StatusCheck()
	}
}

func NewSalmonNear(config *config.SalmonBridgeConfig) (*SalmonNear, error) {
	bridgeAddress := config.FarIp
	bridgePort := config.FarPort

	netcfg := connections.BridgeNetConfig{
		IdleTimeout:               config.IdleTimeout.Duration(),
		InitialRetransmitTimeout:  config.InitialRetransmitTimeout.Duration(),
		MinRetransmitTimeout:      config.MinRetransmitTimeout.Duration(),
		StreamRecvBuffer:          int(config.MaxRecieveBufferSize),
		PacketSize:                config.InitialPacketSize,
		MaxStreams:                socks.MaxConnections,
		MaxBytesInFlight:          int(config.MaxBytesInFlight),
		FECGroupSize:              config.FECGroupSize,
		FEC2D:                     config.FEC2D,
		BandwidthLimit:            int(config.TotalBandwidthLimit),
		PacingDatagramOverhead:    config.PacingDatagramOverhead,
		PacingMinimumDatagramSize: config.PacingMinimumDatagramSize,
		PacingBurstBytes:          int(config.PacingBurstSize),
		TransportBatchSize:        config.TransportBatchSize,
	}

	sl := limiter.NewSharedLimiter(int64(config.TotalBandwidthLimit))
	status.GlobalConnMonitorRef.RegisterLimiter(config.Name, sl)

	salmonBridge := bridge.NewSalmonBridge(config.Name, bridgeAddress, bridgePort,
		netcfg, sl, config.Connect, config.InterfaceName, config.AllowedOutAddresses, config.SharedSecret)

	near := &SalmonNear{
		currentBridge: salmonBridge,
		bridgeName:    config.Name,
		config:        config,
	}
	near.httpProxy = newSalmonHTTPProxy(near, nil)

	if config.StatusCheckFrequency > 0 {
		log.Printf("NEAR: Bridge %s starting status checks every %d ms", near.bridgeName, config.StatusCheckFrequency.Duration().Milliseconds())
		go near.runStatusChecks(int(config.StatusCheckFrequency.Duration().Milliseconds()))
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
	status.GlobalConnMonitorRef.IncSOCKS()
	defer func() {
		conn.Close()
		status.GlobalConnMonitorRef.DecSOCKS()
	}()
	//log.Printf("NEAR: Bridge %s accepted connection from %s", n.bridgeName, conn.RemoteAddr())
	if n.shouldBlockNearConn(conn.RemoteAddr().String()) {
		log.Printf("NEAR: Bridge %s recieved request unallowed near IP: %s", n.bridgeName, conn.RemoteAddr())
		return
	}

	host, port, err := socks.HandleSocksHandshake(conn, n.bridgeName)
	if err != nil {
		// Only log non-EOF errors - EOF just means client disconnected (common with health checks)
		if err != io.EOF {
			log.Printf("NEAR: Bridge %s Failed to handle SOCKS handshake: %v", n.bridgeName, err)
		}
		return
	}

	// 4. Open a streaming session to far
	stream, err := n.currentBridge.NewNearConn(host, port)
	if err != nil {
		conn.Write(socks.ReplyFail)
		log.Printf("NEAR: Bridge %s Failed to open stream to far: %v", n.bridgeName, err)
		return
	}
	defer func() {
		stream.Close()
		log.Printf("NEAR: Bridge %s closed stream to %s:%d", n.bridgeName, host, port)
	}()

	// 5. Reply: success
	conn.Write(socks.ReplySuccess)

	relayConnData(conn, stream)
}
