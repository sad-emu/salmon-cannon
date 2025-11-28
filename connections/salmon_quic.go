package connections

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

type quicConnection struct {
	conn          *quic.Conn
	pconn         net.PacketConn
	activeStreams int32 // atomic counter
	createdAt     time.Time
	lastUsed      time.Time
	mu            sync.Mutex
}

type SalmonQuic struct {
	BridgePort    int
	BridgeAddress string
	BridgeName    string

	connections   []*quicConnection
	connectionsMu sync.RWMutex
	qcfg          *quic.Config
	tlscfg        *tls.Config
	interfaceName string
	cleanupOnce   sync.Once
}

func NewSalmonQuic(port int, address string, name string, tlscfg *tls.Config,
	qcfg *quic.Config, interfaceName string) *SalmonQuic {
	sq := &SalmonQuic{
		BridgeName:    name,
		BridgeAddress: address,
		BridgePort:    port,
		tlscfg:        tlscfg,
		qcfg:          qcfg,
		interfaceName: interfaceName,
		connections:   make([]*quicConnection, 0, MaxConnectionsPerBridge),
	}
	// Start cleanup goroutine
	sq.cleanupOnce.Do(func() {
		go sq.connectionCleanupLoop()
	})
	return sq
}

func listenPacketOnInterface(network, ifname string) (net.PacketConn, error) {
	// Platform-specific SO_BINDTODEVICE first (only supported on Linux)
	if runtime.GOOS == "linux" {
		lc := net.ListenConfig{
			Control: func(network, address string, c syscall.RawConn) error {
				var serr error
				if err := c.Control(func(fd uintptr) {
					serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifname)
				}); err != nil {
					// RawConn.Control returned an error
					return err
				}
				return serr
			},
		}
		pc, err := lc.ListenPacket(context.Background(), network, "0.0.0.0:0")
		if err == nil {
			return pc, nil
		}
	}
	return nil, fmt.Errorf("no usable address found on interface %s", ifname)
}

func listenPacketOnInterfaceForListen(network, ifname string, port int) (net.PacketConn, error) {
	addr := fmt.Sprintf(":%d", port)

	// Linux SO_BINDTODEVICE — binds the socket to the interface itself.
	if runtime.GOOS == "linux" {
		lc := net.ListenConfig{
			Control: func(_network, _address string, c syscall.RawConn) error {
				var serr error
				if err := c.Control(func(fd uintptr) {
					serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifname)
				}); err != nil {
					// RawConn.Control returned an error
					return err
				}
				return serr
			},
		}
		if pc, err := lc.ListenPacket(context.Background(), network, addr); err == nil {
			return pc, nil
		}
	}
	return nil, fmt.Errorf("no usable address found on interface %s", ifname)
}

// createNewConnection creates a new QUIC connection
func (s *SalmonQuic) createNewConnection(ctx context.Context) (*quicConnection, error) {
	addr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var qc *quic.Conn
	var pc net.PacketConn
	var err error

	// If an interface name is provided, create a PacketConn bound to that interface
	// Only supported on Linux via SO_BINDTODEVICE
	if s.interfaceName != "" {
		pc, err = listenPacketOnInterface("udp", s.interfaceName)
		if err != nil {
			return nil, fmt.Errorf("bind to interface %q: %w", s.interfaceName, err)
		}

		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			_ = pc.Close()
			return nil, fmt.Errorf("resolve UDP addr %s: %w", addr, err)
		}
		qc, err = quic.Dial(dialCtx, pc, udpAddr, s.tlscfg, s.qcfg)
		if err != nil {
			_ = pc.Close()
			return nil, fmt.Errorf("dial QUIC %s via interface %s: %w", addr, s.interfaceName, err)
		}

		log.Printf("NEAR: New QUIC bridge for %s connected to far host %s:%d via interface %s", s.BridgeName, s.BridgeAddress, s.BridgePort, s.interfaceName)
	} else {
		// Default: dial without binding to a specific interface
		qc, err = quic.DialAddr(dialCtx, addr, s.tlscfg, s.qcfg)
		if err != nil {
			return nil, fmt.Errorf("dial QUIC %s: %w", addr, err)
		}

		log.Printf("NEAR: New QUIC bridge for %s connected to far host %s:%d", s.BridgeName, s.BridgeAddress, s.BridgePort)
	}

	qconnection := &quicConnection{
		conn:          qc,
		pconn:         pc,
		activeStreams: 0,
		createdAt:     time.Now(),
		lastUsed:      time.Now(),
	}

	return qconnection, nil
}

// selectConnection finds a suitable connection or creates a new one
func (s *SalmonQuic) selectConnection() (*quicConnection, error) {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()

	// Can we to create a new connection
	if len(s.connections) < MaxConnectionsPerBridge {
		newConnection, err := s.createNewConnection(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to create new connection: %w", err)
		}

		s.connections = append(s.connections, newConnection)
		log.Printf("NEAR: Created new connection (total: %d/%d) for %s", len(s.connections), MaxConnectionsPerBridge, s.BridgeName)
		return newConnection, nil
	} else {
		// Find the connection with the least number of active streams
		var selected *quicConnection
		var minStreams int32 = MaxStreamsPerConnection
		for _, conn := range s.connections {
			activeStreams := atomic.LoadInt32(&conn.activeStreams)
			if activeStreams < MaxStreamsPerConnection && activeStreams < minStreams {
				selected = conn
				minStreams = activeStreams
			}
		}

		// If found a suitable connection, use it
		if selected != nil {
			return selected, nil
		}
		return nil, fmt.Errorf("all connections are at maximum stream capacity")
	}
}

// closeConnection safely closes a connection
func (s *SalmonQuic) closeConnection(qconn *quicConnection) {
	qconn.mu.Lock()
	defer qconn.mu.Unlock()

	if qconn.conn != nil {
		_ = qconn.conn.CloseWithError(0, "idle timeout")
		qconn.conn = nil
	}
	if qconn.pconn != nil {
		_ = qconn.pconn.Close()
		qconn.pconn = nil
	}

	// // This need to remove it from the pool as well
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()

	// Remove from connections slice
	for i, conn := range s.connections {
		if conn == qconn {
			s.connections = append(s.connections[:i], s.connections[i+1:]...)
			break
		}
	}
}

// connectionCleanupLoop periodically removes idle connections
func (s *SalmonQuic) connectionCleanupLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.connectionsMu.Lock()

		// Check each connection for idle timeout
		activeConnections := make([]*quicConnection, 0, len(s.connections))
		for _, conn := range s.connections {
			activeCount := atomic.LoadInt32(&conn.activeStreams)

			// Keep connection if it has active streams or was recently used
			if activeCount > 0 || time.Since(conn.lastUsed) < ConnectionIdleTimeout {
				activeConnections = append(activeConnections, conn)
			} else {
				log.Printf("NEAR: Closing idle connection for %s (last used: %v ago)", s.BridgeName, time.Since(conn.lastUsed))
				s.closeConnection(conn)
			}
		}

		s.connections = activeConnections
		s.connectionsMu.Unlock()
	}
}

// OpenStream opens a QUIC stream using the bridge pool
// Returns the stream and a cleanup function that MUST be called when done
func (s *SalmonQuic) OpenStream() (*quic.Stream, func(), error) {
	// Select or create a connection
	qconn, err := s.selectConnection()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to select connection: %w", err)
	}

	// Increment active stream counter
	atomic.AddInt32(&qconn.activeStreams, 1)

	// Update last used timestamp
	qconn.mu.Lock()
	qconn.lastUsed = time.Now()
	qconn.mu.Unlock()

	if qconn == nil {
		atomic.AddInt32(&qconn.activeStreams, -1)
		return nil, nil, fmt.Errorf("connection is nil")
	}

	// Open stream with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream, err := qconn.conn.OpenStreamSync(ctx)
	if err != nil {
		atomic.AddInt32(&qconn.activeStreams, -1)
		// This connection is no good, close it
		s.closeConnection(qconn)
		return nil, nil, fmt.Errorf("failed to open stream: %w", err)
	}

	// Cleanup function to decrement counter
	cleanup := func() {
		atomic.AddInt32(&qconn.activeStreams, -1)
	}

	return stream, cleanup, nil
}

func shouldBlockHost(expectedRemote string, newRemote string) bool {
	if expectedRemote != "" {
		if expectedRemote != newRemote {
			return true
		}
	}
	return false
}

func (s *SalmonQuic) NewFarListen(handleIncomingStream func(*quic.Stream)) error {
	listenAddr := fmt.Sprintf(":%d", s.BridgePort)
	log.Printf("FAR: Address farListenAddr: '%s' (len=%d)\n", listenAddr, len(listenAddr))

	// If you specify an interface name it will fail if that interface is not present
	// or has no usable addresses. If you don't need to configure this do not specify an interface name.
	if s.interfaceName != "" {
		pc, err := listenPacketOnInterfaceForListen("udp", s.interfaceName, s.BridgePort)
		if err != nil {
			return fmt.Errorf("bind to interface %q: %w", s.interfaceName, err)
		}
		// Keep pc open for the lifetime of the listener (do not close here).
		l, err := quic.Listen(pc, s.tlscfg, s.qcfg)
		if err != nil {
			_ = pc.Close()
			return fmt.Errorf("listen QUIC %s on interface %s: %w", listenAddr, s.interfaceName, err)
		}
		log.Printf("FAR: Bridge %s listening on %s via interface %s", s.BridgeName, listenAddr, s.interfaceName)

		for {
			conn, err := l.Accept(context.Background())
			// Ip filtering if BridgeAddress is set
			remoteAddr, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			if shouldBlockHost(s.BridgeAddress, remoteAddr) {
				log.Printf("FAR: Bridge %s rejected connection from unexpected address %s (expected %s)", s.BridgeName, remoteAddr, s.BridgeAddress)
				_ = conn.CloseWithError(0, "unexpected address")
				continue
			}
			if err != nil {
				log.Printf("FAR: Bridge %s accept conn error: %v", s.BridgeName, err)
				continue
			}
			go func(c *quic.Conn) {
				for {
					stream, err := c.AcceptStream(context.Background())
					if err != nil {
						log.Printf("FAR: Bridge %s AcceptStream closed: %v", s.BridgeName, err)
						return
					}
					go handleIncomingStream(stream)
				}
			}(conn)
		}
	} else {
		l, err := quic.ListenAddr(listenAddr, s.tlscfg, s.qcfg)
		if err != nil {
			return fmt.Errorf("listen QUIC %s: %w", listenAddr, err)
		}
		log.Printf("FAR: Bridge %s listening on %s", s.BridgeName, listenAddr)

		for {
			qc, err := l.Accept(context.Background())
			// Ip filtering if BridgeAddress is set
			remoteAddr, _, _ := net.SplitHostPort(qc.RemoteAddr().String())
			if shouldBlockHost(s.BridgeAddress, remoteAddr) {
				log.Printf("FAR: Bridge %s rejected connection from unexpected address %s (expected %s)", s.BridgeName, remoteAddr, s.BridgeAddress)
				_ = qc.CloseWithError(0, "unexpected address")
				continue
			}
			if err != nil {
				log.Printf("FAR: Bridge %s accept conn error: %v", s.BridgeName, err)
				continue
			}

			go func(conn *quic.Conn) {
				for {
					stream, err := conn.AcceptStream(context.Background())
					if err != nil {
						log.Printf("FAR: Bridge %s AcceptStream closed: %v", s.BridgeName, err)
						return
					}
					go handleIncomingStream(stream)
				}
			}(qc)
		}
	}
}
