package main

import (
	"fmt"
	"log"
	"net"
	"sync"
)

type SalmonBridge struct {
	BridgePort    int
	BridgeAddress string
	tunnel        net.Conn
	clientConns   map[uint32]net.Conn
	tunnelMutex   sync.Mutex
}

func (s *SalmonBridge) handleTunnelClose() {
	log.Printf("NEAR BRIDGE tunnel closed, cleaning up")
	// Reset tunnel
	s.tunnelMutex.Lock()
	defer s.tunnelMutex.Unlock()
	s.tunnel.Close()
	s.tunnel = nil
}

func (s *SalmonBridge) farToNearRelay() {
	if s.tunnel == nil {
		log.Printf("NEAR BRIDGE tunnel is nil, cannot start nearTunnel")
		return
	}
	for {
		f, err := decodeFrame(s.tunnel)
		if err != nil {
			log.Printf("NEAR BRIDGE tunnel error: %v", err)
			s.handleTunnelClose()
			return
		}

		if s.clientConns[f.ConnID] == nil {
			log.Printf("NEAR BRIDGE received data for unknown connID %d", f.ConnID)
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

func (s *SalmonBridge) clientToFarRelay(connID uint32, c net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			break
		}
		if s.tunnel == nil {
			log.Printf("NEAR BRIDGE tunnel has died, cannot relay data")
			break
		}
		s.tunnel.Write(encodeFrame(Frame{Type: MsgData, ConnID: connID, Data: buf[:n]}))
	}

	if s.tunnel != nil {
		s.tunnel.Write(encodeFrame(Frame{Type: MsgClose, ConnID: connID}))
	}

	c.Close()
	delete(s.clientConns, connID)
	log.Printf("NEAR BRIDGE clientToFarRelay closed for id %d", connID)
}

func (s *SalmonBridge) NewNearConn(host string, port int) (net.Conn, error) {
	log.Printf("NEAR BRIDGE New connection to %s:%d", host, port)
	s.tunnelMutex.Lock()
	defer s.tunnelMutex.Unlock()
	if s.tunnel == nil {
		s.clientConns = make(map[uint32]net.Conn)
		log.Printf("NEAR BRIDGE IS DOWN - RECONNECTING")
		bridgeAddr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)
		var err error
		s.tunnel, err = net.Dial("tcp", bridgeAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to bridge: %w", err)
		}
		go s.farToNearRelay()
		log.Printf("NEAR BRIDGE IS UP for bridgeAddr: %s", bridgeAddr)
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

	return clientSideCon, nil
}

func (s *SalmonBridge) farConnectionHandler(connID uint32, target net.Conn) {
	buf := make([]byte, 65535)
	for {
		n, err := target.Read(buf)
		if err != nil {
			log.Printf("FAR BRIDGE target read error: %v", err)
			break
		}
		dataFrame := Frame{Type: MsgData, ConnID: connID, Data: buf[:n]}
		log.Printf("FAR BRIDGE sending frame response: %d", len(dataFrame.Data))
		s.tunnel.Write(encodeFrame(dataFrame))
		log.Printf("FAR BRIDGE sent frame response.")
	}
	s.tunnel.Write(encodeFrame(Frame{Type: MsgClose, ConnID: connID}))
	log.Printf("FAR BRIDGE sent close frame for id %d", connID)
}

// Currently only support TCP outbound connections
func (s *SalmonBridge) handleFarListenConnections(tunnel net.Conn) {
	for {
		f, err := decodeFrame(tunnel)
		if err != nil {
			log.Printf("FAR BRIDGE decodeFrame error: %v", err)
			break
		}
		log.Printf("FAR BRIDGE recieved frame of len %d", len(f.Data))
		switch f.Type {
		case MsgOpen:
			log.Printf("FAR BRIDGE MSG OPEN received")
			targetAddr := string(f.Data)
			target, err := net.Dial("tcp", targetAddr)
			if err != nil {
				log.Printf("FAR BRIDGE failed to connect to target %s: %v", targetAddr, err)
				// TODO close back?
				continue
			}
			s.clientConns[f.ConnID] = target

			// Relay target responses back through tunnel
			go s.farConnectionHandler(f.ConnID, target)

		case MsgData:
			if target := s.clientConns[f.ConnID]; target != nil {
				log.Printf("FAR BRIDGE forwarded data for id %d", f.ConnID)
				target.Write(f.Data)
			}
		case MsgClose:
			if target := s.clientConns[f.ConnID]; target != nil {
				log.Printf("FAR BRIDGE CLOSED for id %d", f.ConnID)
				target.Close()
				delete(s.clientConns, f.ConnID)
			}
		}
	}
}

// Do we need to limit the number of tunnels?
func (s *SalmonBridge) NewFarListen(listenAddr string) error {
	s.clientConns = make(map[uint32]net.Conn)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("FAR BRIDGE Failed to listen on %s %v", listenAddr, err)

	}
	log.Printf("FAR BRIDGE listening on %s", listenAddr)
	for {
		tunnel, err := ln.Accept()
		if err != nil {
			log.Printf("FAR BRIDGE Accept error: %v", err)
			continue
		}
		go s.handleFarListenConnections(tunnel)
	}
}
