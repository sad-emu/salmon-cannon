package main

import (
	"fmt"
	"log"
	"net"
	"time"
)

// Helper function to read exact number of bytes
func readExact(conn net.Conn, buf []byte, n int) (int, error) {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 0, err
	}

	total := 0
	for total < n {
		read, err := conn.Read(buf[total:n])
		if err != nil {
			return total, err
		}
		total += read
	}
	return total, nil
}

func handleUserPassAuth(conn net.Conn) error {
	// Accept USER/PASS authentication
	if _, err := conn.Write(handshakeUserPass); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}

	// Read version
	verBuf := make([]byte, 1)
	if _, err := readExact(conn, verBuf, 1); err != nil {
		return fmt.Errorf("read auth version: %w", err)
	}
	if verBuf[0] != 0x01 {
		conn.Write([]byte{0x01, 0xFF}) // version 1, failure
		return fmt.Errorf("unsupported USER/PASS auth version: %d", verBuf[0])
	}

	// Read username
	ulenBuf := make([]byte, 1)
	if _, err := readExact(conn, ulenBuf, 1); err != nil {
		return fmt.Errorf("read username length: %w", err)
	}
	ulen := int(ulenBuf[0])
	usernameBuf := make([]byte, ulen)
	if _, err := readExact(conn, usernameBuf, ulen); err != nil {
		return fmt.Errorf("read username: %w", err)
	}

	// Read password
	plenBuf := make([]byte, 1)
	if _, err := readExact(conn, plenBuf, 1); err != nil {
		return fmt.Errorf("read password length: %w", err)
	}
	plen := int(plenBuf[0])
	passwordBuf := make([]byte, plen)
	if _, err := readExact(conn, passwordBuf, plen); err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	log.Printf("NEAR: Received auth - Username: %s, Password: %s", string(usernameBuf), string(passwordBuf))

	// TODO handle username/password verification here
	if _, err := conn.Write(authReplySuccess); err != nil {
		return fmt.Errorf("write auth success: %w", err)
	}
	return nil
}

func HandleSocksHandshake(conn net.Conn, bridgeName string) (string, int, error) {
	// 1. Read greeting header (version + num methods)
	headerBuf := make([]byte, 2)
	read, err := readExact(conn, headerBuf, 2)
	if err != nil {
		// Don't wrap EOF errors - they just mean client disconnected before sending data
		// This is common with health checks, port scanners, or cancelled connections
		return "", 0, err
	}
	if read != 2 {
		return "", 0, fmt.Errorf("incomplete SOCKS greeting header")
	}

	if headerBuf[0] != socksVersion5 {
		log.Printf("NEAR: Bridge %s recieved unsupported SOCKS version: %d", bridgeName, headerBuf[0])
		return "", 0, fmt.Errorf("unsupported SOCKS version: %d", headerBuf[0])
	}

	// Read the methods
	numMethods := int(headerBuf[1])
	// log.Printf("NEAR: Bridge %s SOCKS number of auth methods: %d", bridgeName, numMethods)
	methodsBuf := make([]byte, numMethods)
	if numMethods > 0 {
		read, err = readExact(conn, methodsBuf, numMethods)
		if err != nil {
			return "", 0, fmt.Errorf("read auth methods: %w", err)
		}
		if read != numMethods {
			return "", 0, fmt.Errorf("incomplete SOCKS methods")
		}
	}

	// log.Printf("NEAR: Bridge %s SOCKS auth methods: %v", bridgeName, methodsBuf)

	foundNoAuth := false
	foundUserPass := false
	for i := 0; i < numMethods; i++ {
		if int(methodsBuf[i]) == socksAuthNoAuth {
			foundNoAuth = true
		}
		if int(methodsBuf[i]) == socksAuthUserPass {
			foundUserPass = true
		}
	}

	if foundNoAuth {
		if _, err := conn.Write(handshakeNoAuth); err != nil {
			return "", 0, fmt.Errorf("write no auth response: %w", err)
		}
	} else if foundUserPass {
		err = handleUserPassAuth(conn)
		if err != nil {
			return "", 0, fmt.Errorf("user/pass auth failed: %w", err)
		}
	} else {
		conn.Write(handshakeNoAcceptable)
		return "", 0, fmt.Errorf("no acceptable SOCKS authentication methods")
	}

	// 3. Read request header (version + cmd + reserved + addr type)
	requestHeader := make([]byte, 4)
	read, err = readExact(conn, requestHeader, 4)
	if err != nil {
		return "", 0, fmt.Errorf("read request header: %w", err)
	}
	if read != 4 {
		return "", 0, fmt.Errorf("incomplete SOCKS request header")
	}

	if requestHeader[0] != socksVersion5 {
		return "", 0, fmt.Errorf("unsupported SOCKS version: %d", requestHeader[0])
	}

	var host string
	var port int

	switch requestHeader[1] {
	case socksCmdConnect:
		switch requestHeader[3] {
		case socksAddrTypeIPv4:
			addrBuf := make([]byte, ipv4Len+portLen)
			if _, err := readExact(conn, addrBuf, ipv4Len+portLen); err != nil {
				return "", 0, fmt.Errorf("read IPv4 address: %w", err)
			}
			host = net.IP(addrBuf[:ipv4Len]).String()
			port = int(addrBuf[ipv4Len])<<8 | int(addrBuf[ipv4Len+1])

		case socksAddrTypeDomain:
			dlenBuf := make([]byte, 1)
			if _, err := readExact(conn, dlenBuf, 1); err != nil {
				return "", 0, fmt.Errorf("read domain length: %w", err)
			}
			dlen := int(dlenBuf[0])

			domainPortBuf := make([]byte, dlen+portLen)
			if _, err := readExact(conn, domainPortBuf, dlen+portLen); err != nil {
				return "", 0, fmt.Errorf("read domain and port: %w", err)
			}
			host = string(domainPortBuf[:dlen])
			port = int(domainPortBuf[dlen])<<8 | int(domainPortBuf[dlen+1])

		case socksAddrTypeIPv6:
			addrBuf := make([]byte, ipv6Len+portLen)
			if _, err := readExact(conn, addrBuf, ipv6Len+portLen); err != nil {
				return "", 0, fmt.Errorf("read IPv6 address: %w", err)
			}
			host = net.IP(addrBuf[:ipv6Len]).String()
			port = int(addrBuf[ipv6Len])<<8 | int(addrBuf[ipv6Len+1])

		default:
			return "", 0, fmt.Errorf("unsupported address type: %d", requestHeader[3])
		}
	default:
		return "", 0, fmt.Errorf("unsupported command: %d", requestHeader[1])
	}

	return host, port, nil
}
