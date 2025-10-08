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
}

func NewSalmonNear(config *config.SalmonBridgeConfig) (*SalmonNear, error) {
	bridgeAddress := config.FarIp
	bridgePort := config.FarPort

	qcfg := &quic.Config{
		MaxIdleTimeout:                 config.IdleTimeout.Duration(),
		InitialStreamReceiveWindow:     uint64(config.RecieveWindow),
		MaxStreamReceiveWindow:         uint64(config.MaxRecieveWindow),
		InitialConnectionReceiveWindow: uint64(config.RecieveWindow),
		MaxConnectionReceiveWindow:     uint64(config.MaxRecieveWindow),
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
	}

	return near, nil
}

func NewSalmonNearFromFar(salmonFar *SalmonFar) *SalmonNear {

	salmonBridge := salmonFar.farBridge

	near := &SalmonNear{
		currentBridge: salmonBridge,
	}

	return near
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

	// This is really noisy
	// log.Printf("NEAR: New request to connect to %s:%d", host, port)

	// 4. Open a streaming session to far
	stream, err := n.currentBridge.NewNearConn(host, port)
	if err != nil {
		conn.Write(replyFail)
		log.Fatalf("NEAR: Failed to open stream to far: %v", err)
		return
	}
	defer stream.Close()

	// 5. Reply: success
	conn.Write(replySuccess)

	// 6. Relay data
	go func() { io.Copy(stream, conn) }()
	io.Copy(conn, stream)
}
