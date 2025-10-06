package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
)

type SalmonBridge struct {
	BridgePort    int
	BridgeAddress string
	tunnel        *quic.Conn   // instead of net.Conn
	tunnelStream  *quic.Stream // single control stream for frames
	clientConns   map[uint32]net.Conn
	tunnelMutex   sync.Mutex
}

func (s *SalmonBridge) handleTunnelClose() {
	log.Printf("NEAR BRIDGE tunnel closed, cleaning up")
	// Reset tunnel
	s.tunnelMutex.Lock()
	defer s.tunnelMutex.Unlock()
	s.tunnelStream.Close()
	s.tunnel.CloseWithError(0, "closed by user")
	s.tunnel = nil
	s.tunnelStream = nil
}

func (s *SalmonBridge) farToNearRelay() {
	if s.tunnel == nil || s.tunnelStream == nil {
		log.Printf("NEAR BRIDGE tunnel is nil, cannot start nearTunnel")
		return
	}
	for {
		f, err := decodeFrame(s.tunnelStream)
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
		s.tunnelStream.Write(encodeFrame(Frame{Type: MsgData, ConnID: connID, Data: buf[:n]}))
	}

	if s.tunnel != nil {
		s.tunnelStream.Write(encodeFrame(Frame{Type: MsgClose, ConnID: connID}))
	}

	c.Close()
	delete(s.clientConns, connID)
	log.Printf("NEAR BRIDGE clientToFarRelay closed for id %d", connID)
}

func (s *SalmonBridge) dialQUIC(bridgeAddr string) (*quic.Conn, *quic.Stream, error) {
	log.Printf("NEAR BRIDGE dialing QUIC to %s", bridgeAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // 3s handshake timeout
	defer cancel()
	quicConfig := &quic.Config{}
	// Set custom MTU if required (e.g., s.MTU > 0)
	quicConfig.InitialPacketSize = 8400
	quicConfig.MaxIdleTimeout = 10 * time.Second

	quic, err := quic.DialAddr(ctx, bridgeAddr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"salmon-bridge"}}, quicConfig)

	if err != nil {
		log.Printf("NEAR BRIDGE failed to dial QUIC: %v", err)
		return nil, nil, fmt.Errorf("failed to connect to bridge: %w", err)
	}
	log.Printf("NEAR BRIDGE opening steam")

	quicStream, errr := quic.OpenStreamSync(context.Background())
	if errr != nil {
		return nil, nil, fmt.Errorf("failed to open stream: %w", errr)
	}

	return quic, quicStream, nil
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
		s.tunnel, s.tunnelStream, err = s.dialQUIC(bridgeAddr)
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

	// TODO find a better way to handle this

	_, err := s.tunnelStream.Write(encodeFrame(openFrame))
	if err != nil {
		log.Printf("NEAR BRIDGE failed to write open frame: %v", err)
		return nil, fmt.Errorf("failed to write open frame: %w", err)
	}

	// Block here until we get an OPEN back from far side
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
		s.tunnelStream.Write(encodeFrame(dataFrame))
		log.Printf("FAR BRIDGE sent frame response.")
	}
	s.tunnelStream.Write(encodeFrame(Frame{Type: MsgClose, ConnID: connID}))
	log.Printf("FAR BRIDGE sent close frame for id %d", connID)
}

// Currently only support TCP outbound connections
func (s *SalmonBridge) handleFarListenConnections(tunnel *quic.Stream) {
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
			// tunnel.Write(encodeFrame(Frame{Type: MsgOpen, ConnID: f.ConnID}))
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

func (s *SalmonBridge) handleFarQUICConnection(conn *quic.Conn) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			log.Printf("FAR BRIDGE AcceptStream error: %v", err)
			return
		}
		// TODO - we should handle multiple streams
		s.tunnelStream = stream
		go s.handleFarListenConnections(stream)
	}
}

// Do we need to limit the number of tunnels?
func (s *SalmonBridge) NewFarListen(listenAddr string) error {
	s.clientConns = make(map[uint32]net.Conn)

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{generateSelfSignedCert()},
		NextProtos:   []string{"salmon-bridge"},
	}

	ln, err := quic.ListenAddr(listenAddr, tlsConf, nil)
	if err != nil {
		log.Fatalf("FAR BRIDGE Failed to listen on %s %v", listenAddr, err)
	}
	log.Printf("FAR BRIDGE listening on %s", listenAddr)

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Printf("FAR BRIDGE Accept error: %v", err)
			continue
		}
		go s.handleFarQUICConnection(conn)
	}
}
