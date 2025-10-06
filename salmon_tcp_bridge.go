package main

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

var globalConnID uint32

func nextID() uint32 {
	return atomic.AddUint32(&globalConnID, 1)
}

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
	tunnel        net.Conn
	clientConns   map[uint32]net.Conn
	tunnelMutex   sync.Mutex
}

func (s *SalmonTCPBridge) handleTunnelClose() {
	log.Printf("NEAR TCP BRIDGE tunnel closed, cleaning up")
	// Reset tunnel
	s.tunnelMutex.Lock()
	defer s.tunnelMutex.Unlock()
	s.tunnel.Close()
	s.tunnel = nil
}

func (s *SalmonTCPBridge) farToNearRelay() {
	if s.tunnel == nil {
		log.Printf("NEAR TCP BRIDGE tunnel is nil, cannot start nearTunnel")
		return
	}
	for {
		f, err := decodeFrame(s.tunnel)
		if err != nil {
			log.Printf("NEAR TCP BRIDGE tunnel error: %v", err)
			s.handleTunnelClose()
			return
		}

		if s.clientConns[f.ConnID] == nil {
			log.Printf("NEAR TCP BRIDGE received data for unknown connID %d", f.ConnID)
			continue
		}

		switch f.Type {
		case MsgData:
			client := s.clientConns[f.ConnID]
			if client != nil {
				client.Write(f.Data)
			}
		case MsgClose:
			if client := s.clientConns[f.ConnID]; client != nil {
				client.Close()
				delete(s.clientConns, f.ConnID)
			}
		}
	}
}

func (s *SalmonTCPBridge) clientToFarRelay(connID uint32, c net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			break
		}
		if s.tunnel == nil {
			log.Printf("NEAR TCP BRIDGE tunnel has died, cannot relay data")
			break
		}
		s.tunnel.Write(encodeFrame(Frame{Type: MsgData, ConnID: connID, Data: buf[:n]}))
	}

	if s.tunnel != nil {
		s.tunnel.Write(encodeFrame(Frame{Type: MsgClose, ConnID: connID}))
	}

	c.Close()
	delete(s.clientConns, connID)
	log.Printf("NEAR TCP BRIDGE clientToFarRelay closed for id %d", connID)
}

func (s *SalmonTCPBridge) NewNearConn(host string, port int) (net.Conn, error) {
	log.Printf("NEAR TCP BRIDGE New connection")
	s.tunnelMutex.Lock()
	defer s.tunnelMutex.Unlock()
	if s.tunnel == nil {
		s.clientConns = make(map[uint32]net.Conn)
		log.Printf("NEAR TCP BRIDGE IS DOWN - RECONNECTING")
		bridgeAddr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)
		var err error
		s.tunnel, err = net.Dial("tcp", bridgeAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to bridge: %w", err)
		}
		go s.farToNearRelay()
		log.Printf("NEAR TCP BRIDGE IS UP for bridgeAddr: %s", bridgeAddr)
	}

	clientSideCon, clientConn := net.Pipe()

	// Assign unique ID for this proxied connection
	connID := nextID() // e.g. atomic counter

	s.clientConns[connID] = clientConn

	// Send OPEN
	openFrame := Frame{
		Type:   MsgOpen,
		ConnID: connID,
		Data:   []byte(fmt.Sprintf("%s:%d", host, port)),
	}
	s.tunnel.Write(encodeFrame(openFrame))

	go s.clientToFarRelay(connID, clientConn)

	log.Printf("NEAR TCP BRIDGE sent connection packet")
	// Return the established connection

	return clientSideCon, nil
}

func (s *SalmonTCPBridge) handleFarListenConnections(tunnel net.Conn) {
	for {
		f, err := decodeFrame(tunnel)
		if err != nil {
			log.Printf("FAR TCP BRIDGE decodeFrame error: %v", err)
			break
		}
		log.Printf("FAR TCP BRIDGE recieved frame of len %d", len(f.Data))
		switch f.Type {
		case MsgOpen:
			log.Printf("FAR TCP BRIDGE MSG OPEN received")
			targetAddr := string(f.Data)
			target, err := net.Dial("tcp", targetAddr)
			if err != nil {
				log.Printf("FAR TCP BRIDGE failed to connect to target %s: %v", targetAddr, err)
				// optionally send CLOSE back
				continue
			}
			s.clientConns[f.ConnID] = target

			// Relay target responses back through tunnel
			go func(connID uint32, target net.Conn) {
				buf := make([]byte, 65535)
				for {
					n, err := target.Read(buf)
					if err != nil {
						log.Printf("FAR TCP BRIDGE target read error: %v", err)
						break
					}
					dataFrame := Frame{Type: MsgData, ConnID: connID, Data: buf[:n]}
					log.Printf("FAR TCP BRIDGE sending frame response: %d", len(dataFrame.Data))
					tunnel.Write(encodeFrame(dataFrame))
					log.Printf("FAR TCP BRIDGE sent frame response.")
				}
				tunnel.Write(encodeFrame(Frame{Type: MsgClose, ConnID: connID}))
				log.Printf("FAR TCP BRIDGE sent close frame for id %d", connID)
			}(f.ConnID, target)

		case MsgData:
			if target := s.clientConns[f.ConnID]; target != nil {
				log.Printf("FAR TCP BRIDGE forwarded data for id %d", f.ConnID)
				target.Write(f.Data)
			}
		case MsgClose:
			if target := s.clientConns[f.ConnID]; target != nil {
				log.Printf("FAR TCP BRIDGE CLOSED for id %d", f.ConnID)
				target.Close()
				delete(s.clientConns, f.ConnID)
			}
		}
	}
}

func (s *SalmonTCPBridge) NewFarListen(listenAddr string) error {
	s.clientConns = make(map[uint32]net.Conn)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("FAR TCP BRIDGE Failed to listen on %s %v", listenAddr, err)

	}
	log.Printf("FAR TCP BRIDGE listening on %s", listenAddr)
	for {
		tunnel, err := ln.Accept()
		if err != nil {
			log.Printf("FAR TCP BRIDGE Accept error: %v", err)
			continue
		}
		go s.handleFarListenConnections(tunnel)
	}
}
