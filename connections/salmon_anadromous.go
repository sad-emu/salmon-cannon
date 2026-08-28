package connections

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"salmoncannon/status"
	"sync"
	"time"

	"github.com/sad-emu/anadromous"
)

// BridgeNetConfig carries the Anadromous transport tuning for a bridge.
// Anadromous has no TLS; when payload encryption is required, run the bridge
// with a SharedSecret so the AES layer covers it.
type BridgeNetConfig struct {
	IdleTimeout time.Duration // dead-peer detection window
	// InitialRetransmitTimeout is used before the RTT estimator has enough
	// samples; MinRetransmitTimeout is the estimator's lower bound. Zero
	// preserves Anadromous's defaults for either setting.
	InitialRetransmitTimeout time.Duration
	MinRetransmitTimeout     time.Duration
	StreamRecvBuffer         int // per-stream receive buffer ceiling
	PacketSize               int // max UDP datagram size, must match both ends
	MaxStreams               int // concurrent streams on the bridge connection
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

	// BandwidthLimit (bytes/sec) is the bridge's SBTotalBandwidthLimit passed
	// down as the transport pacing rate. FEC and retransmission traffic also
	// spend from this wire budget.
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
// into Anadromous options.
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

// options creates a fresh endpoint-wide pacer.
func (c BridgeNetConfig) options(interfaceName string) []anadromous.Option {
	return c.optionsWithPacer(interfaceName, c.newWirePacer())
}

type SalmonAnadromous struct {
	BridgePort    int
	BridgeAddress string
	BridgeName    string

	connection    *anadromous.Connection
	connectionMu  sync.Mutex
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
	}
	// Reset the stream map for this bridge
	status.GlobalConnMonitorRef.ResetStreamCount(name)
	return sq
}

// createNewConnection dials the Anadromous connection to the far side.
func (s *SalmonAnadromous) createNewConnection(ctx context.Context) (*anadromous.Connection, error) {
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

	return conn, nil
}

// getConnection returns the bridge's one transport connection, dialing it
// lazily when necessary. Holding the mutex across Dial prevents concurrent
// stream opens from creating duplicate connections.
func (s *SalmonAnadromous) getConnection() (*anadromous.Connection, error) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()

	if s.connection != nil {
		return s.connection, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := s.createNewConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}

	s.connection = conn
	log.Printf("NEAR: Created connection for %s", s.BridgeName)
	return conn, nil
}

// CloseConnection closes conn only if it is still this bridge's current
// connection. The identity check prevents a late failure on a stale stream
// from closing a replacement connection.
func (s *SalmonAnadromous) CloseConnection(conn *anadromous.Connection) {
	if conn == nil {
		return
	}

	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	if s.connection != conn {
		return
	}

	_ = conn.CloseWithError(0, "connection discarded")
	s.connection = nil
}

// OpenStream opens an Anadromous stream on the bridge's one connection.
// Returns the stream and a cleanup function that MUST be called when done
func (s *SalmonAnadromous) OpenStream() (*anadromous.Stream, func(), error, *anadromous.Connection) {
	conn, err := s.getConnection()
	if err != nil {
		return nil, nil, err, nil
	}

	stream, err := conn.OpenStream(context.Background())
	if err != nil {
		// Reaching the configured stream limit does not make the connection
		// unhealthy. Other open failures invalidate it so the next request can
		// establish a replacement.
		if !errors.Is(err, anadromous.ErrMaxStreams) {
			s.CloseConnection(conn)
		}
		return nil, nil, fmt.Errorf("failed to open stream: %w", err), nil
	}

	status.GlobalConnMonitorRef.AddStream(s.BridgeName)
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			status.GlobalConnMonitorRef.RemoveStream(s.BridgeName)
		})
	}

	return stream, cleanup, nil, conn
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
