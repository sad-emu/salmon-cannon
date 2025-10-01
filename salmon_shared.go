package main

const (
	HeaderRequestBridges   byte = 0x01
	HeaderResponseBridges  byte = 0x02
	HeaderRequestUseBridge byte = 0x03
	HeaderMeta             byte = 0x04
	HeaderData             byte = 0x05
)

type BridgeType byte

const (
	BridgeNone BridgeType = 0x00
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
