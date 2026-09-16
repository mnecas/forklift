# Handoff notes

Operational and orientation notes for whoever picks this up next. For user-facing usage
details see `README.md`; this document covers **what's in the repo, how it fits
together, and the deployment gotchas that aren't obvious from the code.**

## What this project is

Two cooperating pieces that export a host's spare block devices over the network as
**read-only NBD targets secured with mutual TLS**:

1. **`nbd-container`** — a container image (CentOS Stream 9 + `nbdkit`) that exports a
   single host block device over NBD. Read-only, TLS required, client cert verified.
   Certs are host-supplied (bind-mounted), never baked in.
2. **`nbd-orchestrator`** — a Go program that runs *on the host*, discovers eligible
   disks, starts one `nbd-container` per disk via `podman`, and serves a mutual-TLS
   HTTPS "announce" endpoint listing the exports.

Clients query the announce endpoint to learn `{wwid, port, device}` for each export,
then connect to those NBD ports. All TLS uses a shared CA; the server cert is issued for
a fixed **logical name** (`nbd-server`), not a real address, so on-demand servers on
unpredictable IPs can share one cert — clients verify against the logical name.

## Repository layout

Paths are relative to the Forklift repository root.

```
pkg/nbd-container/
  Containerfile            nbd-container image (centos:stream9 + nbdkit)
  entrypoint.sh            container entrypoint: launches nbdkit read-only, TLS+verify-peer
  Makefile                 image + wrappers for the hack scripts
  README.md                user-facing usage
  HANDOFF.md               this file

  blockdev/discover.go     lsblk/udevadm discovery of exportable disks
  blockdev/discover_test.go  parse() test with an inline synthetic lsblk fixture
  runner/podman.go         start/reconcile/verify/teardown of per-disk containers
  runner/podman_test.go    sanitize() + parseContainerState() tests
  announce/server.go       mutual-TLS HTTPS server exposing /disks, /healthz
  announce/server_test.go  verifies a request without a client cert is rejected

  hack/
    gen-certs.sh           generate CA + server + client certs (certtool)
    query-disks.sh         query the announce endpoint over mTLS -> JSON
    connect-disks.sh       attach every export to a local /dev/nbdN (records state)
    disconnect-disks.sh    tear down what connect-disks.sh recorded

cmd/nbd-orchestrator/
  main.go                  flags, startup, signal handling, shutdown/cleanup
```

The command lives under `cmd/` like every other Forklift binary, and the packages under
`pkg/` rather than `internal/` so that `pkg/controller/copyappliance` can reach the
announce client and the `Export` type when it comes to query the endpoint.

## Data flow

1. `blockdev.Discover` runs `lsblk -J -b -o NAME,TYPE,RO,SIZE,MOUNTPOINT`, keeps
   top-level `disk`s that are writable, not pseudo (`zram*`/`loop*`/`sr*`), and have no
   mountpoint anywhere in their subtree. `udevadm` resolves a stable WWID (falls back to
   the device path).
2. `runner.Reconcile` starts one container per device (`podman run -d --restart=always`),
   labeled `nbd.wwid=<wwid>`, publishing a host port (from `--base-port`, default 10809)
   to the container's fixed 10809. Existing containers are reused by WWID (idempotent).
3. `announce` serves the resulting `[]Export` as JSON over mutual TLS.

## Build

The orchestrator **must be built static** for the intended targets:

```sh
make build      # CGO_ENABLED=0, static, stripped -> ./nbd-orchestrator
```

Why: the appliance's glibc is not the build host's, and can be much older (RHEL 8-era
hosts were the original target). `CGO_ENABLED=0` forces Go's pure-Go net/user resolvers
so the binary has no glibc dependency and runs anywhere. Cross-compile with `make build
GOARCH=arm64` (clean, since cgo is off). Build the image with `make image`.

The shipping build is not this one. `build/forklift-controller/Containerfile` and its
`-downstream` twin build the orchestrator into the forklift-controller image at
`/usr/local/bin/nbd-orchestrator`, so the controller has it to hand when it deploys an
appliance. That build also clears the FIPS settings the rest of the image uses — see the
comment on the `RUN` line, and the FIPS note below.

## Makefile targets

`build` (default), `image`, `run`, `certs`, `query`, `connect`, `disconnect`, `clean`,
`help`. `query`/`connect` need `HOST=<host-or-ip>`. Root-needing targets use `sudo`
(override with `SUDO=`). See the Makefile header for all overrides (`IMAGE`, `ENGINE`,
`CERTS_DIR`, `PORT`, `PUBLISH_IP`, `LISTEN`).

Tests, vet and fmt are the repository's, not this directory's: `make test`, `make vet`
and `make fmt` at the repository root run over `./pkg/... ./cmd/...` and so cover this
code along with everything else.

## Client workflow (connect / disconnect)

```sh
make certs                          # once: generate ./certs (CA + server + client)
make image                          # once: build the nbdkit image
sudo make run HOST-side             # on the host: discover + serve (or: make run)

# from a client that has ./certs:
make query   HOST=<host-or-ip>      # list exports
sudo make connect HOST=<host-or-ip> # attach each export to /dev/nbdN
sudo make disconnect                # detach everything connect attached
```

`connect-disks.sh` appends one line per attach (`nbddev host port device wwid`) to
`hack/.nbd-connections`. `disconnect-disks.sh` reads that file to know exactly what to
unmount and disconnect, removing entries it tears down (keeping failures for retry).
The file is the single source of truth for the pair — deleting it loses the record.

## Deployment gotchas (hard-won — read before debugging)

These caused real debugging sessions. Most are captured in code/comments now, but the
symptoms are non-obvious:

- **Static build.** A dynamically linked binary fails on the target with glibc/loader
  errors. Always ship the `CGO_ENABLED=0` build. (`make build`.)
- **lsblk column compatibility.** Old util-linux (RHEL 8 = 2.32) lacks the `PATH`
  column and the plural `MOUNTPOINTS` array, and emits JSON values as *strings*, not
  typed. `discover.go` handles this (singular `MOUNTPOINT`, `Path` derived from name,
  `flexBool`/`flexUint64` decoders). Verify any new lsblk column against 2.32 before
  using it.
- **firewalld.** Targets reject unlisted ports (fast "connection refused"/"no route to
  host", *not* a timeout). Open the announce port and the per-disk NBD ports:
  `firewall-cmd --add-port=8443/tcp --add-port=10809-10908/tcp --permanent && firewall-cmd --reload`.
  Widen the NBD range to cover your max disk count. Cloud VMs also need a security-group
  rule — the host firewall being open is not enough.
  **On a Forklift copy appliance this is a template requirement.** The controller installs
  and starts the orchestrator over SSH but does not touch the firewall, so the image the
  appliance VM is cloned from must already permit both. An appliance whose template does
  not sits in `WaitForExports` indefinitely: the deploy has installed everything correctly
  and the controller simply cannot reach the announce port to see it.
- **Relative `--certs-dir` (fixed).** podman treats a non-absolute `-v` source as a
  *named volume*, not a bind mount — so a relative certs dir mounted an empty volume and
  nbdkit crash-looped with "missing certificate", while the orchestrator's own cwd check
  still passed. `main.go` now resolves `--certs-dir` with `filepath.Abs`. Any new host
  path handed to podman must be made absolute the same way.
- **`INIT_PASSWD bad` / connection refused from the client** almost always means the
  server-side container isn't actually serving (crash loop) or the port isn't reachable
  — not a TLS problem. Check `podman ps` and `podman logs nbd-<wwid>` on the host.

## Behavior worth knowing

- **Container lifecycle is owned by the supervisor.** On `SIGINT`/`SIGTERM` (or an
  announce-server error), the orchestrator force-removes all `nbd.wwid`-labeled
  containers (`runner.Shutdown`). Killing the supervisor tears down the exports. A later
  start recreates and re-verifies them. (This replaced the old "containers outlive the
  orchestrator" behavior.) Note: `SIGKILL`/host crash can't be trapped, so the containers
  survive as `exited`. Nothing runs `Shutdown` at startup; what clears them is
  `runner.existingPort`, which only reuses a *running* container for a WWID and
  `podman rm -f`s one in any other state so the reconcile below can create a replacement.
  Removing that removal makes an unclean reboot permanent: a stopped container publishes
  no port to announce and holds the name the replacement needs, so every later start —
  including the ones `--restart=always` provides — exports nothing.
- **Readiness check.** `podman run -d` returns success even if nbdkit exits immediately.
  `runner.verifyUp` waits ~750ms then fails if the container isn't `running` with zero
  restarts, includes the container log tail in the error, and removes the failed
  container. Failed exports are not announced.
- **One-shot discovery.** Disks are discovered once at startup. Newly attached disks are
  picked up by re-running the orchestrator (idempotent by WWID).

## Testing

```sh
go test ./pkg/nbd-container/...
```

- `discover_test.go` — `parse()` against an **inline synthetic** lsblk fixture (string
  typed values, nested lvm) exercising every selection rule and the flex decoders. (The
  old captured `testdata/lsblk.json` was removed; it contained real-system details.)
- `podman_test.go` — `sanitize()` and `parseContainerState()` (pure, table-driven), plus
  `existingPort`/`Reconcile` against a `podman` stub put on `PATH`: a container left
  `exited` by an unclean shutdown must be removed and replaced, not reused.
- `announce/server_test.go` — starts the server in-process and asserts a request without
  a client cert is rejected.
- `announce/client_test.go` — the client against that same server: the export list round
  trips, and a server named anything other than `ServerName`, signed by another CA, or
  presented a client certificate from another CA, is rejected.

End-to-end without spare hardware: `sudo modprobe scsi_debug dev_size_mb=64` creates a
fake disk with a WWID; run the orchestrator and query `/disks`.

## Conventions / notes

- Pure standard library + shelling out to `podman`, `lsblk`, `udevadm`. No third-party
  Go deps. That is what let this fold into Forklift's module without touching `go.mod`,
  `go.sum` or `vendor/`, and it is worth keeping true.
- **Not FIPS.** The mutual TLS here is Go's own crypto, not the OpenSSL backend every
  other Forklift binary links against, because that backend needs cgo and this binary
  has to be static. Revisit if the announce endpoint's security model is reviewed; if it
  has to become FIPS-linked, the appliance template must be RHEL 9-era and the
  `CGO_ENABLED=0` goes away.
- Shell scripts are `set -euo pipefail`, capture subprocess stderr in errors, and check
  for root / required tools up front.

## Possible follow-ups (not done)

- Re-run discovery periodically or on a signal, instead of one-shot at startup.
- Health-check reused (existing) containers in `Reconcile`, not just freshly started
  ones.
- README has some overlap with this file and predates a few changes; consider
  consolidating.
