package main

import (
	"net"
)

type SalmonNear struct {
	farIP         string
	farPort       int
	conn          net.Conn
	currentBridge SalmonBridge
}

func NewSalmonNear(farIP string, farPort int) (*SalmonNear, error) {
	near := &SalmonNear{
		farIP:         farIP,
		farPort:       farPort,
		currentBridge: SalmonBridge{},
	}
	near.currentBridge.bridgeDown = true
	return near, nil
}

func (n *SalmonNear) HandleRequest(conn net.Conn) {
	defer conn.Close()

	// 1. Read greeting
	buf := make([]byte, maxMethods+2)
	readb, err := conn.Read(buf)
	if err != nil || readb < handshakeMinLen {
		return
	}
	if buf[0] != socksVersion5 {
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

	// 4. Open a streaming session to far
	n.currentBridge.BridgeAddress = "127.0.0.1"
	n.currentBridge.BridgePort = 55001
	stream, err := n.currentBridge.NewNearConn(host, port)
	if err != nil {
		conn.Write(replyFail)
		return
	}
	defer stream.Close()

	// 5. Reply: success
	conn.Write(replySuccess)

	// 6. Relay data
	go func() { ioCopy(stream, conn) }()
	ioCopy(conn, stream)
}
