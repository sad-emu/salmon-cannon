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
	// 1. Read greeting
	buf := make([]byte, maxMethods+2)
	readb, err := conn.Read(buf)
	if err != nil || readb < handshakeMinLen {
		return
	}
	if buf[0] != socksVersion5 {
		log.Printf("NEAR: Bridge %s recieved unsupported SOCKS version: %d", "SocksRedirectBridge", buf[0])
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
