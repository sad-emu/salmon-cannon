package main

const (
	socksVersion5         = 0x05
	socksCmdConnect       = 0x01
	socksCmdUDPAssociate  = 0x03
	socksAddrTypeIPv4     = 0x01
	socksAddrTypeDomain   = 0x03
	socksAddrTypeIPv6     = 0x04
	socksReplySucceeded   = 0x00
	socksReplyGeneralFail = 0x01
	socksReserved         = 0x00
	maxMethods            = 255
	handshakeMinLen       = 2
	requestMinLen         = 7
	ipv4Len               = 4
	ipv6Len               = 16
	portLen               = 2
)

var (
	handshakeNoAuth = []byte{socksVersion5, 0x00}
	replySuccess    = []byte{socksVersion5, socksReplySucceeded, socksReserved, socksAddrTypeIPv4, 0, 0, 0, 0, 0, 0}
	replyFail       = []byte{socksVersion5, socksReplyGeneralFail, socksReserved, socksAddrTypeIPv4, 0, 0, 0, 0, 0, 0}
)
