package bridge

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"salmoncannon/connections"
	"salmoncannon/limiter"
	"salmoncannon/status"
	"slices"
	"time"

	quic "github.com/quic-go/quic-go"
)

type SalmonBridge struct {
	BridgeName string
	sq         *connections.SalmonQuic // Handler for QUIC connections

	sl                  *limiter.SharedLimiter
	connector           bool
	allowedOutAddresses []string
}

func NewSalmonBridge(name string, address string, port int, tlscfg *tls.Config,
	qcfg *quic.Config, sl *limiter.SharedLimiter, connector bool, interfaceName string,
	allowedOutAddresses []string) *SalmonBridge {
	sq := connections.NewSalmonQuic(port, address, name, tlscfg, qcfg, interfaceName)
	return &SalmonBridge{
		BridgeName:          name,
		sl:                  sl,
		sq:                  sq,
		connector:           connector,
		allowedOutAddresses: allowedOutAddresses,
	}
}

// =========================================================
// Near side: dial far, open a new QUIC stream per TCP conn
// =========================================================

func (s *SalmonBridge) StatusCheck() {
	stream, cleanup, err := s.sq.OpenStream()
	if err != nil {
		log.Printf("NEAR: Bridge %s status check connect error: %v", s.BridgeName, err)
		return
	}
	defer stream.Close()
	defer cleanup()

	startTime := time.Now()
	written, err := stream.Write([]byte{STATUS_HEADER})
	if err != nil || written != 1 {
		log.Printf("NEAR: Bridge %s status check write error: %v", s.BridgeName, err)
		return
	}

	// Read response
	buf := make([]byte, 1)
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := stream.Read(buf)
	if err != nil || n != 1 || buf[0] != STATUS_ACK {
		log.Printf("NEAR: Bridge %s status check read error: %v", s.BridgeName, err)
		return
	}

	elapsed := time.Since(startTime)
	// convert to ms
	status.GlobalConnMonitorRef.RegisterPing(s.BridgeName, elapsed.Milliseconds())

	written, err = stream.Write([]byte{STATUS_ACK})
	if err != nil || written != 1 {
		log.Printf("NEAR: Bridge %s status check final write error: %v", s.BridgeName, err)
		return
	}

	// Listen for the far side to close the stream
	buf = make([]byte, 1)
	stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = stream.Read(buf)
}

func (s *SalmonBridge) tryConnect() (net.Conn, net.Conn, *quic.Stream, error) {
	// Open the stream first
	stream, cleanup, err := s.sq.OpenStream()
	if err != nil {
		return nil, nil, nil, err
	}

	// Only create the pipe after we successfully have a stream
	// This prevents pipe leaks if stream creation fails
	clientSide, internal := net.Pipe()
	defer cleanup()
	return clientSide, internal, stream, nil
} // NewNearConn returns a net.Conn to the caller. Internally, it opens a new QUIC

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

func (s *SalmonBridge) NewFarListen() error {
	// Pass it down the the quic stream with the handler
	return s.sq.NewFarListen(s.handleIncomingStream)
}
