package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"salmoncannon/bridge"
	"salmoncannon/config"
	"salmoncannon/limiter"
	"salmoncannon/socks"
	"salmoncannon/status"
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

	sl := limiter.NewSharedLimiter(int64(config.TotalBandwidthLimit))
	status.GlobalConnMonitorRef.RegisterLimiter(config.Name, sl)

	qcfg := &quic.Config{
		MaxIdleTimeout:                 config.IdleTimeout.Duration(),
		InitialStreamReceiveWindow:     uint64(1024 * 1024 * 50),
		MaxStreamReceiveWindow:         uint64(config.MaxRecieveBufferSize),
		InitialConnectionReceiveWindow: uint64(1024 * 1024 * 25),
		MaxConnectionReceiveWindow:     uint64(config.MaxRecieveBufferSize),
		InitialPacketSize:              uint16(config.InitialPacketSize),
		MaxIncomingStreams:             socks.MaxConnections,
		MaxIncomingUniStreams:          socks.MaxConnections,
		EnableDatagrams:                false,
	}

	farListenAddr := fmt.Sprintf(":%d", config.NearPort)
	log.Printf("FAR: Listen address for bridge %s is '%s' (len=%d)\n", config.Name, farListenAddr, len(farListenAddr))

	var aesKey []byte = nil
	var err error = nil
	if config.SharedSecret != "" {
		aesKey, err = utils.DeriveAesKeyFromPassphrase(config.Name, config.SharedSecret)
		if err != nil {
			log.Printf("NEAR: Bridge %s Failed to derive AES key: %v", config.Name, err)
			return nil, err
		}
		log.Printf("NEAR: Bridge %s using encryption key", config.Name)
	}

	farBridge := bridge.NewSalmonBridge(config.Name, config.FarIp, config.NearPort,
		tlscfg, qcfg, sl, config.Connect, config.InterfaceName, config.AllowedOutAddresses, aesKey)

	far := &SalmonFar{
		farBridge: farBridge,
	}

	return far, nil
}
