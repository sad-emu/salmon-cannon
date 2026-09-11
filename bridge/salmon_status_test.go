package bridge

import (
	"context"
	"io"
	"net"
	"salmoncannon/connections"
	"salmoncannon/limiter"
	"salmoncannon/status"
	"testing"
	"time"

	"github.com/sad-emu/anadromous"
)

func TestStatusCheckWhileTCPResponsePendingAtOneMegabit(t *testing.T) {
	const bytesPerSecond = 1_000_000 / 8 // SBTotalBandwidthLimit: 1M
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	requestReceived := make(chan struct{})
	releaseResponse := make(chan struct{})
	targetResult := make(chan error, 1)
	go func() {
		conn, err := target.Accept()
		if err != nil {
			targetResult <- err
			return
		}
		defer conn.Close()
		deadline, _ := ctx.Deadline()
		conn.SetDeadline(deadline)
		buf := make([]byte, len("login"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			targetResult <- err
			return
		}
		close(requestReceived)
		select {
		case <-releaseResponse:
			_, err = conn.Write(buf)
			targetResult <- err
		case <-ctx.Done():
		}
	}()

	listener, err := anadromous.Listen("127.0.0.1:0",
		anadromous.WithIdleTimeout(time.Minute),
		anadromous.WithMaxDatagramSize(1350),
		anadromous.WithMaxStreams(50),
		anadromous.WithStreamBufferSize(100*1024*1024),
		anadromous.WithWirePacer(anadromous.NewWirePacer(anadromous.WirePacerConfig{
			RateBytesPerSecond: bytesPerSecond,
		})))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	far := &SalmonBridge{BridgeName: t.Name() + "/far", sl: limiter.NewSharedLimiter(bytesPerSecond)}
	go func() {
		conn, err := listener.Accept(ctx)
		if err != nil {
			return
		}
		for {
			stream, err := conn.AcceptStream(ctx)
			if err != nil {
				return
			}
			status.GlobalConnMonitorRef.AddStream(far.BridgeName)
			go far.handleIncomingStream(stream)
		}
	}()
	addr, err := net.ResolveUDPAddr("udp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	near := NewSalmonBridge(t.Name()+"/near", "127.0.0.1", addr.Port,
		connections.BridgeNetConfig{
			IdleTimeout: time.Minute, PacketSize: 1350, BandwidthLimit: bytesPerSecond,
			MaxStreams: 500, StreamRecvBuffer: 400 * 1024 * 1024,
		}, limiter.NewSharedLimiter(bytesPerSecond), "", nil, "")
	conn, err := near.NewNearConn("127.0.0.1", target.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte("login")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestReceived:
	case err := <-targetResult:
		t.Fatalf("target did not receive login: %v", err)
	case <-ctx.Done():
		t.Fatal("target did not receive login before deadline")
	}

	// Leave the real TCP target silent past the former five-second timeout,
	// checking status every two seconds just as the near-side config does.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for i := 0; i < 3; i++ {
		<-ticker.C
		near.StatusCheck()
		age := status.GlobalConnMonitorRef.GetLastAliveMs(near.BridgeName)
		if age < 0 || age > 1000 {
			t.Fatalf("probe %d failed while TCP response was pending (last success %d ms ago)", i+1, age)
		}
		t.Logf("probe %d: %d ms round trip while TCP response is pending", i+1,
			status.GlobalConnMonitorRef.GetPing(near.BridgeName))
	}
	close(releaseResponse)
	buf := make([]byte, len("login"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("TCP response did not survive the wait: %v", err)
	}
	if string(buf) != "login" {
		t.Fatalf("unexpected TCP response: %q", buf)
	}
	if err := <-targetResult; err != nil {
		t.Fatalf("target response: %v", err)
	}
}
