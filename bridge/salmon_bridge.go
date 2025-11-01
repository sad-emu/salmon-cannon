package bridge

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"runtime"
	"salmoncannon/limiter"
	"salmoncannon/status"
	"slices"
	"sync"
	"syscall"
	"time"

	quic "github.com/quic-go/quic-go"
)

type SalmonBridge struct {
	BridgePort    int
	BridgeAddress string
	BridgeName    string

	mu    sync.Mutex
	qconn *quic.Conn // single long-lived QUIC connection
	pconn net.PacketConn

	sl *limiter.SharedLimiter

	bridgeDown          bool
	connector           bool
	qcfg                *quic.Config
	tlscfg              *tls.Config
	interfaceName       string
	allowedOutAddresses []string
}

func NewSalmonBridge(name string, address string, port int, tlscfg *tls.Config,
	qcfg *quic.Config, sl *limiter.SharedLimiter, connector bool, interfaceName string,
	allowedOutAddresses []string) *SalmonBridge {
	return &SalmonBridge{
		BridgeName:          name,
		BridgeAddress:       address,
		BridgePort:          port,
		tlscfg:              tlscfg,
		qcfg:                qcfg,
		sl:                  sl,
		connector:           connector,
		bridgeDown:          true,
		interfaceName:       interfaceName,
		allowedOutAddresses: allowedOutAddresses,
	}
}

// =========================================================
// Near side: dial far, open a new QUIC stream per TCP conn
// =========================================================

func (s *SalmonBridge) ensureQUIC(ctx context.Context) error {
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

func (s *SalmonBridge) reconnectBridge() error {
	if err := s.ensureQUIC(context.Background()); err != nil {
		log.Printf("NEAR: Bridge %s creation failed: %v", s.BridgeName, err)
		return err
	}
	return nil
}

func (s *SalmonBridge) StatusCheck() {
	clientSide, internal, stream, err := s.tryConnect()
	if err != nil {
		log.Printf("NEAR: Bridge %s status check connect error: %v", s.BridgeName, err)
		return
	}
	defer clientSide.Close()
	defer internal.Close()
	defer stream.Close()

	startTime := time.Now()
	if stream != nil {
		written, err := stream.Write([]byte{STATUS_HEADER})
		if err != nil && written != 1 {
			stream.Close()
			stream = nil
			log.Printf("NEAR: Bridge %s status check write error: %v", s.BridgeName, err)
		} else {
			// Read response
			buf := make([]byte, 1)
			stream.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := stream.Read(buf)
			if err != nil || n != 1 || buf[0] != STATUS_ACK {
				stream.Close()
				stream = nil
				log.Printf("NEAR: Bridge %s status check read error: %v", s.BridgeName, err)
			} else {
				elapsed := time.Since(startTime)
				// convert to ms
				status.GlobalConnMonitorRef.RegisterPing(s.BridgeName, elapsed.Milliseconds())
				written, err := stream.Write([]byte{STATUS_ACK})
				if err != nil && written != 1 {
					stream.Close()
					stream = nil
					log.Printf("NEAR: Bridge %s status check final write error: %v", s.BridgeName, err)
				}
				// Listen for the far side to close the stream
				buf = make([]byte, 1)
				stream.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, _ = stream.Read(buf)
			}
		}
	}

}

func (s *SalmonBridge) tryConnect() (net.Conn, net.Conn, *quic.Stream, error) {

	if s.connector {
		// Only connectors can initiate connections.
		if err := s.reconnectBridge(); err != nil {
			return nil, nil, nil, err
		}
	}

	// Create a local pipe: return one end to the caller, and connect the other to the QUIC stream.
	clientSide, internal := net.Pipe()

	// TODO maybe fix this mess????
	maxAttempts := 2
	stream := &quic.Stream{}
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Open a fresh bidirectional QUIC stream for this logical connection.
		stream, err = s.qconn.OpenStreamSync(context.Background())
		if err != nil {
			log.Printf("NEAR: OpenStreamSync closed: %v", err)
			s.mu.Lock()
			s.bridgeDown = true
			s.mu.Unlock()
			if attempt < maxAttempts {
				log.Printf("NEAR: Bridge %s reconnected to far", s.BridgeName)
				if s.connector {
					// Only connectors can initiate connections.
					_ = s.reconnectBridge()
				}
				continue
			} else {
				log.Printf("NEAR: Bridge %s failed to open stream after %d attempts", s.BridgeName, maxAttempts)
				return nil, nil, nil, fmt.Errorf("failed to open stream after %d attempts: %w", maxAttempts, err)
			}
		}
	}
	return clientSide, internal, stream, nil
}

// NewNearConn returns a net.Conn to the caller. Internally, it opens a new QUIC
// stream, sends a small header identifying the remote target (host:port),
// and then pipes bytes bidirectionally.
func (s *SalmonBridge) NewNearConn(host string, port int) (net.Conn, error) {

	clientSide, internal, stream, err := s.tryConnect()

	if err != nil {
		return nil, err
	}

	go func() {
		// Ensure we close the internal end if anything fails here.
		defer internal.Close()
		defer stream.Close()

		// 1) Send a small header carrying target address.
		target := fmt.Sprintf("%s:%d", host, port)
		if err := WriteTargetHeader(stream, target); err != nil {
			log.Printf("NEAR: write header error: %v", err)
			// If we fail before copying, cancel read to unblock far side quickly.
			stream.CancelRead(0)
			return
		}

		// 2) Pump data both ways.
		BidiPipe(stream, internal, s.sl)
	}()

	return clientSide, nil
}

// =========================================================
// Far side: accept streams, read header, dial target, pipe
// =========================================================

func shouldBlockHost(expectedRemote string, newRemote string) bool {
	if expectedRemote != "" {
		if expectedRemote != newRemote {
			return true
		}
	}
	return false
}

func (s *SalmonBridge) NewFarListen() error {
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
			if err != nil {
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
			go func(c *quic.Conn) {
				for {
					stream, err := c.AcceptStream(context.Background())
					if err != nil {
						log.Printf("FAR: Bridge %s AcceptStream closed: %v", s.BridgeName, err)
						return
					}
					go s.handleIncomingStream(stream)
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

			if err != nil {
				log.Printf("FAR: Bridge %s accept conn error: %v", s.BridgeName, err)
				continue
			}
			// Ip filtering if BridgeAddress is set
			remoteAddr, _, _ := net.SplitHostPort(qc.RemoteAddr().String())
			if shouldBlockHost(s.BridgeAddress, remoteAddr) {
				log.Printf("FAR: Bridge %s rejected connection from unexpected address %s (expected %s)", s.BridgeName, remoteAddr, s.BridgeAddress)
				_ = qc.CloseWithError(0, "unexpected address")
				continue
			}

			go func(conn *quic.Conn) {
				for {
					stream, err := conn.AcceptStream(context.Background())
					if err != nil {
						log.Printf("FAR: Bridge %s AcceptStream closed: %v", s.BridgeName, err)
						return
					}
					go s.handleIncomingStream(stream)
				}
			}(qc)
		}
	}
}

func (s *SalmonBridge) shouldBlockFarOutConn(outHostFull string) bool {
	if len(s.allowedOutAddresses) == 0 {
		return false
	}
	nearAddr, _, _ := net.SplitHostPort(outHostFull)
	return !slices.Contains(s.allowedOutAddresses, nearAddr)
}

func (s *SalmonBridge) handleStatusPing(stream *quic.Stream) {
	// Simple status response: number of active connections
	startTime := time.Now()
	_, err := stream.Write([]byte{STATUS_ACK})
	if err != nil {
		log.Printf("FAR: Bridge %s status write response error: %v", s.BridgeName, err)
		return
	}
	// Read ACK back
	buf := make([]byte, 1)
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := stream.Read(buf)
	if err != nil || n != 1 || buf[0] != STATUS_ACK {
		log.Printf("FAR: Bridge %s status read ACK error: %v", s.BridgeName, err)
		return
	}
	elapsed := time.Since(startTime)
	// convert to ms
	status.GlobalConnMonitorRef.RegisterPing(s.BridgeName, elapsed.Milliseconds())
}

func (s *SalmonBridge) handleIncomingStream(stream *quic.Stream) {
	// 1) Read target header.
	headerType, err := ReadHeaderType(stream)
	if err != nil {
		log.Printf("FAR: read header error: %v", err)
		stream.CancelRead(0)
		stream.Close()
		return
	}

	if headerType == STATUS_HEADER {
		// Handle status request
		// log.Printf("FAR: Bridge %s received status ping", s.BridgeName)
		s.handleStatusPing(stream)
		stream.Close()
		// log.Printf("FAR: Bridge %s closed stream for status ping", s.BridgeName)
		return
	}

	target, err := ReadTargetHeader(stream)
	if err != nil {
		log.Printf("FAR: read header error: %v", err)
		stream.CancelRead(0)
		stream.Close()
		return
	}

	// 2) Check for allowed outbound IPs/Hostnames
	if s.shouldBlockFarOutConn(target) {
		log.Printf("FAR: target addr not found in allow list: %s", target)
		stream.CancelRead(0)
		stream.Close()
		return
	}

	// 3) Dial target TCP.
	dst, err := net.Dial("tcp", target)
	if err != nil {
		log.Printf("FAR: dial on bridge %s failed %s error: %v", s.BridgeName, target, err)
		stream.CancelRead(0)
		stream.Close()
		return
	}
	// Ensure we close both sides.
	defer func() {
		dst.Close()
		stream.Close()
		status.GlobalConnMonitorRef.DecOUT()
	}()

	// Increment active OUT connections
	status.GlobalConnMonitorRef.IncOUT()

	// 4) Pipe bytes both directions.
	BidiPipe(stream, dst, s.sl)
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
