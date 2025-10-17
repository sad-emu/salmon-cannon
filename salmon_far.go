package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"salmoncannon/bridge"
	"salmoncannon/config"
	"salmoncannon/utils"

	quic "github.com/quic-go/quic-go"
)

type SalmonFar struct {
	farBridge *bridge.SalmonBridge
}

func NewSalmonFar(config *config.SalmonBridgeConfig) (*SalmonFar, error) {

	tlscfg := &tls.Config{
		Certificates: []tls.Certificate{utils.GenerateSelfSignedCert()},
		NextProtos:   []string{config.Name},
	}

	// TODO is this bits or bytes?
	sl := bridge.NewSharedLimiter(int64(config.TotalBandwidthLimit))

	qcfg := &quic.Config{
		MaxIdleTimeout:                 config.IdleTimeout.Duration(),
		InitialStreamReceiveWindow:     uint64(1024 * 1024 * 50 * 8),
		MaxStreamReceiveWindow:         uint64(1024 * 1024 * 400 * 8),
		InitialConnectionReceiveWindow: uint64(1024 * 1024 * 50 * 8),
		MaxConnectionReceiveWindow:     uint64(1024 * 1024 * 400 * 8),
		InitialPacketSize:              uint16(config.InitialPacketSize),
	}

	farListenAddr := fmt.Sprintf(":%d", config.NearPort)
	log.Printf("FAR: Listen address for bridge %s is '%s' (len=%d)\n", config.Name, farListenAddr, len(farListenAddr))

	farBridge := bridge.NewSalmonBridge(config.Name, "", config.NearPort, tlscfg, qcfg, sl, config.Connect)

	far := &SalmonFar{
		farBridge: farBridge,
	}

	return far, nil
}
