#!/usr/bin/env bash
#
# Query the nbd-orchestrator announce endpoint over mutual TLS and print the exported
# disks as JSON.
#
# The orchestrator's HTTPS server presents a certificate issued for a fixed *logical*
# name (nbd-server), not its real address. We connect to the server's real host/IP but
# verify the certificate against that logical name -- curl's --connect-to keeps the
# logical name for SNI and certificate verification while dialing the real address.
# The endpoint also requires a client certificate signed by the shared CA (mutual TLS),
# which we present from CERTS_DIR.
#
# Usage:
#   hack/query-disks.sh <host-or-ip> [port]
#
# Environment overrides (take precedence over positional args):
#   HOST        supervisor host or IP             (required; or positional arg 1)
#   PORT        announce server port              (default: 8443)
#   CERTS_DIR   dir with ca-cert/client cert+key  (default: ./certs)
#   TLS_NAME    logical name in the server cert   (default: nbd-server)
#   ENDPOINT    path to request                   (default: /disks)

set -euo pipefail

HOST="${HOST:-${1:-}}"
PORT="${PORT:-${2:-8443}}"
CERTS_DIR="${CERTS_DIR:-./certs}"
TLS_NAME="${TLS_NAME:-nbd-server}"
ENDPOINT="${ENDPOINT:-/disks}"

die() {
	echo "query-disks: error: $*" >&2
	exit 1
}

[ -n "$HOST" ] || die "no host given -- usage: hack/query-disks.sh <host-or-ip> [port]"
command -v curl >/dev/null 2>&1 || die "curl not found"

# Client material for mutual TLS. ca-cert.pem also verifies the server chain.
for f in ca-cert.pem client-cert.pem client-key.pem; do
	[ -f "$CERTS_DIR/$f" ] || die "missing '$CERTS_DIR/$f' -- set CERTS_DIR to the client cert dir"
done

# --connect-to <logical>:<port>:<real-host>:<port> dials the real address but keeps the
# logical name for SNI and certificate verification, so the cert (issued for TLS_NAME)
# validates even though HOST is some unpredictable on-demand address.
resp="$(curl --fail --show-error --silent \
	--cacert "$CERTS_DIR/ca-cert.pem" \
	--cert "$CERTS_DIR/client-cert.pem" \
	--key "$CERTS_DIR/client-key.pem" \
	--connect-to "$TLS_NAME:$PORT:$HOST:$PORT" \
	"https://$TLS_NAME:$PORT$ENDPOINT")" ||
	die "request to $HOST:$PORT$ENDPOINT failed (TLS verification or HTTP error)"

# Pretty-print when jq is available; otherwise emit the raw JSON body.
if command -v jq >/dev/null 2>&1; then
	printf '%s\n' "$resp" | jq .
else
	printf '%s\n' "$resp"
fi
