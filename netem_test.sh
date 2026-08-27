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
# Important defaults can be overridden without editing the script:
#   NETIF=lo NETEM_LIMIT=10000 TEST_WINDOW=64MB \
#     TEST_BANDWIDTH=2G MIN_RECEIVER_MBIT=1000 ./netem_test.sh
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
TEST_BANDWIDTH="${TEST_BANDWIDTH:-2G}"
TEST_WINDOW="${TEST_WINDOW:-64MB}"

# netem settings (Example 1 - hostile-ish)
NETIF="${NETIF:-lo}"
NETEM_DELAY="${NETEM_DELAY:-50ms}"
NETEM_JITTER="${NETEM_JITTER:-10ms}"
NETEM_LOSS="${NETEM_LOSS:-10% 10%}"
NETEM_REORDER="${NETEM_REORDER:-10% 10%}"
NETEM_DUP="${NETEM_DUP:-5%}"
NETEM_CORRUPT="${NETEM_CORRUPT:-5%}"
MIN_RECEIVER_MBIT="${MIN_RECEIVER_MBIT:-1000}"
# netem defaults to only 1,000 queued packets. At 2 Gbit/s, 50 ms of
# one-way delay already holds about 1,471 8,500-byte datagrams, before
# jitter, duplication, reordering, FEC, or recovery traffic. That default
# therefore injects a large amount of queue-overflow loss on top of the loss
# requested above. Keep enough BDP in the emulator for this profile.
NETEM_LIMIT="${NETEM_LIMIT:-10000}"

OLD_QDISC_FILE="$WORKDIR/old_qdisc.txt"

FAR_PID=""
NEAR_PID=""
RATELISTEN_PID=""
NETEM_APPLIED=0

# Use tc directly when already privileged (including an isolated user/network
# namespace); otherwise use sudo. Keeping this decision in one place also
# makes cleanup use the same authority as setup.
TC=(tc)
if [[ $EUID -ne 0 ]]; then
    if ! command -v sudo >/dev/null 2>&1; then
        echo "netem requires root/CAP_NET_ADMIN and sudo was not found" >&2
        exit 1
    fi
    TC=(sudo tc)
fi

# cleanup function to kill background processes and remove netem
cleanup() {
    echo "Cleaning up..."
    # remove netem first to restore networking before killing things that might depend on it
    if [[ $NETEM_APPLIED -eq 1 ]]; then
        echo "Restoring network interface $NETIF (removing qdisc)..."
        "${TC[@]}" qdisc del dev "$NETIF" root 2>/dev/null || true
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
SalmonBridges:
  - SBName: "sc-near"
    SBConnect: false
    SBNearPort: ${FAR_PORT}
    SBSocksListenPort: 0
    SBSocksListenAddress: "127.0.0.1"
    SBIdleTimeout: 10s
    SBInitialPacketSize: 8500
    SBMaxRecieveBufferSize: ${TEST_WINDOW}
    SBMaxBytesInFlight: ${TEST_WINDOW}
    SBInitialRetransmitTimeout: 300ms
    SBMinRetransmitTimeout: 150ms
    SBTotalBandwidthLimit: ${TEST_BANDWIDTH}
    SBPacingDatagramOverhead: 66
    SBPacingMinimumDatagramSize: 84
    SBFECGroupSize: 8
    SBFEC2D: true
    SBInterfaceName: "lo"
EOF

# Near (connects to far, exposes a local SOCKS5 interface that ratetest will talk to)
cat > "$NEAR_DIR/scconfig.yml" <<EOF
SalmonBridges:
  - SBName: "sc-near"
    SBSocksListenPort: ${SOCKS_PORT}
    SBSocksListenAddress: "127.0.0.1"
    SBHttpListenPort: 0
    SBConnect: true
    SBFarPort: ${FAR_PORT}
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: 10s
    SBInitialPacketSize: 8500
    SBMaxRecieveBufferSize: ${TEST_WINDOW}
    SBMaxBytesInFlight: ${TEST_WINDOW}
    SBInitialRetransmitTimeout: 300ms
    SBMinRetransmitTimeout: 150ms
    SBTotalBandwidthLimit: ${TEST_BANDWIDTH}
    SBPacingDatagramOverhead: 66
    SBPacingMinimumDatagramSize: 84
    SBFECGroupSize: 8
    SBFEC2D: true
    SBInterfaceName: "lo"
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
    "${TC[@]}" qdisc show dev "$NETIF" > "$OLD_QDISC_FILE" 2>/dev/null || true
    echo "Saved existing qdisc to $OLD_QDISC_FILE"

    # From this point onward cleanup must attempt to remove the root qdisc,
    # including when setup fails part-way through.
    NETEM_APPLIED=1

    # 1) Create a classful root qdisc (prio) — simple 3-band classifier.
    #    Band 3 will carry UDP (netem), other bands are untouched.
    echo "Setting up prio root qdisc on $NETIF (bands: 3)"
    "${TC[@]}" qdisc replace dev "$NETIF" root handle 1: prio

    # 2) Attach netem to band 3 (parent 1:3)
    echo "Attaching netem to band 3 (parent 1:3)"
    NETEM_CMD=(qdisc replace dev "$NETIF" parent 1:3 handle 30: netem
        limit "${NETEM_LIMIT}"
        delay "${NETEM_DELAY}" "${NETEM_JITTER}" distribution normal
        loss ${NETEM_LOSS}
        reorder ${NETEM_REORDER}
        duplicate ${NETEM_DUP}
        corrupt ${NETEM_CORRUPT})
    "${TC[@]}" "${NETEM_CMD[@]}"

    # 3) Add filter: match IPv4 UDP (protocol 17) and send to flowid 1:3
    echo "Adding filter to classify IPv4 UDP into band 3"
    "${TC[@]}" filter replace dev "$NETIF" protocol ip parent 1: prio 1 u32 \
        match ip protocol 17 0xff \
        flowid 1:3

    # 4) (Optional) Add IPv6 UDP filter so udp6 is also classified
    echo "Adding filter to classify IPv6 UDP into band 3 (if kernel supports it)"
    "${TC[@]}" filter replace dev "$NETIF" protocol ipv6 parent 1: prio 2 flower ip_proto 17 action goto chain 1 2>/dev/null || true
    # Simpler attempt using u32 style for ip6 (older kernels may not support match ip6 nexthdr in u32).
    "${TC[@]}" filter replace dev "$NETIF" protocol ipv6 parent 1: prio 2 u32 \
        match ip6 nexthdr 17 0xff \
        flowid 1:3 2>/dev/null || true

    echo "UDP-only netem applied to $NETIF (band 3)."
    # show qdisc and filters
    "${TC[@]}" -s qdisc show dev "$NETIF"
    "${TC[@]}" filter show dev "$NETIF" parent 1:
}

echo "==> Applying UDP-only netem (hostile profile) to interface: $NETIF"
# Call directly rather than from an `if`/`||` condition: Bash disables
# `set -e` throughout a function used as a conditional, which previously
# allowed failed tc commands to fall through into an unshaped benchmark.
apply_netem_udp_only

echo "==> Running ratetest (mode=test) in near directory, output will be shown below"
cd "$NEAR_DIR"

# Run ratetest and capture its output. This binary reads scconfig.yml from cwd, which points to the near config.
set +e
"$RATETEST_BIN" -mode=test -min-mbps "$MIN_RECEIVER_MBIT" 2>&1 | tee ratetest_full_output.txt
RT_EXIT=$?
set -e

echo "==> Final netem counters"
"${TC[@]}" -s qdisc show dev "$NETIF" || true

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
echo "The test processes will be stopped after the summary is printed."
echo
echo "===== ratetest summary (extracted) ====="
tail -n 40 ratetest_full_output.txt || true
echo "========================================"

# Preserve the benchmark result while still running the cleanup trap.
exit "$RT_EXIT"
