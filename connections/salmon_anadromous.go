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
	IdleTimeout time.Duration // dead-peer detection window (was MaxIdleTimeout)
	// InitialRetransmitTimeout is used before the RTT estimator has enough
	// samples; MinRetransmitTimeout is the estimator's lower bound. Zero
	// preserves Anadromous's defaults for either setting.
	InitialRetransmitTimeout time.Duration
	MinRetransmitTimeout     time.Duration
	StreamRecvBuffer         int // per-stream receive buffer ceiling (was MaxStreamReceiveWindow)
	PacketSize               int // max UDP datagram size, must match both ends (was InitialPacketSize)
	MaxStreams               int // concurrent streams per connection (was MaxIncomingStreams)
	// MaxBytesInFlight caps sent-but-unacknowledged bytes per stream. Zero
	// leaves Anadromous to derive a safe value from the UDP receive buffer.
	MaxBytesInFlight int
	// FECGroupSize selects the number of DATA frames protected by each XOR
	// parity frame. nil keeps Anadromous's default, while a pointer to zero
	// disables FEC. Both ends must use the same value.
	FECGroupSize *int
	// FEC2D adds orthogonal row parity. It requires FEC to be enabled and
	// should be configured identically at both ends.
	FEC2D bool

	// BandwidthLimit (bytes/sec) is the bridge's SBTotalBandwidthLimit
	// passed down as one bridge-wide transport pacing rate. Every connection
	// created by this bridge shares the same wire pacer, so FEC/retransmission
	// traffic cannot multiply the provisioned rate with the pool size.
	BandwidthLimit            int
	PacingDatagramOverhead    int // carrier-counted bytes outside each UDP payload
	PacingMinimumDatagramSize int // carrier minimum including overhead/padding
	PacingBurstBytes          int // zero selects Anadromous's ~2ms quantum
	TransportBatchSize        int // zero preserves Anadromous's default
}

func (c BridgeNetConfig) newWirePacer() *anadromous.WirePacer {
	if c.BandwidthLimit <= 0 {
		return nil
	}
	return anadromous.NewWirePacer(anadromous.WirePacerConfig{
		RateBytesPerSecond: int64(c.BandwidthLimit),
		BurstBytes:         int64(c.PacingBurstBytes),
		Accounting: anadromous.WireAccounting{
			PerDatagramOverhead: int64(c.PacingDatagramOverhead),
			MinimumDatagramSize: int64(c.PacingMinimumDatagramSize),
		},
	})
}

// optionsWithPacer translates the config (plus optional interface binding)
// into Anadromous options. pacer is caller-owned so one pointer can be reused
// by every Dial/accepted Connection belonging to a bridge.
func (c BridgeNetConfig) optionsWithPacer(interfaceName string, pacer *anadromous.WirePacer) []anadromous.Option {
	var opts []anadromous.Option
	if c.IdleTimeout > 0 {
		opts = append(opts, anadromous.WithIdleTimeout(c.IdleTimeout))
	}
	if c.InitialRetransmitTimeout > 0 {
		opts = append(opts, anadromous.WithRetransmitTimeout(c.InitialRetransmitTimeout))
	}
	if c.MinRetransmitTimeout > 0 {
		opts = append(opts, anadromous.WithMinRetransmitTimeout(c.MinRetransmitTimeout))
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
	if c.MaxBytesInFlight > 0 {
		opts = append(opts, anadromous.WithMaxBytesInFlight(c.MaxBytesInFlight))
	}
	if c.FECGroupSize != nil {
		opts = append(opts, anadromous.WithFEC(*c.FECGroupSize))
	}
	if c.FEC2D {
		opts = append(opts, anadromous.WithFEC2D(true))
	}
	if c.TransportBatchSize > 0 {
		opts = append(opts, anadromous.WithBatchSize(c.TransportBatchSize))
	}
	if pacer != nil {
		opts = append(opts, anadromous.WithWirePacer(pacer))
	}
	if interfaceName != "" {
		opts = append(opts, anadromous.WithBindToDevice(interfaceName))
	}
	return opts
}

// options creates a fresh endpoint-wide pacer. NewSalmonAnadromous uses the
// split form above so it can retain and share the pointer across later Dials.
func (c BridgeNetConfig) options(interfaceName string) []anadromous.Option {
	return c.optionsWithPacer(interfaceName, c.newWirePacer())
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
	wirePacer     *anadromous.WirePacer
	interfaceName string
}

func NewSalmonAnadromous(port int, address string, name string,
	netcfg BridgeNetConfig, interfaceName string) *SalmonAnadromous {
	pacer := netcfg.newWirePacer()
	sq := &SalmonAnadromous{
		BridgeName:    name,
		BridgeAddress: address,
		BridgePort:    port,
		opts:          netcfg.optionsWithPacer(interfaceName, pacer),
		wirePacer:     pacer,
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
