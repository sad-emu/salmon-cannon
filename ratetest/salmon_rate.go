package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"salmoncannon/config"
	"time"
)

const VERSION = "0.0.3"

const (
	rateProtocolMagic = "SRATE001"
	rateAckMagic      = "SRACK001"
	rateFrameData     = byte(1)
	rateFrameEnd      = byte(2)
	rateChunkSize     = 64 * 1024
	rateFrameHeader   = 13
	rateEndPayload    = 12
	rateAckHeader     = 39
	rateTestDuration  = 10 * time.Second
	rateWriteTimeout  = 5 * time.Second
	rateAckTimeout    = 30 * time.Second
)

var rateCRCTable = crc32.MakeTable(crc32.Castagnoli)

var LISTEN_PORT = 5555
var CONNECT_PORT = 5555

func main() {
	log.Printf("Salmon RateTest version %s starting...", VERSION)

	// Define flags first before any other operations
	mode := flag.String("mode", "test", "Mode: test, listen, pingpong")
	lp := flag.Int("lport", 5555, "Port to listen on")
	cp := flag.Int("cport", 5555, "Port to connect to")
	minMbps := flag.Float64("min-mbps", 0, "Minimum receiver-confirmed payload rate in Mbit/s (test mode; zero disables)")
	flag.Parse()

	LISTEN_PORT = *lp
	CONNECT_PORT = *cp

	log.Printf("Listening on port %d, connecting to port %d", LISTEN_PORT, CONNECT_PORT)

	cannonConfig, configErr := config.LoadConfig("scconfig.yml")
	log.Printf("Loaded %d salmon bridges", len(cannonConfig.Bridges))
	if configErr != nil {
		log.Fatalf("Failed to load config: %v", configErr)
	}

	tester := NewSalmonRateTester(cannonConfig)
	tester.minimumReceiverMbps = *minMbps
	switch *mode {
	case "test":
		log.Printf("Starting rate test...")
		if err := tester.Run(); err != nil {
			log.Fatalf("Rate test failed: %v", err)
		}
	case "listen":
		log.Printf("Starting rate listen...")
		tester.RunListen()
	case "pingpong":
		log.Printf("Starting pingpong mode...")
		tester.RunPingPong()
	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s\n", *mode)
		os.Exit(1)
	}
}

type SalmonRateTester struct {
	cfg                 *config.SalmonCannonConfig
	minimumReceiverMbps float64
}

func NewSalmonRateTester(cfg *config.SalmonCannonConfig) *SalmonRateTester {
	return &SalmonRateTester{cfg: cfg}
}

func (rt *SalmonRateTester) RunPingPong() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", LISTEN_PORT))
	if err != nil {
		log.Fatalf("PingPong responder failed to listen on %d: %v", LISTEN_PORT, err)
	}
	defer ln.Close()
	log.Printf("PingPong responder listening on :%d", LISTEN_PORT)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("Accept error: %v", err)
				continue
			}
			log.Printf("Accepted connection from %s", conn.RemoteAddr())
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						if err != io.EOF {
							log.Printf("Read error: %v", err)
						}
						return
					}
					// Echo back the data
					_, err = c.Write(buf[:n])
					if err != nil {
						log.Printf("Write error: %v", err)
						return
					}
					// wait for 3 seconds before next read
					time.Sleep(3 * time.Second)
				}
			}(conn)
		}
	}()

	for _, bridge := range rt.cfg.Bridges {
		if bridge.Connect {
			rt.testPingBridge(bridge)
		}
	}
}

func (rt *SalmonRateTester) Run() error {
	for _, bridge := range rt.cfg.Bridges {
		if bridge.Connect {
			if err := rt.testBridge(bridge); err != nil {
				return fmt.Errorf("bridge %s: %w", bridge.Name, err)
			}
		}
	}
	log.Println("RateTester finished all tests.")
	return nil
}

func (rt *SalmonRateTester) testPingBridge(b config.SalmonBridgeConfig) {
	addr := fmt.Sprintf("127.0.0.1:%d", b.SocksListenPort)

	for {
		log.Printf("Testing bridge %s at %s", b.Name, addr)

		// 1. Connect to local SOCKS proxy
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			log.Printf("Failed to connect to bridge %s: %v, retrying in 5 seconds...", b.Name, err)
			time.Sleep(5 * time.Second)
			continue
		}

		// SOCKS5 handshake (no authentication)
		handshake := []byte{0x05, 0x01, 0x00}
		if _, err := conn.Write(handshake); err != nil {
			log.Printf("SOCKS handshake write error: %v, retrying in 5 seconds...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		resp := make([]byte, 2)
		if _, err := io.ReadFull(conn, resp); err != nil {
			log.Printf("SOCKS handshake read error: %v, retrying in 5 seconds...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		if resp[0] != 0x05 || resp[1] != 0x00 {
			log.Printf("SOCKS handshake failed: %v, retrying in 5 seconds...", resp)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		// SOCKS5 CONNECT request to 127.0.0.1:CONNECT_PORT
		targetPort := CONNECT_PORT
		req := []byte{
			0x05,         // version
			0x01,         // CONNECT
			0x00,         // reserved
			0x01,         // IPv4
			127, 0, 0, 1, // 127.0.0.1
			byte(targetPort >> 8), byte(targetPort & 0xff), // port
		}
		if _, err := conn.Write(req); err != nil {
			log.Printf("SOCKS CONNECT write error: %v, retrying in 5 seconds...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		resp = make([]byte, 10)
		if _, err := io.ReadFull(conn, resp); err != nil {
			log.Printf("SOCKS CONNECT read error: %v, retrying in 5 seconds...", err)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		if resp[1] != 0x00 {
			log.Printf("SOCKS CONNECT failed: %v, retrying in 5 seconds...", resp)
			conn.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		log.Printf("Bridge %s: SOCKS CONNECT successful", b.Name)

		// 2. Ping test loop
		pingMessage := []byte("ping")
		buf := make([]byte, len(pingMessage))
		pingFailed := false

		for !pingFailed {
			start := time.Now()
			_, err := conn.Write(pingMessage)
			if err != nil {
				log.Printf("Ping write error: %v, reconnecting in 5 seconds...", err)
				pingFailed = true
				break
			}
			_, err = io.ReadFull(conn, buf)
			if err != nil {
				log.Printf("Ping read error: %v, reconnecting in 5 seconds...", err)
				pingFailed = true
				break
			}
			elapsed := time.Since(start)
			log.Printf("Bridge %s: Ping response received in %v", b.Name, elapsed)
			time.Sleep(2 * time.Second)
		}

		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

// RunListen receives framed rate-test payloads, validates their integrity, and
// acknowledges only after all payload bytes have reached the receiver.
func (rt *SalmonRateTester) RunListen() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", LISTEN_PORT))
	if err != nil {
		log.Fatalf("Responder failed to listen on %d: %v", LISTEN_PORT, err)
	}
	defer ln.Close()
	log.Printf("Responder listening on :%d", LISTEN_PORT)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		log.Printf("Accepted connection from %s", conn.RemoteAddr())
		go func(c net.Conn) {
			defer c.Close()
			if err := c.SetReadDeadline(time.Now().Add(rateTestDuration + rateAckTimeout)); err != nil {
				log.Printf("Failed to set rate test receive deadline for %s: %v", c.RemoteAddr(), err)
				return
			}
			result, err := receiveRateTest(c)
			if deadlineErr := c.SetWriteDeadline(time.Now().Add(rateAckTimeout)); deadlineErr != nil {
				log.Printf("Failed to set rate test acknowledgement deadline for %s: %v", c.RemoteAddr(), deadlineErr)
				return
			}
			if err != nil {
				log.Printf("Rate test receive failed from %s: %v", c.RemoteAddr(), err)
				if ackErr := writeRateAcknowledgement(c, result, err); ackErr != nil {
					log.Printf("Failed to send rate test failure acknowledgement to %s: %v", c.RemoteAddr(), ackErr)
				}
				return
			}
			if err := writeRateAcknowledgement(c, result, nil); err != nil {
				log.Printf("Failed to acknowledge rate test from %s: %v", c.RemoteAddr(), err)
				return
			}

			seconds := result.duration.Seconds()
			mbps := float64(result.bytes) * 8 / 1_000_000 / seconds
			log.Printf("Receiver verified %d payload bytes in %.2f seconds (%.2f Mbit/s, CRC32C %08x)", result.bytes, seconds, mbps, result.checksum)
		}(conn)
	}
}

type rateTestResult struct {
	bytes    uint64
	chunks   uint64
	duration time.Duration
	checksum uint32
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func writeRateFrameHeader(w io.Writer, frameType byte, sequence uint64, payloadLength uint32) error {
	var header [rateFrameHeader]byte
	header[0] = frameType
	binary.BigEndian.PutUint64(header[1:9], sequence)
	binary.BigEndian.PutUint32(header[9:13], payloadLength)
	return writeAll(w, header[:])
}

func readRateFrameHeader(r io.Reader) (byte, uint64, uint32, error) {
	var header [rateFrameHeader]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, 0, 0, err
	}
	return header[0], binary.BigEndian.Uint64(header[1:9]), binary.BigEndian.Uint32(header[9:13]), nil
}

func receiveRateTest(r io.Reader) (rateTestResult, error) {
	var result rateTestResult
	var magic [len(rateProtocolMagic)]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return result, fmt.Errorf("read protocol header: %w", err)
	}
	if string(magic[:]) != rateProtocolMagic {
		return result, fmt.Errorf("unexpected protocol header %q", magic[:])
	}

	payload := make([]byte, rateChunkSize)
	var started time.Time
	for {
		frameType, sequence, payloadLength, err := readRateFrameHeader(r)
		if err != nil {
			return result, fmt.Errorf("read frame %d header: %w", result.chunks, err)
		}

		switch frameType {
		case rateFrameData:
			if sequence != result.chunks {
				return result, fmt.Errorf("unexpected data sequence: got %d, want %d", sequence, result.chunks)
			}
			if payloadLength != rateChunkSize {
				return result, fmt.Errorf("invalid data payload length %d, want %d", payloadLength, rateChunkSize)
			}
			if started.IsZero() {
				started = time.Now()
			}
			chunk := payload[:payloadLength]
			if _, err := io.ReadFull(r, chunk); err != nil {
				return result, fmt.Errorf("read frame %d payload: %w", sequence, err)
			}
			result.checksum = crc32.Update(result.checksum, rateCRCTable, chunk)
			result.bytes += uint64(payloadLength)
			result.chunks++

		case rateFrameEnd:
			if started.IsZero() {
				return result, fmt.Errorf("received completion before any payload")
			}
			if sequence != result.chunks {
				return result, fmt.Errorf("unexpected completion sequence: got %d, want %d", sequence, result.chunks)
			}
			if payloadLength != rateEndPayload {
				return result, fmt.Errorf("invalid completion payload length %d", payloadLength)
			}
			var completion [rateEndPayload]byte
			if _, err := io.ReadFull(r, completion[:]); err != nil {
				return result, fmt.Errorf("read completion payload: %w", err)
			}
			result.duration = time.Since(started)
			expectedBytes := binary.BigEndian.Uint64(completion[0:8])
			expectedChecksum := binary.BigEndian.Uint32(completion[8:12])
			if expectedBytes != result.bytes {
				return result, fmt.Errorf("payload byte count mismatch: received %d, sender reported %d", result.bytes, expectedBytes)
			}
			if expectedChecksum != result.checksum {
				return result, fmt.Errorf("payload checksum mismatch: received %08x, sender reported %08x", result.checksum, expectedChecksum)
			}
			return result, nil

		default:
			return result, fmt.Errorf("unknown frame type %d", frameType)
		}
	}
}

func writeRateAcknowledgement(w io.Writer, result rateTestResult, resultErr error) error {
	message := []byte(nil)
	status := byte(0)
	if resultErr != nil {
		status = 1
		message = []byte(resultErr.Error())
		if len(message) > 65535 {
			message = message[:65535]
		}
	}

	var header [rateAckHeader]byte
	copy(header[0:8], rateAckMagic)
	header[8] = status
	binary.BigEndian.PutUint64(header[9:17], result.bytes)
	binary.BigEndian.PutUint64(header[17:25], uint64(result.duration))
	binary.BigEndian.PutUint32(header[25:29], result.checksum)
	binary.BigEndian.PutUint64(header[29:37], result.chunks)
	binary.BigEndian.PutUint16(header[37:39], uint16(len(message)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, message)
}

func readRateAcknowledgement(r io.Reader) (rateTestResult, error) {
	var result rateTestResult
	var header [rateAckHeader]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return result, err
	}
	if string(header[0:8]) != rateAckMagic {
		return result, fmt.Errorf("unexpected acknowledgement header %q", header[0:8])
	}

	result.bytes = binary.BigEndian.Uint64(header[9:17])
	result.duration = time.Duration(binary.BigEndian.Uint64(header[17:25]))
	result.checksum = binary.BigEndian.Uint32(header[25:29])
	result.chunks = binary.BigEndian.Uint64(header[29:37])
	messageLength := binary.BigEndian.Uint16(header[37:39])
	message := make([]byte, int(messageLength))
	if _, err := io.ReadFull(r, message); err != nil {
		return result, err
	}
	if header[8] != 0 {
		return result, fmt.Errorf("receiver rejected rate test: %s", message)
	}
	if len(message) != 0 {
		return result, fmt.Errorf("successful acknowledgement contained an unexpected message")
	}
	return result, nil
}

func benchmarkPayload(sequence uint64, payload []byte) {
	// Keep most of the payload stable to minimize generator overhead, while the
	// sequence prefix ensures reordered or duplicated chunks change the checksum.
	if sequence == 0 {
		for i := range payload {
			payload[i] = byte((i*31 + 17) % 251)
		}
	}
	binary.BigEndian.PutUint64(payload[:8], sequence)
}

func (rt *SalmonRateTester) testBridge(b config.SalmonBridgeConfig) error {
	addr := fmt.Sprintf("127.0.0.1:%d", b.SocksListenPort)
	log.Printf("Testing bridge %s at %s", b.Name, addr)

	// 1. Connect to local SOCKS proxy
	conn, err := net.DialTimeout("tcp", addr, rateAckTimeout)
	if err != nil {
		return fmt.Errorf("connect to local SOCKS proxy: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(rateAckTimeout)); err != nil {
		return fmt.Errorf("set SOCKS handshake deadline: %w", err)
	}

	// SOCKS5 handshake (no authentication)
	handshake := []byte{0x05, 0x01, 0x00}
	if err := writeAll(conn, handshake); err != nil {
		return fmt.Errorf("write SOCKS handshake: %w", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("read SOCKS handshake: %w", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		return fmt.Errorf("SOCKS handshake failed: %v", resp)
	}

	// SOCKS5 CONNECT request to 127.0.0.1:5555
	targetPort := CONNECT_PORT
	req := []byte{
		0x05,         // version
		0x01,         // CONNECT
		0x00,         // reserved
		0x01,         // IPv4
		127, 0, 0, 1, // 127.0.0.1
		byte(targetPort >> 8), byte(targetPort & 0xff), // port
	}
	if err := writeAll(conn, req); err != nil {
		return fmt.Errorf("write SOCKS CONNECT request: %w", err)
	}
	resp = make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("read SOCKS CONNECT response: %w", err)
	}
	if resp[1] != 0x00 {
		return fmt.Errorf("SOCKS CONNECT failed: %v", resp)
	}

	log.Printf("Bridge %s: SOCKS CONNECT successful", b.Name)
	log.Printf("Bridge %s: Starting %.0f second receiver-confirmed rate test...", b.Name, rateTestDuration.Seconds())

	if err := conn.SetWriteDeadline(time.Now().Add(rateWriteTimeout)); err != nil {
		return fmt.Errorf("set protocol header write deadline: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear SOCKS read deadline: %w", err)
	}
	if err := writeAll(conn, []byte(rateProtocolMagic)); err != nil {
		return fmt.Errorf("write rate protocol header: %w", err)
	}

	end := time.Now().Add(rateTestDuration)
	payload := make([]byte, rateChunkSize)
	var sent rateTestResult
	sendStart := time.Now()
	for time.Now().Before(end) {
		benchmarkPayload(sent.chunks, payload)
		if err := conn.SetWriteDeadline(time.Now().Add(rateWriteTimeout)); err != nil {
			return fmt.Errorf("set payload write deadline: %w", err)
		}
		if err := writeRateFrameHeader(conn, rateFrameData, sent.chunks, uint32(len(payload))); err != nil {
			return fmt.Errorf("write frame %d header: %w", sent.chunks, err)
		}
		if err := writeAll(conn, payload); err != nil {
			return fmt.Errorf("write frame %d payload: %w", sent.chunks, err)
		}
		sent.checksum = crc32.Update(sent.checksum, rateCRCTable, payload)
		sent.bytes += uint64(len(payload))
		sent.chunks++
	}

	var completion [rateEndPayload]byte
	binary.BigEndian.PutUint64(completion[0:8], sent.bytes)
	binary.BigEndian.PutUint32(completion[8:12], sent.checksum)
	if err := conn.SetWriteDeadline(time.Now().Add(rateWriteTimeout)); err != nil {
		return fmt.Errorf("set completion write deadline: %w", err)
	}
	if err := writeRateFrameHeader(conn, rateFrameEnd, sent.chunks, rateEndPayload); err != nil {
		return fmt.Errorf("write completion header: %w", err)
	}
	if err := writeAll(conn, completion[:]); err != nil {
		return fmt.Errorf("write completion payload: %w", err)
	}
	sent.duration = time.Since(sendStart)

	// A successful acknowledgement means the completion marker and every data
	// frame ahead of it were drained through the tunnel and verified remotely.
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear write deadline: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(rateAckTimeout)); err != nil {
		return fmt.Errorf("set acknowledgement deadline: %w", err)
	}
	confirmed, err := readRateAcknowledgement(conn)
	if err != nil {
		return fmt.Errorf("read completion acknowledgement: %w", err)
	}
	if confirmed.bytes != sent.bytes || confirmed.chunks != sent.chunks || confirmed.checksum != sent.checksum {
		return fmt.Errorf("receiver acknowledgement mismatch: sent %d bytes/%d chunks/%08x, confirmed %d bytes/%d chunks/%08x", sent.bytes, sent.chunks, sent.checksum, confirmed.bytes, confirmed.chunks, confirmed.checksum)
	}
	if confirmed.duration <= 0 {
		return fmt.Errorf("receiver reported invalid duration %s", confirmed.duration)
	}

	receiverSeconds := confirmed.duration.Seconds()
	senderSeconds := sent.duration.Seconds()
	receiverMbps := float64(confirmed.bytes) * 8 / 1_000_000 / receiverSeconds
	senderMbps := float64(sent.bytes) * 8 / 1_000_000 / senderSeconds
	log.Printf("Bridge %s: Receiver confirmed and integrity-verified %d payload bytes in %.2f seconds (%.2f Mbit/s, %.4f Gbit/s, CRC32C %08x); sender accepted them in %.2f seconds (%.2f Mbit/s)", b.Name, confirmed.bytes, receiverSeconds, receiverMbps, receiverMbps/1000, confirmed.checksum, senderSeconds, senderMbps)
	if rt.minimumReceiverMbps > 0 && receiverMbps < rt.minimumReceiverMbps {
		return fmt.Errorf("receiver-confirmed rate %.2f Mbit/s is below required %.2f Mbit/s", receiverMbps, rt.minimumReceiverMbps)
	}
	return nil
}
