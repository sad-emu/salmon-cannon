package main

import (
	"fmt"
	"log"
	"salmoncannon/bridge"
	"salmoncannon/config"
	"salmoncannon/connections"
	"salmoncannon/limiter"
	"salmoncannon/socks"
	"salmoncannon/status"
)

type SalmonFar struct {
	farBridge *bridge.SalmonBridge
}

func NewSalmonFar(config *config.SalmonBridgeConfig) (*SalmonFar, error) {

	sl := limiter.NewSharedLimiter(int64(config.TotalBandwidthLimit))
	status.GlobalConnMonitorRef.RegisterLimiter(config.Name, sl)

	netcfg := connections.BridgeNetConfig{
		IdleTimeout:      config.IdleTimeout.Duration(),
		StreamRecvBuffer: int(config.MaxRecieveBufferSize),
		PacketSize:       config.InitialPacketSize,
		MaxStreams:       socks.MaxConnections,
	}

	farListenAddr := fmt.Sprintf(":%d", config.NearPort)
	log.Printf("FAR: Listen address for bridge %s is '%s' (len=%d)\n", config.Name, farListenAddr, len(farListenAddr))

	farBridge := bridge.NewSalmonBridge(config.Name, config.FarIp, config.NearPort,
		netcfg, sl, config.Connect, config.InterfaceName, config.AllowedOutAddresses, config.SharedSecret)

	far := &SalmonFar{
		farBridge: farBridge,
	}

	return far, nil
}
