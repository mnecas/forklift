#!/bin/sh
#
# Start nbdkit in the foreground, exporting a host block device over NBD with
# mutual TLS (the server requires and verifies an X.509 client certificate).
#
# Configuration (env vars, all optional):
#   DISK       path inside the container of the block device to export
#              (default: /dev/nbd-export -- map the host device here with
#               `podman run --device /dev/sdX:/dev/nbd-export:r`)
#   CERTS_DIR  directory holding ca-cert.pem, server-cert.pem, server-key.pem
#              (default: /etc/pki/nbdkit -- bind-mount the host cert dir here)
#   PORT       TCP port to listen on (default: 10809)

set -eu

DISK="${DISK:-/dev/nbd-export}"
CERTS_DIR="${CERTS_DIR:-/etc/pki/nbdkit}"
PORT="${PORT:-10809}"

die() {
	echo "entrypoint: error: $*" >&2
	exit 1
}

# The export target must be a block device present in the container.
[ -e "$DISK" ] || die "device '$DISK' not found -- pass it in with '--device /dev/sdX:$DISK:r'"
[ -b "$DISK" ] || die "'$DISK' exists but is not a block device"

# All three files are required for mutual TLS. ca-cert.pem is used both to present
# the server chain and to verify client certificates (--tls-verify-peer).
for f in ca-cert.pem server-cert.pem server-key.pem; do
	[ -f "$CERTS_DIR/$f" ] || die "missing certificate '$CERTS_DIR/$f' -- bind-mount the cert dir to '$CERTS_DIR'"
done

echo "entrypoint: exporting $DISK (read-only) on port $PORT with mutual TLS from $CERTS_DIR"

# --readonly    : never write to the host device (primary read-only guard)
# --tls=require : refuse any non-TLS connection
# --tls-verify-peer : require and verify a client cert signed by ca-cert.pem
# exec so nbdkit becomes PID 1 and receives signals directly.
exec nbdkit --foreground \
	--readonly \
	--port "$PORT" \
	--tls=require \
	--tls-certificates="$CERTS_DIR" \
	--tls-verify-peer \
	file "$DISK"
