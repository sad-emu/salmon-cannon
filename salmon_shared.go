package main

const (
	HeaderRequestBridges   byte = 0x01
	HeaderResponseBridges  byte = 0x02
	HeaderRequestUseBridge byte = 0x03
	HeaderData             byte = 0x04
)

type BridgeType byte

const (
	BridgeTCP  BridgeType = 0x01
	BridgeQUIC BridgeType = 0x02
	// Add more bridge types as needed
)

func (b BridgeType) String() string {
	switch b {
	case BridgeTCP:
		return "tcp-bridge"
	default:
		return "unknown"
	}
}
