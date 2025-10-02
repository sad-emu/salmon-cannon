package main

import (
	"context"
	"fmt"
	"net"
)

type SalmonNear struct {
	farIP          string
	farPort        int
	conn           net.Conn
	allowedBridges []BridgeType // acceptable bridges in order of preference
	bridgeType     BridgeType
	currentBridge  SalmonTCPBridge
}

func NewSalmonNear(farIP string, farPort int, allowedBridges []BridgeType) (*SalmonNear, error) {
	addr := fmt.Sprintf("%s:%d", farIP, farPort)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	near := &SalmonNear{
		farIP:          farIP,
		farPort:        farPort,
		conn:           conn,
		allowedBridges: allowedBridges,
		bridgeType:     BridgeTCP, // Default to none
		currentBridge:  SalmonTCPBridge{},
	}
	// Request available bridges from far
	if err := near.configureBridges(); err != nil {
		conn.Close()
		return nil, err
	}
	return near, nil
}

func (n *SalmonNear) Connect() error {
	switch n.bridgeType {
	case BridgeTCP:
		return n.currentBridge.Connect(fmt.Sprintf("%s:%d", "127.0.0.1", 1098))
	case BridgeQUIC:
		return fmt.Errorf("QUIC bridge not implemented yet")
	default:
		return fmt.Errorf("no compatible bridge found")
	}
}

func (n *SalmonNear) HandleRequest(conn net.Conn) {
	defer conn.Close()
	println("New near request 1")

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
	println("New near request 2")

	// 3. Read request
	readb, err = conn.Read(buf)
	if err != nil || readb < requestMinLen {
		return
	}
	if buf[0] != socksVersion5 {
		return
	}

	println("New near request 3")

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
	ctx := context.Background()
	stream, err := n.currentBridge.OpenStream(ctx, host, port)
	if err != nil {
		conn.Write(replyFail)
		return
	}
	//defer close(stream.Close())

	// 5. Reply: success
	conn.Write(replySuccess)

	// 6. Pump data both ways

	// Client → Far
	go func() {
		buf := make([]byte, 65536)
		for {
			nRead, err := conn.Read(buf)
			if err != nil {
				// client closed
				//close(stream.sendCh)
				return
			}
			// Copy chunk (avoid reusing buffer)
			chunk := append([]byte(nil), buf[:nRead]...)
			stream.sendCh <- chunk
		}
	}()

	// Far → Client
	for {
		select {
		case data, ok := <-stream.recvCh:
			if !ok {
				return // far closed
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		// case err := <-stream.Close().Error():
		// 	if err != nil {
		// 		return
		// 	}
		case <-stream.closed:
			return
		}
	}
}

func (n *SalmonNear) configureBridges() error {
	// Send a simple handshake/request
	_, err := n.conn.Write([]byte{HeaderRequestBridges})
	if err != nil {
		return err
	}
	buf := make([]byte, 256)
	nRead, err := n.conn.Read(buf)
	if err != nil {
		return err
	}
	if nRead < 3 {
		return fmt.Errorf("response too short")
	}
	if buf[0] != HeaderResponseBridges {
		return fmt.Errorf("unexpected header: got 0x%02x", buf[0])
	}
	length := int(buf[1])
	if nRead < 2+length {
		return fmt.Errorf("payload length mismatch")
	}
	bridges := buf[2 : 2+length]
	fmt.Print("Available bridges from far: ")
	for _, b := range bridges {
		fmt.Printf("%d ", b)
	}
	fmt.Println()

	// Find the first allowed bridge present in the available bridges
	for _, allowed := range n.allowedBridges {
		for _, avail := range bridges {
			if byte(allowed) == avail {
				n.bridgeType = allowed
				// Found a match
				fmt.Printf("Selected bridge: %d\n", allowed)
			}
		}
	}
	if n.bridgeType == BridgeNone {
		// Format allowed and available bridges for error message
		allowedStr := ""
		for i, b := range n.allowedBridges {
			if i > 0 {
				allowedStr += ", "
			}
			allowedStr += fmt.Sprintf("%d", b)
		}
		availStr := ""
		for i, b := range bridges {
			if i > 0 {
				availStr += ", "
			}
			availStr += fmt.Sprintf("%d", b)
		}
		return fmt.Errorf("no allowed bridge found. allowed: [%s], available: [%s]", allowedStr, availStr)
	}

	return nil
}
