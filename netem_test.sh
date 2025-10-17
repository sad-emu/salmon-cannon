#!/usr/bin/env bash
# run_ratetest.sh
#
# Fedora42-oriented helper to:
#  1) Clean /tmp/sc-test
#  2) Create near and far dirs
#  3) Write simple scconfig.yml for far (accept) and near (connect)
#  4) Build sc and salmon-rate binaries from the repo root where this script lives
#  5) Start far then near (background) and a ratetest listener on far (mode=listen)
#  6) Apply Linux netem (hostile profile) to a network interface (default: lo)
#     BUT only affect UDP traffic — TCP will go through normally.
#  7) Run the ratetest (near-side) and print the ratetest output to the console
#  8) Restore the network interface (remove qdisc) and clean up processes on exit
#
# Usage: run from repository root (where main.go and ratetest/ live).
#   ./run_ratetest.sh
#
# By default this will apply netem only to UDP on the loopback interface (lo).
# You may override the interface with NETIF env var:
#   NETIF=lo ./run_ratetest.sh
#
# Notes:
#  - Requires `go`, `tc` (iproute2) and `sudo` (if not running as root).
#  - This script uses a prio qdisc and classifies UDP (ip proto 17) into band 3
#    which has netem applied. TCP will remain on the default bands.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKDIR="/tmp/sc-test"
FAR_DIR="$WORKDIR/far"
NEAR_DIR="$WORKDIR/near"
BIN_DIR="$WORKDIR/bin"

FAR_PORT=8444
SOCKS_PORT=1080

SC_BIN="$BIN_DIR/sc"
RATETEST_BIN="$BIN_DIR/salmon-rate"

# netem settings (Example 1 - hostile-ish)
NETIF="${NETIF:-lo}"
NETEM_DELAY="20ms"
NETEM_JITTER="10ms"
NETEM_LOSS="20% 30%"
NETEM_REORDER="20% 30%"
NETEM_DUP="1%"
NETEM_CORRUPT="1%"

OLD_QDISC_FILE="$WORKDIR/old_qdisc.txt"

FAR_PID=""
NEAR_PID=""
RATELISTEN_PID=""
NETEM_APPLIED=0

# cleanup function to kill background processes and remove netem
cleanup() {
    echo "Cleaning up..."
    # remove netem first to restore networking before killing things that might depend on it
    if [[ $NETEM_APPLIED -eq 1 ]]; then
        echo "Restoring network interface $NETIF (removing qdisc)..."
        if command -v sudo >/dev/null 2>&1; then
            sudo tc qdisc del dev "$NETIF" root 2>/dev/null || true
        else
            tc qdisc del dev "$NETIF" root 2>/dev/null || true
        fi
        NETEM_APPLIED=0
        echo "Old qdisc saved at: $OLD_QDISC_FILE"
        if [[ -f "$OLD_QDISC_FILE" ]]; then
            echo "Previous qdisc (for inspection):"
            sed -n '1,200p' "$OLD_QDISC_FILE" || true
        fi
    fi

    if [[ -n "${FAR_PID:-}" ]]; then
        echo "Killing far (pid $FAR_PID)"
        kill "$FAR_PID" 2>/dev/null || true
    fi
    if [[ -n "${NEAR_PID:-}" ]]; then
        echo "Killing near (pid $NEAR_PID)"
        kill "$NEAR_PID" 2>/dev/null || true
    fi
    if [[ -n "${RATELISTEN_PID:-}" ]]; then
        echo "Killing ratetest listener (pid $RATELISTEN_PID)"
        kill "$RATELISTEN_PID" 2>/dev/null || true
    fi

    # Wait briefly for processes to die
    sleep 0.3
}
trap cleanup EXIT

echo "==> Preparing workspace: $WORKDIR"
rm -rf "$WORKDIR"
mkdir -p "$FAR_DIR" "$NEAR_DIR" "$BIN_DIR"

echo "==> Checking toolchain and tc"
if ! command -v go >/dev/null 2>&1; then
    echo "Go toolchain not found in PATH. Please install Go and retry."
    exit 1
fi
if ! command -v tc >/dev/null 2>&1; then
    echo "tc (iproute2) not found in PATH. Please install iproute2 and retry."
    exit 1
fi

echo "==> Building binaries into $BIN_DIR"
cd "$REPO_ROOT"
echo "  - building sc (main)..."
go build -o "$SC_BIN" ./ || { echo "go build sc failed"; exit 1; }
echo "  - building salmon-rate (ratetest)..."
go build -o "$RATETEST_BIN" ./ratetest || { echo "go build ratetest failed"; exit 1; }

echo "==> Writing configuration files"

# Far (accepting QUIC connections)
cat > "$FAR_DIR/scconfig.yml" <<EOF
salmonbridges:
  - SBName: "sc-near"
    SBConnect: false
    SBNearPort: ${FAR_PORT}
    SBSocksListenPort: 0
    SBSocksListenAddress: "127.0.0.1"
    SBIdleTimeout: 10s
    SBInitialPacketSize: 1350
    SBRecieveWindow: 10M
    SBMaxRecieveWindow: 40M
    SBTotalBandwidthLimit: 100M
globallog:
  Filename: "sc.log"
  MaxSize: 5
  MaxBackups: 2
  MaxAge: 1
  Compress: false
EOF

# Near (connects to far, exposes a local SOCKS5 interface that ratetest will talk to)
cat > "$NEAR_DIR/scconfig.yml" <<EOF
salmonbridges:
  - SBName: "sc-near"
    SBSocksListenPort: ${SOCKS_PORT}
    SBSocksListenAddress: "127.0.0.1"
    SBHttpListenPort: 0
    SBConnect: true
    SBFarPort: ${FAR_PORT}
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: 10s
    SBInitialPacketSize: 1350
    SBRecieveWindow: 10M
    SBMaxRecieveWindow: 40M
    SBTotalBandwidthLimit: 100M
globallog:
  Filename: "sc.log"
  MaxSize: 5
  MaxBackups: 2
  MaxAge: 1
  Compress: false
EOF

echo "==> Starting far instance (accept mode) in background"
cd "$FAR_DIR"
nohup "$SC_BIN" > far.stdout.log 2> far.stderr.log &
FAR_PID=$!
echo "Far PID: $FAR_PID"
sleep 0.8

echo "Waiting briefly for far to start..."
sleep 0.6

echo "==> Starting ratetest listener on far (mode=listen) in background"
cd "$FAR_DIR"
nohup "$RATETEST_BIN" -mode=listen > ratetest-listen.stdout.log 2> ratetest-listen.stderr.log &
RATELISTEN_PID=$!
echo "ratetest listener PID: $RATELISTEN_PID"
sleep 0.6

echo "==> Starting near instance (connect mode) in background"
cd "$NEAR_DIR"
nohup "$SC_BIN" > near.stdout.log 2> near.stderr.log &
NEAR_PID=$!
echo "Near PID: $NEAR_PID"

# Wait for near to settle and start listening on SOCKS port
echo "Waiting for near SOCKS listen to come up..."
sleep 1.2

# Apply UDP-only netem to the chosen interface
apply_netem_udp_only() {
    echo "==> Preparing to apply UDP-only netem to interface: $NETIF"
    mkdir -p "$(dirname "$OLD_QDISC_FILE")"
    # Save existing qdisc for inspection
    if command -v sudo >/dev/null 2>&1; then
        sudo tc qdisc show dev "$NETIF" > "$OLD_QDISC_FILE" 2>/dev/null || true
    else
        tc qdisc show dev "$NETIF" > "$OLD_QDISC_FILE" 2>/dev/null || true
    fi
    echo "Saved existing qdisc to $OLD_QDISC_FILE"

    # 1) Create a classful root qdisc (prio) — simple 3-band classifier.
    #    Band 3 will carry UDP (netem), other bands are untouched.
    echo "Setting up prio root qdisc on $NETIF (bands: 3)"
    if command -v sudo >/dev/null 2>&1; then
        sudo tc qdisc replace dev "$NETIF" root handle 1: prio
    else
        tc qdisc replace dev "$NETIF" root handle 1: prio
    fi

    # 2) Attach netem to band 3 (parent 1:3)
    echo "Attaching netem to band 3 (parent 1:3)"
    NETEM_CMD=(tc qdisc replace dev "$NETIF" parent 1:3 handle 30: netem
        delay "${NETEM_DELAY}" "${NETEM_JITTER}" distribution normal
        loss ${NETEM_LOSS}
        reorder ${NETEM_REORDER}
        duplicate ${NETEM_DUP}
        corrupt ${NETEM_CORRUPT})
    if command -v sudo >/dev/null 2>&1; then
        sudo "${NETEM_CMD[@]}"
    else
        "${NETEM_CMD[@]}"
    fi

    # 3) Add filter: match IPv4 UDP (protocol 17) and send to flowid 1:3
    echo "Adding filter to classify IPv4 UDP into band 3"
    if command -v sudo >/dev/null 2>&1; then
        sudo tc filter replace dev "$NETIF" protocol ip parent 1: prio 1 u32 \
            match ip protocol 17 0xff \
            flowid 1:3
    else
        tc filter replace dev "$NETIF" protocol ip parent 1: prio 1 u32 \
            match ip protocol 17 0xff \
            flowid 1:3
    fi

    # 4) (Optional) Add IPv6 UDP filter so udp6 is also classified
    echo "Adding filter to classify IPv6 UDP into band 3 (if kernel supports it)"
    if command -v sudo >/dev/null 2>&1; then
        sudo tc filter replace dev "$NETIF" protocol ipv6 parent 1: prio 2 flower ip_proto 17 action goto chain 1 2>/dev/null || true
        # Simpler attempt using u32 style for ip6 (older kernels may not support match ip6 nexthdr in u32).
        sudo tc filter replace dev "$NETIF" protocol ipv6 parent 1: prio 2 u32 \
            match ip6 nexthdr 17 0xff \
            flowid 1:3 2>/dev/null || true
    else
        tc filter replace dev "$NETIF" protocol ipv6 parent 1: prio 2 flower ip_proto 17 action goto chain 1 2>/dev/null || true
        tc filter replace dev "$NETIF" protocol ipv6 parent 1: prio 2 u32 \
            match ip6 nexthdr 17 0xff \
            flowid 1:3 2>/dev/null || true
    fi

    NETEM_APPLIED=1
    echo "UDP-only netem applied to $NETIF (band 3)."
    # show qdisc and filters
    if command -v sudo >/dev/null 2>&1; then
        sudo tc -s qdisc show dev "$NETIF"
        sudo tc filter show dev "$NETIF" parent 1:
    else
        tc -s qdisc show dev "$NETIF"
        tc filter show dev "$NETIF" parent 1:
    fi
}

echo "==> Applying UDP-only netem (hostile profile) to interface: $NETIF"
apply_netem_udp_only || { echo "Failed to apply netem. Exiting."; exit 1; }

echo "==> Running ratetest (mode=test) in near directory, output will be shown below"
cd "$NEAR_DIR"

# Run ratetest and capture its output. This binary reads scconfig.yml from cwd, which points to the near config.
set +e
"$RATETEST_BIN" -mode=test 2>&1 | tee ratetest_full_output.txt
RT_EXIT=$?
set -e

if [[ $RT_EXIT -ne 0 ]]; then
    echo "ratetest exited with code $RT_EXIT"
else
    echo "ratetest completed successfully."
fi

# Print the final summary lines from the ratetest output


echo
echo "Far logs: $FAR_DIR/sc.log  (stdout: $FAR_DIR/far.stdout.log)"
echo "Near logs: $NEAR_DIR/sc.log (stdout: $NEAR_DIR/near.stdout.log)"
echo "ratetest listener logs: $FAR_DIR/ratetest-listen.stdout.log (stderr: $FAR_DIR/ratetest-listen.stderr.log)"
echo
echo "Netem (UDP-only) was applied to interface: $NETIF"
echo "If you want to stop the sc instances and ratetest listener now, press ENTER; otherwise they'll be left running until this script exits (or Ctrl+C)."
echo
echo "===== ratetest summary (extracted) ====="
tail -n 40 ratetest_full_output.txt || true
echo "========================================"

# exit -> cleanup trap will run and remove qdisc + kill processes