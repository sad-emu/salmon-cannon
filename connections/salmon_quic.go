package connections

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"runtime"
	"salmoncannon/limiter"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

type SalmonQuic struct {
	BridgePort    int
	BridgeAddress string
	BridgeName    string

	mu    sync.Mutex
	qconn *quic.Conn // single long-lived QUIC connection
	pconn net.PacketConn

	sl *limiter.SharedLimiter

	bridgeDown    bool
	qcfg          *quic.Config
	tlscfg        *tls.Config
	interfaceName string
}

func NewSalmonQuic(port int, address string, name string, tlscfg *tls.Config,
	qcfg *quic.Config, sl *limiter.SharedLimiter, interfaceName string) *SalmonQuic {
	return &SalmonQuic{
		BridgeName:    name,
		BridgeAddress: address,
		BridgePort:    port,
		tlscfg:        tlscfg,
		qcfg:          qcfg,
		sl:            sl,
		bridgeDown:    true,
		interfaceName: interfaceName,
	}
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

func (s *SalmonQuic) ensureQUIC(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.qconn != nil && s.bridgeDown == false {
		return nil
	}

	// close old quic connection and underlying packet conn (if any)
	if s.qconn != nil {
		_ = s.qconn.CloseWithError(0, "reconnecting")
		s.qconn = nil
	}
	if s.pconn != nil {
		_ = s.pconn.Close()
		s.pconn = nil
	}

	addr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// If an interface name is provided, create a PacketConn bound to that interface
	// Only supported on Linux via SO_BINDTODEVICE
	if s.interfaceName != "" {
		pc, err := listenPacketOnInterface("udp", s.interfaceName)
		if err != nil {
			return fmt.Errorf("bind to interface %q: %w", s.interfaceName, err)
		}

		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			_ = pc.Close()
			return fmt.Errorf("resolve UDP addr %s: %w", addr, err)
		}
		qc, err := quic.Dial(dialCtx, pc, udpAddr, s.tlscfg, s.qcfg)
		if err != nil {
			_ = pc.Close()
			return fmt.Errorf("dial QUIC %s via interface %s: %w", addr, s.interfaceName, err)
		}

		s.bridgeDown = false
		s.pconn = pc
		s.qconn = qc

		log.Printf("NEAR: New bridge for %s connected to far host %s %d via interface %s", s.BridgeName, s.BridgeAddress, s.BridgePort, s.interfaceName)
		return nil
	} else {
		// Default: dial without binding to a specific interface
		qc, err := quic.DialAddr(dialCtx, addr, s.tlscfg, s.qcfg)
		if err != nil {
			return fmt.Errorf("dial QUIC %s: %w", addr, err)
		}
		s.bridgeDown = false
		s.qconn = qc

		log.Printf("NEAR: New bridge for %s connected to far host %s %d", s.BridgeName, s.BridgeAddress, s.BridgePort)
		return nil
	}
}

func (s *SalmonQuic) reconnectBridge() error {
	if err := s.ensureQUIC(context.Background()); err != nil {
		log.Printf("NEAR: Bridge %s creation failed: %v", s.BridgeName, err)
		return err
	}
	return nil
}

// OpenStream opens a QUIC stream, handling reconnection if needed
func (s *SalmonQuic) OpenStream() (*quic.Stream, error) {

	if err := s.reconnectBridge(); err != nil {
		return nil, err
	}

	// Check if connection is valid before proceeding
	s.mu.Lock()
	qconn := s.qconn
	s.mu.Unlock()

	if qconn == nil {
		return nil, fmt.Errorf("QUIC connection is nil")
	}

	// Log current connection stats for debugging
	// Uncomment if needed: log.Printf("NEAR: Bridge %s attempting to open stream", s.BridgeName)

	maxAttempts := 2
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Add a timeout context to prevent indefinite blocking
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		stream, streamErr := qconn.OpenStreamSync(ctx)
		cancel()
		if streamErr != nil {
			err = streamErr
			log.Printf("NEAR: OpenStreamSync failed (attempt %d/%d): %v", attempt, maxAttempts, err)
			s.mu.Lock()
			s.bridgeDown = true
			s.mu.Unlock()
			if attempt < maxAttempts {
				log.Printf("NEAR: Bridge %s attempting reconnect to far", s.BridgeName)

				if reconErr := s.reconnectBridge(); reconErr != nil {
					log.Printf("NEAR: Bridge %s reconnect failed: %v", s.BridgeName, reconErr)
					return nil, fmt.Errorf("reconnect failed: %w", reconErr)
				}
				// Update local reference after successful reconnect
				s.mu.Lock()
				qconn = s.qconn
				s.mu.Unlock()
				if qconn == nil {
					return nil, fmt.Errorf("QUIC connection is nil after reconnect")
				}

				continue
			} else {
				log.Printf("NEAR: Bridge %s failed to open stream after %d attempts", s.BridgeName, maxAttempts)
				return nil, fmt.Errorf("failed to open stream after %d attempts: %w", maxAttempts, err)
			}
		}
		// Success
		return stream, nil
	}
	return nil, err
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
