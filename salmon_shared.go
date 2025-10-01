package main

const (
	HeaderRequestBridges     byte = 0x01
	HeaderRequestBridgesResp byte = 0x02
	HeaderData               byte = 0x03
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
