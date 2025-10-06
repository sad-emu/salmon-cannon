package main

import (
	"fmt"
	"log"
	"net"
)

type SalmonTCPBridgeConnection struct {
	structLength     uint32
	connectionString string
}

// Encode serializes the struct into bytes for network transmission.
func (c *SalmonTCPBridgeConnection) Encode() ([]byte, error) {
	connStrBytes := []byte(c.connectionString)
	c.structLength = uint32(4 + len(connStrBytes)) // 4 bytes for structLength field itself
	buf := make([]byte, c.structLength)

	// Write structLength (big endian)
	buf[0] = byte(c.structLength >> 24)
	buf[1] = byte(c.structLength >> 16)
	buf[2] = byte(c.structLength >> 8)
	buf[3] = byte(c.structLength)

	// Write connectionString bytes
	copy(buf[4:], connStrBytes)
	return buf, nil
}

// Decode deserializes bytes into the struct.
func (c *SalmonTCPBridgeConnection) Decode(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("data too short")
	}
	c.structLength = uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	if int(c.structLength) != len(data) {
		return fmt.Errorf("structLength mismatch: expected %d, got %d", c.structLength, len(data))
	}
	c.connectionString = string(data[4:])
	return nil
}

type SalmonTCPBridge struct {
	BridgePort    int
	BridgeAddress string
}

func (s *SalmonTCPBridge) NewNearConn(hostname string, port int) (net.Conn, error) {
	// Connect to the far bridge using BridgePort and BridgeAddress
	log.Printf("NEAR TCP BRIDGE New connection")
	bridgeAddr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)
	log.Printf("NEAR TCP BRIDGE for bridgeAddr: %s", bridgeAddr)
	conn, err := net.Dial("tcp", bridgeAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to bridge: %w", err)
	}
	log.Printf("NEAR TCP BRIDGE connected to far bridge")
	// Prepare the connection request
	connReq := &SalmonTCPBridgeConnection{
		connectionString: fmt.Sprintf("%s:%d", hostname, port),
	}
	log.Printf("NEAR TCP BRIDGE built connection packet")
	reqBytes, err := connReq.Encode()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to encode connection request: %w", err)
	}

	// Send the connection request
	_, err = conn.Write(reqBytes)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send connection request: %w", err)
	}

	log.Printf("NEAR TCP BRIDGE sent connection packet")
	// Return the established connection

	return conn, nil
}

func (s *SalmonTCPBridge) handleFarListenConnections(conn net.Conn) {
	log.Printf("FAR TCP BRIDGE New connection")
	defer conn.Close()
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		log.Printf("FAR TCP BRIDGE Read error: %v", err)
		return
	}
	log.Printf("FAR TCP BRIDGE read %d bytes", n)
	var connReq SalmonTCPBridgeConnection
	err = connReq.Decode(buf[:n])
	if err != nil {
		log.Printf("FAR TCP BRIDGE Decode error: %v", err)
		return
	}
	log.Printf("FAR TCP BRIDGE Received connection request: %s", connReq.connectionString)
	// Here you would handle the connection request, e.g., establish a new connection
	// For demonstration, we just log and close

	// 5. Connect to target
	target, err := net.Dial("tcp", connReq.connectionString)
	if err != nil {
		log.Printf("FAR TCP BRIDGE Connect error: %v", err)
		return
	}
	defer target.Close()
	// 6. Relay data
	go func() { ioCopy(target, conn) }()
	ioCopy(conn, target)
	log.Printf("FAR TCP BRIDGE Handling connection to %s", connReq.connectionString)
}

func (s *SalmonTCPBridge) NewFarListen(listenAddr string) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("FAR TCP BRIDGE Failed to listen on %s %v", listenAddr, err)

	}
	log.Printf("FAR TCP BRIDGE listening on %s", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("FAR TCP BRIDGE Accept error: %v", err)
			continue
		}
		go s.handleFarListenConnections(conn)
	}
}
