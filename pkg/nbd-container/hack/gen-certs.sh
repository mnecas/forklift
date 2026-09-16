#!/usr/bin/env bash
#
# Generate a self-signed CA and server + client certificates for running the
# nbd-container acceptance tests (mutual TLS with X.509 client verification).
#
# Produces, in the output directory:
#   ca-cert.pem       CA that signs the server and client certs
#   ca-key.pem        CA private key (keep local; not needed by the container)
#   server-cert.pem   server certificate  ) bind-mount this dir to /etc/pki/nbdkit
#   server-key.pem    server private key   )
#   client-cert.pem   client certificate  ) point the NBD client's
#   client-key.pem    client private key   ) tls-certificates dir here
#
# The server certificate is issued for a fixed *logical* name (default: nbd-server)
# rather than any real hostname or IP. On-demand servers come up on unpredictable
# addresses, so clients connect to the real address but verify the certificate against
# this stable logical name via libnbd's `tls-hostname` parameter. See the README.
#
# Usage:
#   hack/gen-certs.sh [output-dir] [server-name]
#
# Environment overrides (take precedence over positional args):
#   OUT_DIR      output directory        (default: ./certs)
#   SERVER_NAME  logical server identity (default: nbd-server)

set -euo pipefail

OUT_DIR="${OUT_DIR:-${1:-./certs}}"
SERVER_NAME="${SERVER_NAME:-${2:-nbd-server}}"

command -v certtool >/dev/null 2>&1 || {
	echo "error: certtool not found (install the 'gnutls-utils' package)" >&2
	exit 1
}

mkdir -p "$OUT_DIR"
cd "$OUT_DIR"

# certtool reads certificate attributes from a template file. We write each one to a
# temp file and clean up on exit.
tmpl="$(mktemp)"
trap 'rm -f "$tmpl"' EXIT

echo "Generating certificates in $(pwd) (logical server name=$SERVER_NAME)"

# --- Certificate authority ---------------------------------------------------
certtool --generate-privkey --outfile ca-key.pem
cat > "$tmpl" <<EOF
cn = NBD Test CA
ca
cert_signing_key
expiration_days = 7300
EOF
certtool --generate-self-signed \
	--load-privkey ca-key.pem \
	--template "$tmpl" \
	--outfile ca-cert.pem

# --- Server ------------------------------------------------------------------
certtool --generate-privkey --outfile server-key.pem
cat > "$tmpl" <<EOF
cn = $SERVER_NAME
dns_name = $SERVER_NAME
tls_www_server
encryption_key
signing_key
expiration_days = 3650
EOF
certtool --generate-certificate \
	--load-privkey server-key.pem \
	--load-ca-certificate ca-cert.pem \
	--load-ca-privkey ca-key.pem \
	--template "$tmpl" \
	--outfile server-cert.pem

# --- Client ------------------------------------------------------------------
certtool --generate-privkey --outfile client-key.pem
cat > "$tmpl" <<EOF
cn = nbd-client
tls_www_client
encryption_key
signing_key
expiration_days = 3650
EOF
certtool --generate-certificate \
	--load-privkey client-key.pem \
	--load-ca-certificate ca-cert.pem \
	--load-ca-privkey ca-key.pem \
	--template "$tmpl" \
	--outfile client-cert.pem

# nbdkit refuses a world-readable server key; lock down all private keys.
chmod 0600 ./*-key.pem
chmod 0644 ./*-cert.pem

echo "Done. Files:"
ls -l ca-cert.pem ca-key.pem server-cert.pem server-key.pem client-cert.pem client-key.pem

cat <<EOF

Next steps:
  # Run the server, bind-mounting this dir as the server cert store:
  podman run --rm --device /dev/sdX:/dev/nbd-export:r \\
    -v $(pwd):/etc/pki/nbdkit:ro,Z -p 10809:10809 nbd-container

  # Connect a client. Point the URI at the server's real host/IP, but verify the
  # certificate against its logical name ($SERVER_NAME) with tls-hostname. The
  # client dir supplies ca-cert.pem + client-cert.pem + client-key.pem.
  nbdinfo "nbds://SERVER_HOST_OR_IP:10809/?tls-certificates=$(pwd)&tls-hostname=$SERVER_NAME"
EOF
