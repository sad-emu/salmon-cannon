package main

import (
	"fmt"
	"net"
)

type SalmonNear struct {
	farIP   string
	farPort int
	conn    net.Conn
}

func NewSalmonNear(farIP string, farPort int) (*SalmonNear, error) {
	addr := fmt.Sprintf("%s:%d", farIP, farPort)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	near := &SalmonNear{
		farIP:   farIP,
		farPort: farPort,
		conn:    conn,
	}
	// Request available bridges from far
	if err := near.requestBridges(); err != nil {
		conn.Close()
		return nil, err
	}
	return near, nil
}

func (n *SalmonNear) requestBridges() error {
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
	if buf[0] != HeaderRequestBridges {
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
	return nil
}
