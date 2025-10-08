package main

import (
	"log"
	"net"
	"salmoncannon/config"
	"strconv"
	"sync"
)

func initNear(cfg *config.SalmonBridgeConfig, near *SalmonNear) {
	log.Printf("Initializing near side SOCKS listener for bridge %s", cfg.Name)
	listenAddr := cfg.SocksListenAddress + ":" + strconv.Itoa(cfg.SocksListenPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}
	log.Printf("NEAR SOCKS proxy listening on %s", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		go near.HandleRequest(conn)
	}
}

func main() {
	cannonConfig, configErr := config.LoadConfig("scconfig.yml")
	log.Printf("Loaded %d salmon bridges", len(cannonConfig.Bridges))
	if configErr != nil {
		log.Fatalf("Failed to load config: %v", configErr)
	}

	var wg sync.WaitGroup

	for cb := range cannonConfig.Bridges {
		wg.Add(1)
		bridgeConfig := &cannonConfig.Bridges[cb] // Avoid closure capture bug
		log.Printf("Setting up salmon bridge %s: %+v", bridgeConfig.Name, bridgeConfig)
		go func(cfg *config.SalmonBridgeConfig) {
			defer wg.Done()
			if cfg.Connect {
				log.Printf("Starting bridge %s in Near mode...", cfg.Name)
				near, err := NewSalmonNear(cfg)
				if err != nil {
					log.Fatalf("Failed to setup salmon near: %v", err)
				}
				initNear(cfg, near)
			} else {
				log.Printf("Starting bridge %s in Far mode...", cfg.Name)
				far, err := NewSalmonFar(cfg)
				if err != nil {
					log.Fatalf("Failed to start SalmonFar: %v", err)
				}
				go far.farBridge.NewFarListen()

				select {}
			}
		}(bridgeConfig)
	}

	wg.Wait()
	log.Printf("Salmon cannon exiting.")
}
