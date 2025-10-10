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

#### Logging
If no logging config is provided the sc binary with log to stdout.
```yaml
globallog:
  Filename: "sc.log"
  MaxSize: 20        # megabytes
  MaxBackups: 5
  MaxAge: 28         # days
  Compress: false

```


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
    SBHttpListenPort: 8080
    SBConnect: true
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
4. **Point your SOCKS5 client** (e.g., browser, curl, proxychains) to the near node's listen address and port. curl --socks5-hostname 127.0.0.1:1080 https://www.google.com/

## Configuration Reference
- `SBName`: Bridge name (string)
- `SBSocksListenPort`: SOCKS5 listen port (int)
- `SBSocksListenAddress`: SOCKS5 listen address (string, optional)
- `SBHttpListenPort`: HTTP proxy listen port on near node (int, optional; 0 disables)
- `SBConnect`: If true, acts as near node (initiates QUIC connection)
- `SBNearPort`: QUIC port on near node - Far ONLY (int)
- `SBFarPort`: QUIC port on far node - Near ONLY (int)
- `SBFarIp`: Far node IP address (string, required for connect mode)
- `SBIdleTimeout`: Idle timeout (duration e.g. 10s or 2m, optional)
- `SBInitialPacketSize`: QUIC initial packet size (int e.g. 50M, optional)
- `SBRecieveWindow`: QUIC receive window (size e.g. 50M, optional)
- `SBMaxRecieveWindow`: QUIC max receive window (size e.g. 50M, optional)
- `SBTotalBandwidthLimit`: Bandwidth limit (size e.g. 100M or 1G, optional)

### Logging Configuration (`globallog`)
Logging is configured via the `globallog` section in your config:

```yaml
globallog:
  Filename: "sc.log"   # Log file name
  MaxSize: 20          # Max log file size (megabytes)
  MaxBackups: 5        # Max number of old log files to keep
  MaxAge: 28           # Max number of days to retain old log files
  Compress: false      # Whether to compress old log files
```

- `Filename`: Log file name (string)
- `MaxSize`: Maximum log file size before rotation (int, megabytes)
- `MaxBackups`: Maximum number of backup log files to keep (int)
- `MaxAge`: Maximum number of days to retain old log files (int, days)
- `Compress`: Whether to compress rotated log files (bool)

## Ratetest App

Built with the 'build-ratetest.sh' command. It requires a valid scconfig.yml file to configure the tests.

### Modes
#### Listen
./salmon-rate -mode=listen

Listens on port 5555 for incomming TCP connections on 127.0.0.1
#### Test
./salmon-rate -mode=test

Uses the config to start a 10 sec ratetest on all of the salmonbridges configured with 'connect: true'.

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
