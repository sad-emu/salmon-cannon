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

const VERSION = "0.0.1"

func main() {
	log.Printf("Salmon RateTest version %s starting...", VERSION)
	cannonConfig, configErr := config.LoadConfig("scconfig.yml")
	log.Printf("Loaded %d salmon bridges", len(cannonConfig.Bridges))
	if configErr != nil {
		log.Fatalf("Failed to load config: %v", configErr)
	}
	mode := flag.String("mode", "test", "Mode: test or listen")
	flag.Parse()

	tester := NewSalmonRateTester(cannonConfig)
	switch *mode {
	case "test":
		log.Printf("Starting rate test...")
		tester.Run()
	case "listen":
		log.Printf("Starting rate listen...")
		tester.RunListen()
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

func (rt *SalmonRateTester) Run() {
	for _, bridge := range rt.cfg.Bridges {
		if bridge.Connect {
			rt.testBridge(bridge)
		}
	}
	log.Println("RateTester finished all tests.")
}

// RunListen listens on TCP port 5555 and responds to pings indefinitely
func (rt *SalmonRateTester) RunListen() {
	ln, err := net.Listen("tcp", ":5555")
	if err != nil {
		log.Fatalf("Responder failed to listen on 5555: %v", err)
	}
	defer ln.Close()
	log.Printf("Responder listening on :5555")
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
				// if string(buf[:n]) == "ping\n" {
				// 	c.Write([]byte("pong\n"))
				// }
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
	// Send: version 5, 1 method, noauth (0x00)
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
	targetPort := 5555
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
	for time.Now().Before(end) {
		n, err := conn.Write(buf)
		if err != nil {
			log.Printf("Write error during ratetest: %v", err)
			break
		}
		total += n
	}

	kbps := float64(total) * 8 / 1024 / float64(timeSec)
	mbps := float64(total) * 8 / (1024 * 1024) / float64(timeSec)
	gbps := float64(total) * 8 / (1024 * 1024 * 1024) / float64(timeSec)
	log.Printf("Bridge %s: Sent %d bytes in %d secs \n -   %.2f kbps\n -   %.2f mbps\n -   %.4f gbps", b.Name, total, timeSec, kbps, mbps, gbps)

	// // 3. 10 pings: send 'ping', expect 'pong', measure latency
	// var latencies []time.Duration
	// for i := 0; i < 10; i++ {
	// 	start := time.Now()
	// 	if _, err := conn.Write([]byte("ping\n")); err != nil {
	// 		log.Printf("Ping write error: %v", err)
	// 		break
	// 	}
	// 	reply := make([]byte, 16)
	// 	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	// 	n, err := conn.Read(reply)
	// 	if err != nil {
	// 		log.Printf("Ping read error: %v", err)
	// 		break
	// 	}
	// 	if string(reply[:n]) != "pong\n" {
	// 		log.Printf("Unexpected ping reply: %q", reply[:n])
	// 		break
	// 	}
	// 	latencies = append(latencies, time.Since(start))
	// 	time.Sleep(100 * time.Millisecond)
	// }
	// if len(latencies) > 0 {
	// 	var sum time.Duration
	// 	for _, l := range latencies {
	// 		sum += l
	// 	}
	// 	avg := sum / time.Duration(len(latencies))
	// 	log.Printf("Bridge %s: Average ping latency: %v", b.Name, avg)
	// }
}
