# Makefile for nbd-container.
#
# `build` (the default) produces a statically linked, pure-Go orchestrator binary
# (CGO disabled) so it runs on older targets that lack a compatible glibc (e.g. RHEL
# 8-era hosts). Override GOOS/GOARCH to cross-compile. The remaining targets build the
# nbdkit container image and wrap the hack/ scripts for the connect/disconnect workflow.
#
# Common overrides (make VAR=value target):
#   HOST         supervisor host/IP for query/connect (required by those targets)
#   PORT         announce server port                 (default: 8443)
#   CERTS_DIR    certificate directory                (default: ./certs)
#   IMAGE        nbdkit container image tag           (default: localhost/nbd-container)
#   ENGINE       container engine                     (default: podman)
#   SUDO         privilege escalation for root targets (default: sudo; set empty if root)

BINARY  := nbd-orchestrator
PKG     := github.com/kubev2v/forklift/cmd/nbd-orchestrator
GOOS    ?= linux
GOARCH  ?= amd64
LDFLAGS := -s -w

IMAGE     ?= localhost/nbd-container
ENGINE    ?= podman
SUDO      ?= sudo
CERTS_DIR ?= ./certs
PORT      ?= 8443
HOST      ?=
PUBLISH_IP ?= 0.0.0.0
LISTEN     ?= 0.0.0.0:8443

# Tests and vet come from the repository's top-level Makefile, which runs them over
# ./pkg/... ./cmd/... and so covers this code along with everything else.
.PHONY: build image run certs query connect disconnect clean help

# Statically linked release binary. CGO_ENABLED=0 forces Go's pure-Go net/user
# resolvers instead of linking against the host glibc.
build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) $(PKG)

# Build the nbdkit container image the orchestrator launches per disk.
image:
	$(ENGINE) build -t $(IMAGE) -f Containerfile .

# Build then run the orchestrator locally (needs root for podman + device access).
run: build
	$(SUDO) ./$(BINARY) -certs-dir=$(CERTS_DIR) -image=$(IMAGE) \
		-publish-ip $(PUBLISH_IP) -listen $(LISTEN)

# Generate the CA + server/client certificates into CERTS_DIR.
certs:
	hack/gen-certs.sh $(CERTS_DIR)

# Query the announce endpoint for the exported disks (mutual TLS).
query:
	@[ -n "$(HOST)" ] || { echo "HOST is required, e.g. make query HOST=<host-or-ip>" >&2; exit 1; }
	CERTS_DIR=$(CERTS_DIR) hack/query-disks.sh $(HOST) $(PORT)

# Attach every advertised export to a local /dev/nbdN (records state for disconnect).
connect:
	@[ -n "$(HOST)" ] || { echo "HOST is required, e.g. make connect HOST=<host-or-ip>" >&2; exit 1; }
	$(SUDO) env CERTS_DIR=$(CERTS_DIR) hack/connect-disks.sh $(HOST) $(PORT)

# Tear down everything connect attached, per the recorded state file.
disconnect:
	$(SUDO) hack/disconnect-disks.sh

clean:
	rm -f $(BINARY)

help:
	@echo "Targets:"
	@echo "  build       statically linked orchestrator binary (default)"
	@echo "  image       build the nbdkit container image ($(IMAGE))"
	@echo "  run       build and run the orchestrator locally (HOST-side; uses $(SUDO))"
	@echo "  certs       generate CA + server/client certs into $(CERTS_DIR)"
	@echo "  query       list exported disks from HOST (mutual TLS)"
	@echo "  connect     attach all exports from HOST to local /dev/nbdN devices"
	@echo "  disconnect  detach everything connect attached"
	@echo "  clean       remove the built binary"
	@echo ""
	@echo "Set HOST=<host-or-ip> for query/connect. See the header of this file for more overrides."
