package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"salmoncannon/config"
	"time"
)

const VERSION = "0.0.3"

var LISTEN_PORT = 5555
var CONNECT_PORT = 5555

func main() {
	log.Printf("Salmon RateTest version %s starting...", VERSION)

	// Define flags first before any other operations
	mode := flag.String("mode", "test", "Mode: test, listen, pingpong")
	lp := flag.Int("lport", 5555, "Port to listen on")
	cp := flag.Int("cport", 5555, "Port to connect to")
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
	switch *mode {
	case "test":
		log.Printf("Starting rate test...")
		tester.Run()
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
	cfg *config.SalmonCannonConfig
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

func (rt *SalmonRateTester) Run() {
	for _, bridge := range rt.cfg.Bridges {
		if bridge.Connect {
			rt.testBridge(bridge)
		}
	}
	log.Println("RateTester finished all tests.")
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

// RunListen listens on TCP port 5555 and responds to pings indefinitely
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
			buf := make([]byte, 4096)
			for {
				_, err := c.Read(buf)
				if err != nil {
					if err != io.EOF {
						log.Printf("Read error: %v", err)
					}
					return
				}
				// accept and drop data
			}
		}(conn)
	}
}

func (rt *SalmonRateTester) testBridge(b config.SalmonBridgeConfig) {
	addr := fmt.Sprintf("127.0.0.1:%d", b.SocksListenPort)
	log.Printf("Testing bridge %s at %s", b.Name, addr)

	// 1. Connect to local SOCKS proxy
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Printf("Failed to connect to bridge %s: %v", b.Name, err)
		return
	}
	defer conn.Close()

	// SOCKS5 handshake (no authentication)
	handshake := []byte{0x05, 0x01, 0x00}
	if _, err := conn.Write(handshake); err != nil {
		log.Printf("SOCKS handshake write error: %v", err)
		return
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		log.Printf("SOCKS handshake read error: %v", err)
		return
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		log.Printf("SOCKS handshake failed: %v", resp)
		return
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
	if _, err := conn.Write(req); err != nil {
		log.Printf("SOCKS CONNECT write error: %v", err)
		return
	}
	resp = make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		log.Printf("SOCKS CONNECT read error: %v", err)
		return
	}
	if resp[1] != 0x00 {
		log.Printf("SOCKS CONNECT failed: %v", resp)
		return
	}

	timeSec := 10
	log.Printf("Bridge %s: SOCKS CONNECT successful", b.Name)
	log.Printf("Bridge %s: Starting %d sec test...", b.Name, timeSec)

	// 2. nSec ratetest: send garbage
	end := time.Now().Add(time.Duration(timeSec) * time.Second)
	total := 0
	buf := make([]byte, 4096)
	rand.Read(buf)

	start := time.Now()
	for time.Now().Before(end) {
		// limit blocking per write so extreme netem doesn't stall the loop for many seconds
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Write(buf)
		if err != nil {
			// write timeout or other error; log and continue until the end time
			log.Printf("Write error during ratetest: %v", err)
			// small sleep to avoid tight error loop if the connection is blocked/broken
			time.Sleep(50 * time.Millisecond)
			continue
		} else {
			total += n
		}
	}
	elapsed := time.Since(start)
	secs := elapsed.Seconds()
	if secs <= 0 {
		secs = float64(timeSec)
	}

	kbps := float64(total) * 8 / 1024 / secs
	mbps := float64(total) * 8 / (1024 * 1024) / secs
	gbps := float64(total) * 8 / (1024 * 1024 * 1024) / secs
	log.Printf("Bridge %s: Sent %d bytes in %.2f secs \n -   %.2f kbps\n -   %.2f mbps\n -   %.4f gbps", b.Name, total, secs, kbps, mbps, gbps)
}
