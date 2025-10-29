package main

import (
	"log"
	"net"
	"os"
	"salmoncannon/api"
	"salmoncannon/config"
	"strconv"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

const VERSION = "0.0.6"

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

	// Setup API server if configured
	if cannonConfig.ApiConfig != nil {
		apiListenAddr := net.JoinHostPort(cannonConfig.ApiConfig.Hostname, strconv.Itoa(cannonConfig.ApiConfig.Port))
		apiServer := api.NewServer(cannonConfig, apiListenAddr)
		err := apiServer.Start()
		if err != nil {
			log.Fatalf("API Server: failed to start API server: %v", err)
		}
		log.Printf("API Server: HTTP API server started on %s", apiListenAddr)
	}

	var wg sync.WaitGroup
	bridgeRegistry := make(map[string]*SalmonNear) // Store references to near bridges

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
				bridgeRegistry[cfg.Name] = near // Store reference
				if cfg.HttpListenPort > 0 {
					log.Printf("NEAR: HTTP proxy enabled on port %d", cfg.HttpListenPort)
					go initHTTPNear(cfg, near)
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

	if cannonConfig.SocksRedirectConfig != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := runSocksRedirector(cannonConfig.SocksRedirectConfig, &bridgeRegistry)
			if err != nil {
				log.Fatalf("SOCKS Redirector: %v", err)
			}
		}()
	}

	wg.Wait()
	log.Printf("Salmon cannon exiting.")
}
