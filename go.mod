module salmoncannon

go 1.24.7

require (
	github.com/juju/ratelimit v1.0.2
	github.com/quic-go/quic-go v0.55.1-0.20251017053007-f07d6939d007
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kr/text v0.2.0 // indirect
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
)

replace github.com/quic-go/quic-go => /home/emu/projects/quic-go
