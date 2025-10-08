# The Salmon Cannon
## NOT READY FOR USE
SOCKS5 auth & bridge authentication is TODO. Do not use this project yet.
## Description
SalmonCannon (sc) is a SOCKS5 proxy that tunnels TCP traffic between a 'near' node and a 'far' node using the QUIC protocol. It is designed for efficient, and reliable TCP forwarding in challenging network environments.

[client(s)]--tcp-->[Near sc]--QUIC-->[Far sc]--TCP-->[server(s)]

## Features
- **SOCKS5 Proxy:** Accepts TCP connections from SOCKS5 clients.
- **QUIC Tunneling:** Transports TCP streams over QUIC between near and far nodes.
- **Configurable:** Flexible YAML configuration for multiple bridges and advanced options.
- **TCP Only:** UDP is not supported.

## Architecture
- **Near Node:** Listens for SOCKS5 connections and forwards them to the far node over QUIC.
- **Far Node:** Accepts QUIC connections and proxies TCP traffic to the destination.

## Quick Start

To run:
1. Ensure your UDP buffers are configred on the host (see common errors below)
2. Place the `scconfig.yml` file in the same directory as the `sc` binary.
3. Run `./sc`.

### 1. Minimal Example

#### Near Node (Connect Mode)
```yaml
salmonbridges:
	- SBName: "salmon-bridge-1-connect-minimal"
		SBSocksListenPort: 1080
		SBConnect: true
		SBNearPort: 55001
		SBFarPort: 55001
		SBFarIp: "far-ip-here"
```

#### Far Node (Accept Mode)
```yaml
salmonbridges:
	- SBName: "salmon-bridge-1-accept-minimal"
		SBConnect: false
		SBNearPort: 55001
		SBFarPort: 55001
```

### 2. Full Example

#### Near Node (Connect Mode)
```yaml
salmonbridges:
	- SBName: "salmon-bridge-1-connect-full"
		SBSocksListenPort: 1080
		SBConnect: true
		SBNearPort: 55001
		SBFarPort: 55001
		SBFarIp: "far-ip-here"
		SBSocksListenAddress: "127.0.0.1"
		SBIdleTimeout: 1m
		SBInitialPacketSize: 1350
		SBRecieveWindow: 10M
		SBMaxRecieveWindow: 40M
		SBTotalBandwidthLimit: 100M
```

#### Far Node (Accept Mode)
```yaml
salmonbridges:
	- SBName: "salmon-bridge-2-accept-full"
		SBSocksListenPort: 1081
		SBConnect: false
		SBNearPort: 55002
		SBFarPort: 55002
		SBIdleTimeout: 1m
		SBInitialPacketSize: 1350
		SBRecieveWindow: 10M
		SBMaxRecieveWindow: 40M
		SBTotalBandwidthLimit: 100M
```

## Usage
1. **Configure** your near and far nodes using YAML as shown above.
2. **Start the near node** in connect mode to initiate QUIC connections to the far node.
3. **Start the far node** in accept mode to receive QUIC connections and proxy TCP traffic.
4. **Point your SOCKS5 client** (e.g., browser, curl, proxychains) to the near node's listen address and port.

## Configuration Reference
- `SBName`: Bridge name (string)
- `SBSocksListenPort`: SOCKS5 listen port (int)
- `SBSocksListenAddress`: SOCKS5 listen address (string, optional)
- `SBConnect`: If true, acts as near node (initiates QUIC connection)
- `SBNearPort`: QUIC port on near node (int)
- `SBFarPort`: QUIC port on far node (int)
- `SBFarIp`: Far node IP address (string, required for connect mode)
- `SBIdleTimeout`: Idle timeout (duration e.g. 10s or 2m, optional)
- `SBInitialPacketSize`: QUIC initial packet size (int e.g. 50M, optional)
- `SBRecieveWindow`: QUIC receive window (size e.g. 50M, optional)
- `SBMaxRecieveWindow`: QUIC max receive window (size e.g. 50M, optional)
- `SBTotalBandwidthLimit`: Bandwidth limit (size e.g. 100M or 1G, optional)

## Common Issues
### UDP Init Error  
failed to sufficiently increase receive buffer size 
(was: 208 kiB, wanted: 7168 kiB, got: 416 kiB). 
See https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes for details.

#### Fix

Add these lines to /etc/sysctl.conf:

Code
net.core.wmem_max=838860800
net.core.wmem_default=83886080
net.core.rmem_max=838860800
net.core.rmem_default=83886080

Then apply with

sudo sysctl -p

## License
GPLv3
