package main

import (
	"net"
)

// handleConnection handles a single SOCKS client connection.
func handleConnection(conn net.Conn) {
	defer conn.Close()

	// 1. Read greeting
	buf := make([]byte, maxMethods+2)
	n, err := conn.Read(buf)
	if err != nil || n < handshakeMinLen {
		return
	}
	if buf[0] != socksVersion5 {
		return // Only SOCKS5
	}
	// 2. Send handshake response: no auth
	conn.Write(handshakeNoAuth)

	// 3. Read request
	n, err = conn.Read(buf)
	if err != nil || n < requestMinLen {
		return
	}
	if buf[0] != socksVersion5 {
		return // Only SOCKS5 supported
	}

	switch buf[1] {
	case socksCmdConnect:
		// 4. Parse address
		var host string
		switch buf[3] {
		case socksAddrTypeIPv4:
			if n < 4+ipv4Len+portLen {
				return
			}
			host = net.IP(buf[4 : 4+ipv4Len]).String()
			port := int(buf[4+ipv4Len])<<8 | int(buf[5+ipv4Len])
			host = net.JoinHostPort(host, itoa(port))
		case socksAddrTypeDomain:
			dlen := int(buf[4])
			if n < 5+dlen+portLen {
				return
			}
			host = string(buf[5 : 5+dlen])
			port := int(buf[5+dlen])<<8 | int(buf[6+dlen])
			host = net.JoinHostPort(host, itoa(port))
		case socksAddrTypeIPv6:
			if n < 4+ipv6Len+portLen {
				return
			}
			host = net.IP(buf[4 : 4+ipv6Len]).String()
			port := int(buf[4+ipv6Len])<<8 | int(buf[5+ipv6Len])
			host = net.JoinHostPort(host, itoa(port))
		default:
			return
		}

		// 5. Connect to target
		target, err := net.Dial("tcp", host)
		if err != nil {
			// Reply: general failure
			conn.Write(replyFail)
			return
		}
		defer target.Close()

		// Reply: success
		conn.Write(replySuccess)

		// 6. Relay data
		go func() { ioCopy(target, conn) }()
		ioCopy(conn, target)

	case socksCmdUDPAssociate:
		// UDP ASSOCIATE: bind a UDP socket and reply with its address
		udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		if err != nil {
			conn.Write(replyFail)
			return
		}
		udpConn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			conn.Write(replyFail)
			return
		}
		// Prepare reply with UDP bind address
		local := udpConn.LocalAddr().(*net.UDPAddr)
		ip := local.IP.To4()
		if ip == nil {
			ip = local.IP // fallback for IPv6
		}
		port := local.Port
		reply := []byte{socksVersion5, socksReplySucceeded, socksReserved, socksAddrTypeIPv4}
		reply = append(reply, ip...)
		reply = append(reply, byte(port>>8), byte(port))
		conn.Write(reply)

		// Start UDP relay goroutine
		go udpRelay(udpConn)

		// Keep TCP connection open until closed by client
		buf := make([]byte, 1)
		conn.Read(buf)
		udpConn.Close()
	default:
		// Only CONNECT and UDP ASSOCIATE supported
		conn.Write(replyFail)
		return
	}
}

// udpRelay relays UDP packets between client and destination per SOCKS5 UDP protocol.
func udpRelay(udpConn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, _, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// Parse SOCKS5 UDP request header
		if n < 10 || buf[0] != 0x00 || buf[1] != 0x00 {
			continue
		}
		atyp := buf[3]
		var host string
		var port int
		var addrLen int
		switch atyp {
		case socksAddrTypeIPv4:
			if n < 4+ipv4Len+portLen {
				continue
			}
			host = net.IP(buf[4 : 4+ipv4Len]).String()
			port = int(buf[4+ipv4Len])<<8 | int(buf[5+ipv4Len])
			addrLen = 4 + ipv4Len + portLen
		case socksAddrTypeDomain:
			dlen := int(buf[4])
			if n < 5+dlen+portLen {
				continue
			}
			host = string(buf[5 : 5+dlen])
			port = int(buf[5+dlen])<<8 | int(buf[6+dlen])
			addrLen = 5 + dlen + portLen
		case socksAddrTypeIPv6:
			if n < 4+ipv6Len+portLen {
				continue
			}
			host = net.IP(buf[4 : 4+ipv6Len]).String()
			port = int(buf[4+ipv6Len])<<8 | int(buf[5+ipv6Len])
			addrLen = 4 + ipv6Len + portLen
		default:
			continue
		}
		destAddr := net.JoinHostPort(host, itoa(port))
		// Forward UDP payload to destination
		dst, err := net.Dial("udp", destAddr)
		if err != nil {
			continue
		}
		dst.Write(buf[addrLen:n])
		dst.Close()
	}
}

// ioCopy is a thin wrapper for io.Copy, ignoring errors.
func ioCopy(dst, src net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_, _ = dst.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}
