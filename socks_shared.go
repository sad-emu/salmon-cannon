package main

const (
	socksVersion5     = 0x05
	socksAuthNoAuth   = 0x00
	socksAuthUserPass = 0x02

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

	maxConnections = 2000
)

var (
	handshakeNoAuth       = []byte{socksVersion5, socksAuthNoAuth}
	handshakeUserPass     = []byte{socksVersion5, socksAuthUserPass}
	handshakeNoAcceptable = []byte{socksVersion5, 0xff}
	authReplySuccess      = []byte{0x01, 0x00}
	authReplyFail         = []byte{0x01, 0x01}
	replySuccess          = []byte{socksVersion5, socksReplySucceeded, socksReserved, socksAddrTypeIPv4, 0, 0, 0, 0, 0, 0}
	replyFail             = []byte{socksVersion5, socksReplyGeneralFail, socksReserved, socksAddrTypeIPv4, 0, 0, 0, 0, 0, 0}
)
