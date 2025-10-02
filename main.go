package main

import (
	"flag"
	"log"
	"net"
)

func main() {
	// Parse command-line flags '-n for near mode, -f for far mode'
	mode := flag.String("mode", "near", "Mode to run: 'near' or 'far'")
	listenAddr := flag.String("listen", ":1080", "Listen address (default :1080)")
	flag.Parse()

	if *mode == "near" {
		log.Println("Starting in Near mode...")
		near, err := NewSalmonNear("127.0.0.1", 1099, []BridgeType{BridgeQUIC})
		if err == nil {
			defer near.conn.Close()
		}
		ln, err := net.Listen("tcp", *listenAddr)
		if err != nil {
			log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
		}
		log.Printf("NEAR SOCKS proxy listening on %s", *listenAddr)
		connected := false
		for {
			if !connected {
				err := near.Connect()
				if err != nil {
					log.Printf("Failed to connect to far: %v", err)
				} else {
					print("Near created bridge to far")
					connected = true
				}
			}
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("Accept error: %v", err)
				continue
			}
			go near.HandleRequest(conn)
		}
	} else if *mode == "far" {
		log.Println("Starting in Far mode...")
		far, err := NewSalmonFar(1099, []BridgeType{BridgeTCP})
		if err != nil {
			log.Fatalf("Failed to start SalmonFar: %v", err)
		}
		defer far.ln.Close()
		select {}
	}

}

// func main() {
// 	listenAddr := flag.String("listen", ":1080", "Listen address (default :1080)")
// 	flag.Parse()

// 	ln, err := net.Listen("tcp", *listenAddr)
// 	if err != nil {
// 		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
// 	}
// 	log.Printf("SOCKS proxy listening on %s", *listenAddr)

// 	for {
// 		conn, err := ln.Accept()
// 		if err != nil {
// 			log.Printf("Accept error: %v", err)
// 			continue
// 		}
// 		go handleConnection(conn)
// 	}
// }
