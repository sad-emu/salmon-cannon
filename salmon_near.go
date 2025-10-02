package main

import (
	"fmt"
	"net"
)

type SalmonNear struct {
	farIP          string
	farPort        int
	conn           net.Conn
	allowedBridges []BridgeType // acceptable bridges in order of preference
	bridgeType     BridgeType
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
		bridgeType:     BridgeNone, // Default to none
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

	case BridgeQUIC:
		return fmt.Errorf("QUIC bridge not implemented yet")
	default:
		return fmt.Errorf("no compatible bridge found")
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
