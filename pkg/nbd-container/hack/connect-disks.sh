#!/usr/bin/env bash
#
# Query the nbd-orchestrator announce endpoint for the exported disks, then attach each
# one to a local /dev/nbdN device with the reference NBD client (mutual TLS).
#
# Discovery is delegated to hack/query-disks.sh, which speaks the same mutual-TLS HTTPS
# to the announce server and returns a JSON array of {wwid, port, device}. For each
# export we run nbd-client against the server's real host/IP but verify its certificate
# against the fixed logical name (nbd-server) via -tlshostname -- the on-demand nbdkit
# servers come up on unpredictable addresses but all present a cert for that stable
# name (see hack/gen-certs.sh and the README). The server requires a client cert signed
# by the shared CA, which we present from CERTS_DIR.
#
# Each successful attach is appended to a state file (.nbd-connections in this
# directory) recording the nbd device, host, port, source device and WWID.
# hack/disconnect-disks.sh reads that file to tear everything down again.
#
# The exports are read-only (nbdkit --readonly), so the resulting /dev/nbdN devices are
# safe to mount read-only, e.g.  mount -o ro /dev/nbd0 /mnt.
#
# Requires root (nbd-client and the nbd kernel module) plus nbd-client, jq and curl.
#
# Usage:
#   hack/connect-disks.sh <host-or-ip> [announce-port]
#
# Environment overrides (take precedence over positional args):
#   HOST           supervisor host or IP             (required; or positional arg 1)
#   PORT           announce server port              (default: 8443)
#   CERTS_DIR      dir with ca-cert/client cert+key  (default: ./certs)
#   TLS_NAME       logical name in the server certs  (default: nbd-server)
#   EXPORT_NAME    NBD export name to request        (default: "" -- nbdkit's default)

set -euo pipefail

HOST="${HOST:-${1:-}}"
PORT="${PORT:-${2:-8443}}"
CERTS_DIR="${CERTS_DIR:-./certs}"
TLS_NAME="${TLS_NAME:-nbd-server}"
EXPORT_NAME="${EXPORT_NAME:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_FILE="$SCRIPT_DIR/.nbd-connections"

die() {
	echo "connect-disks: error: $*" >&2
	exit 1
}

[ -n "$HOST" ] || die "no host given -- usage: hack/connect-disks.sh <host-or-ip> [announce-port]"
[ "$(id -u)" -eq 0 ] || die "must run as root (nbd-client and the nbd module need it) -- try: sudo $0 $*"
for cmd in nbd-client jq curl; do
	command -v "$cmd" >/dev/null 2>&1 || die "$cmd not found"
done

# Client material for mutual TLS. ca-cert.pem also verifies the server chain.
for f in ca-cert.pem client-cert.pem client-key.pem; do
	[ -f "$CERTS_DIR/$f" ] || die "missing '$CERTS_DIR/$f' -- set CERTS_DIR to the client cert dir"
done

# The nbd kernel module provides the /dev/nbdN block devices nbd-client binds to.
modprobe nbd >/dev/null 2>&1 || die "failed to load the 'nbd' kernel module"

# find_free_nbd prints the first /dev/nbdN with no client attached. The kernel exposes
# a 'pid' attribute under /sys/block/nbdN only while a device is connected.
find_free_nbd() {
	local d name
	for d in /sys/block/nbd*; do
		[ -d "$d" ] || continue
		name="${d#/sys/block/}"
		[ -e "$d/pid" ] || { echo "/dev/$name"; return 0; }
	done
	return 1
}

# Discover exports (mutual TLS) via the shared query script, passing our TLS settings.
echo "connect-disks: querying $HOST:$PORT for exports..." >&2
json="$(CERTS_DIR="$CERTS_DIR" TLS_NAME="$TLS_NAME" "$SCRIPT_DIR/query-disks.sh" "$HOST" "$PORT")" ||
	die "failed to query exports from $HOST:$PORT (see error above)"

count="$(printf '%s' "$json" | jq 'length')"
[ "$count" -gt 0 ] || { echo "connect-disks: no exports advertised by $HOST" >&2; exit 0; }
echo "connect-disks: $count export(s) advertised; attaching..." >&2

# Emit one "port<TAB>device<TAB>wwid" line per export for the loop below.
printf '%s' "$json" | jq -r '.[] | "\(.port)\t\(.device)\t\(.wwid)"' |
	while IFS=$'\t' read -r port device wwid; do
		nbddev="$(find_free_nbd)" || die "no free /dev/nbdN device available (raise nbd.nbds_max)"

		# Connect to the real HOST:port but verify the cert against TLS_NAME, presenting
		# our client cert/key. Only pass -N when a non-default export name is requested;
		# the empty default matches both nbdkit and nbd-client, and some nbd-client
		# builds reject an explicit empty -N.
		args=("$HOST" "$port" "$nbddev"
			-cacertfile "$CERTS_DIR/ca-cert.pem"
			-certfile "$CERTS_DIR/client-cert.pem"
			-keyfile "$CERTS_DIR/client-key.pem"
			-tlshostname "$TLS_NAME")
		[ -n "$EXPORT_NAME" ] && args+=(-N "$EXPORT_NAME")

		if out="$(nbd-client "${args[@]}" 2>&1)"; then
			printf '  %s  ->  %s  (%s, port %s)\n' "$device" "$nbddev" "$wwid" "$port"
			# Record the attach so disconnect-disks.sh can undo exactly this.
			printf '%s\t%s\t%s\t%s\t%s\n' "$nbddev" "$HOST" "$port" "$device" "$wwid" >> "$STATE_FILE"
		else
			echo "connect-disks: warning: failed to attach $device (port $port):" >&2
			printf '%s\n' "$out" | sed 's/^/    /' >&2
		fi
	done

echo "connect-disks: done. Mount read-only, e.g.: mount -o ro /dev/nbd0 /mnt" >&2
echo "connect-disks: tear down with: sudo hack/disconnect-disks.sh" >&2
