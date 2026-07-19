package connections

import (
	"context"
	"fmt"
	"log"
	"net"
	"salmoncannon/status"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sad-emu/anadromous"
)

// BridgeNetConfig carries the transport tuning previously expressed through
// quic.Config / tls.Config. Anadromous has no TLS; when payload encryption
// is required, run the bridge with a SharedSecret so the AES layer covers it.
type BridgeNetConfig struct {
	IdleTimeout      time.Duration // dead-peer detection window (was MaxIdleTimeout)
	StreamRecvBuffer int           // per-stream receive buffer ceiling (was MaxStreamReceiveWindow)
	PacketSize       int           // max UDP datagram size, must match both ends (was InitialPacketSize)
	MaxStreams       int           // concurrent streams per connection (was MaxIncomingStreams)
}

// options translates the config (plus optional interface binding) into
// anadromous options.
func (c BridgeNetConfig) options(interfaceName string) []anadromous.Option {
	var opts []anadromous.Option
	if c.IdleTimeout > 0 {
		opts = append(opts, anadromous.WithIdleTimeout(c.IdleTimeout))
	}
	if c.StreamRecvBuffer > 0 {
		opts = append(opts, anadromous.WithStreamBufferSize(c.StreamRecvBuffer))
	}
	if c.PacketSize > 0 {
		opts = append(opts, anadromous.WithMaxDatagramSize(c.PacketSize))
	}
	if c.MaxStreams > 0 {
		opts = append(opts, anadromous.WithMaxStreams(c.MaxStreams))
	}
	if interfaceName != "" {
		opts = append(opts, anadromous.WithBindToDevice(interfaceName))
	}
	return opts
}

type anadromousConnection struct {
	conn          *anadromous.Connection
	activeStreams int32 // atomic counter
	createdAt     time.Time
	mu            sync.Mutex
}

type SalmonAnadromous struct {
	BridgePort    int
	BridgeAddress string
	BridgeName    string

	connections   []*anadromousConnection
	connectionsMu sync.RWMutex
	opts          []anadromous.Option
	interfaceName string
}

func NewSalmonAnadromous(port int, address string, name string,
	netcfg BridgeNetConfig, interfaceName string) *SalmonAnadromous {
	sq := &SalmonAnadromous{
		BridgeName:    name,
		BridgeAddress: address,
		BridgePort:    port,
		opts:          netcfg.options(interfaceName),
		interfaceName: interfaceName,
		connections:   make([]*anadromousConnection, 0, MaxConnectionsPerBridge),
	}
	// Reset the stream map for this bridge
	status.GlobalConnMonitorRef.ResetStreamCount(name)
	return sq
}

// createNewConnection dials a new anadromous connection to the far side.
func (s *SalmonAnadromous) createNewConnection(ctx context.Context) (*anadromousConnection, error) {
	addr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := anadromous.Dial(dialCtx, addr, s.opts...)
	if err != nil {
		if s.interfaceName != "" {
			return nil, fmt.Errorf("dial anadromous %s via interface %s: %w", addr, s.interfaceName, err)
		}
		return nil, fmt.Errorf("dial anadromous %s: %w", addr, err)
	}

	if s.interfaceName != "" {
		log.Printf("NEAR: New anadromous bridge for %s connected to far host %s:%d via interface %s",
			s.BridgeName, s.BridgeAddress, s.BridgePort, s.interfaceName)
	} else {
		log.Printf("NEAR: New anadromous bridge for %s connected to far host %s:%d",
			s.BridgeName, s.BridgeAddress, s.BridgePort)
	}

	return &anadromousConnection{
		conn:          conn,
		activeStreams: 0,
		createdAt:     time.Now(),
	}, nil
}

// selectConnection finds a suitable connection or creates a new one
func (s *SalmonAnadromous) selectConnection() (*anadromousConnection, error) {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()

	// Can we to create a new connection
	if len(s.connections) < MaxConnectionsPerBridge {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		newConnection, err := s.createNewConnection(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create new connection: %w", err)
		}

		s.connections = append(s.connections, newConnection)
		status.GlobalConnMonitorRef.AddStream(s.BridgeName)
		log.Printf("NEAR: Created new connection (total: %d/%d) for %s", len(s.connections), MaxConnectionsPerBridge, s.BridgeName)
		return newConnection, nil
	} else {
		// Find the connection with the least number of active streams
		var selected *anadromousConnection
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
			status.GlobalConnMonitorRef.AddStream(s.BridgeName)
			return selected, nil
		}
		return nil, fmt.Errorf("all connections are at maximum stream capacity")
	}
}

// CloseConnection safely closes a connection
func (s *SalmonAnadromous) CloseConnection(aconn *anadromousConnection) {
	aconn.mu.Lock()
	defer aconn.mu.Unlock()

	if aconn.conn != nil {
		_ = aconn.conn.CloseWithError(0, "idle timeout")
		aconn.conn = nil
	}

	// This need to remove it from the pool as well
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()

	// Remove from connections slice
	for i, conn := range s.connections {
		if conn == aconn {
			s.connections = append(s.connections[:i], s.connections[i+1:]...)
			break
		}
	}
}

// OpenStream opens an anadromous stream using the bridge pool
// Returns the stream and a cleanup function that MUST be called when done
func (s *SalmonAnadromous) OpenStream() (*anadromous.Stream, func(), error, *anadromousConnection) {
	// Select or create a connection
	aconn, err := s.selectConnection()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to select connection: %w", err), nil
	}

	// Increment active stream counter
	atomic.AddInt32(&aconn.activeStreams, 1)

	// Open stream with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream, err := aconn.conn.OpenStreamSync(ctx)
	if err != nil {
		atomic.AddInt32(&aconn.activeStreams, -1)
		// This connection is no good, close it
		s.CloseConnection(aconn)
		return nil, nil, fmt.Errorf("failed to open stream: %w", err), nil
	}

	// Cleanup function to decrement counter
	cleanup := func() {
		status.GlobalConnMonitorRef.RemoveStream(s.BridgeName)
		atomic.AddInt32(&aconn.activeStreams, -1)
	}

	return stream, cleanup, nil, aconn
}

func shouldBlockHost(expectedRemote string, newRemote string) bool {
	if expectedRemote != "" {
		if expectedRemote != newRemote {
			return true
		}
	}
	return false
}

func (s *SalmonAnadromous) NewFarListen(handleIncomingStream func(*anadromous.Stream)) error {
	listenAddr := fmt.Sprintf(":%d", s.BridgePort)
	log.Printf("FAR: Address farListenAddr: '%s' (len=%d)\n", listenAddr, len(listenAddr))

	// If you specify an interface name it will fail if that interface is not
	// present. If you don't need this do not specify an interface name.
	l, err := anadromous.Listen(listenAddr, s.opts...)
	if err != nil {
		if s.interfaceName != "" {
			return fmt.Errorf("listen anadromous %s on interface %s: %w", listenAddr, s.interfaceName, err)
		}
		return fmt.Errorf("listen anadromous %s: %w", listenAddr, err)
	}
	if s.interfaceName != "" {
		log.Printf("FAR: Bridge %s listening on %s via interface %s", s.BridgeName, listenAddr, s.interfaceName)
	} else {
		log.Printf("FAR: Bridge %s listening on %s", s.BridgeName, listenAddr)
	}

	for {
		conn, err := l.Accept(context.Background())
		if err != nil {
			if err == anadromous.ErrClosed {
				return err
			}
			log.Printf("FAR: Bridge %s accept conn error: %v", s.BridgeName, err)
			continue
		}

		// Ip filtering if BridgeAddress is set
		remoteAddr, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		if shouldBlockHost(s.BridgeAddress, remoteAddr) {
			log.Printf("FAR: Bridge %s rejected connection from unexpected address %s (expected %s)", s.BridgeName, remoteAddr, s.BridgeAddress)
			_ = conn.CloseWithError(0, "unexpected address")
			continue
		}

		go func(c *anadromous.Connection) {
			for {
				stream, err := c.AcceptStream(context.Background())
				if err != nil {
					log.Printf("FAR: Bridge %s AcceptStream closed: %v", s.BridgeName, err)
					return
				}
				status.GlobalConnMonitorRef.AddStream(s.BridgeName)
				go handleIncomingStream(stream)
			}
		}(conn)
	}
}
