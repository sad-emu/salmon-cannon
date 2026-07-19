module salmoncannon

go 1.25.0

require (
	github.com/juju/ratelimit v1.0.2
	github.com/sad-emu/anadromous v0.0.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/tredeske/u v0.0.0-20250421110454-ea06120e3caa // indirect
	golang.org/x/sys v0.45.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

replace github.com/sad-emu/anadromous => ../anadromous

replace github.com/tredeske/u => github.com/sad-emu/u v0.0.0-20250421110454-ea06120e3caa
