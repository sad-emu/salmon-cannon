package main

import (
	"io"
	"log"
	"net"
	"salmoncannon/config"
	"strconv"
	"strings"
)

func handleSocksRedirect(conn net.Conn, socksConfig *config.SocksRedirectConfig, bridgeRegistry *map[string]*SalmonNear) {
	defer conn.Close()

	dummyBridgeName := "SocksRedirectBridge"

	host, port, err := HandleSocksHandshake(conn, dummyBridgeName)
	if err != nil {
		log.Printf("NEAR: Bridge %s Failed to handle SOCKS handshake: %v", dummyBridgeName, err)
		return
	}

	// Check to see if we have a redirect for this destination
	var bridgeName string
	for addrPart, bName := range socksConfig.Redirects {
		if strings.Contains(host, addrPart) {
			bridgeName = bName
			break
		}
	}

	if bridgeName == "" || (*bridgeRegistry)[bridgeName] == nil {
		log.Printf("SOCKS Redirector: No redirect found for destination %s", host)
		conn.Write(replyFail)
		return
	}
	log.Printf("SOCKS Redirector: Redirecting %s:%d to bridge %s", host, port, bridgeName)

	// Do our block check here
	if (*bridgeRegistry)[bridgeName].shouldBlockNearConn(conn.RemoteAddr().String()) {
		log.Printf("NEAR: Bridge %s recieved request unallowed near IP: %s", (*bridgeRegistry)[bridgeName].bridgeName, conn.RemoteAddr())
		return
	}

	// 4. Open a streaming session to far
	stream, err := (*bridgeRegistry)[bridgeName].currentBridge.NewNearConn(host, port)

	if err != nil {
		conn.Write(replyFail)
		log.Printf("NEAR: Bridge %s Failed to open stream to far: %v", "SocksRedirectBridge", err)
		return
	}
	defer stream.Close()

	// 5. Reply: success
	conn.Write(replySuccess)

	// 6. Relay data
	go func() { io.Copy(stream, conn) }()
	io.Copy(conn, stream)
}
func runSocksRedirector(socksConfig *config.SocksRedirectConfig, bridgeRegistry *map[string]*SalmonNear) error {
	listenAddr := socksConfig.Hostname + ":" + strconv.Itoa(socksConfig.Port)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	log.Printf("SOCKS Redirector listening on %s", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("SOCKS Redirector: TCP accept error: %v", err)
			continue
		}
		go handleSocksRedirect(conn, socksConfig, bridgeRegistry)
	}
}
