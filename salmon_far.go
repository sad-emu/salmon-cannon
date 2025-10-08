package main

import (
	"crypto/tls"
	"fmt"
	"salmoncannon/bridge"
	"salmoncannon/config"

	quic "github.com/quic-go/quic-go"
)

type SalmonFar struct {
	farBridge *bridge.SalmonBridge
}

// TODO - for bridge types it should start listeners for them
// near should be able to make requests through them

func NewSalmonFar(config *config.SalmonBridgeConfig) (*SalmonFar, error) {

	tlscfg := &tls.Config{
		Certificates: []tls.Certificate{generateSelfSignedCert()},
		NextProtos:   []string{config.Name},
	}

	// TODO is this bits or bytes?
	sl := bridge.NewSharedLimiter(int64(config.TotalBandwidthLimit))

	qcfg := &quic.Config{
		// Tune as needed (see near side).
	}

	farListenAddr := fmt.Sprintf(":%d", config.NearPort)
	fmt.Printf("farListenAddr: '%s' (len=%d)\n", farListenAddr, len(farListenAddr))

	farBridge := bridge.NewSalmonBridge("", config.NearPort, tlscfg, qcfg, sl, config.Connect)

	far := &SalmonFar{
		farBridge: farBridge,
	}

	return far, nil
}
