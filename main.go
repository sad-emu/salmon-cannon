package main

import (
	"log"
	"net"
	"os"
	"salmoncannon/config"
	"strconv"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

const VERSION = "0.0.2"

func initNear(cfg *config.SalmonBridgeConfig, near *SalmonNear) {
	log.Printf("NEAR: Initializing near side SOCKS listener for bridge %s", cfg.Name)
	listenAddr := cfg.SocksListenAddress + ":" + strconv.Itoa(cfg.SocksListenPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("NEAR: Failed to listen on %s: %v", listenAddr, err)
	}
	log.Printf("NEAR: SOCKS proxy listening on %s", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("NEAR: Local SOCKS TCP accept error: %v", err)
			continue
		}
		go near.HandleRequest(conn)
	}
}

func main() {
	log.Printf("Salmon Cannon version %s starting...", VERSION)
	cannonConfig, configErr := config.LoadConfig("scconfig.yml")
	log.Printf("Loaded %d salmon bridges", len(cannonConfig.Bridges))

	// If we cannot even read the config, log to a crash file.
	if configErr != nil {
		f, err := os.OpenFile("crash.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString("Failed to load config: " + configErr.Error() + "\n")
			f.Close()
		}
		log.Fatalf("Failed to load config: %v", configErr)
	}

	if len(cannonConfig.GlobalLog.Filename) != 0 {
		log.SetOutput(&lumberjack.Logger{
			Filename:   cannonConfig.GlobalLog.Filename,
			MaxSize:    cannonConfig.GlobalLog.MaxSize, // megabytes
			MaxBackups: cannonConfig.GlobalLog.MaxBackups,
			MaxAge:     cannonConfig.GlobalLog.MaxAge, // days
			Compress:   true,                          // optional
		})
		log.Printf("Salmon Cannon version %s starting...", VERSION)
		log.Printf("Loaded %d salmon bridges", len(cannonConfig.Bridges))
	}

	var wg sync.WaitGroup

	for cb := range cannonConfig.Bridges {
		wg.Add(1)
		bridgeConfig := &cannonConfig.Bridges[cb] // Avoid closure capture bug
		log.Printf("Setting up salmon bridge %s: %+v", bridgeConfig.Name, bridgeConfig)
		go func(cfg *config.SalmonBridgeConfig) {
			defer wg.Done()
			if cfg.Connect {
				log.Printf("NEAR: Starting bridge %s in Near mode...", cfg.Name)
				near, err := NewSalmonNear(cfg)
				if err != nil {
					log.Fatalf("NEAR: Failed to setup SalmonNear: %v", err)
				}
				initNear(cfg, near)
			} else {
				log.Printf("FAR: Starting bridge %s in Far mode...", cfg.Name)
				far, err := NewSalmonFar(cfg)
				if err != nil {
					log.Fatalf("FAR: Failed to setup SalmonFar: %v", err)
				}
				err = far.farBridge.NewFarListen()
				if err != nil {
					log.Fatalf("FAR: Failed to start SalmonFar: %v", err)
				}

				select {}
			}
		}(bridgeConfig)
	}

	wg.Wait()
	log.Printf("Salmon cannon exiting.")
}
