#!/usr/bin/env bash
set -euo pipefail

export LIBGUESTFS_BACKEND=direct

WORK=/work
OVERLAY="${WORK}/overlay.qcow2"
VMDK="${WORK}/disk-0.vmdk"
TARBALL=/usr/share/toehold/appliance-root.tar.gz

shopt -s nullglob
disks=(/disk/disk/*.qcow2)
if (( ${#disks[@]} != 1 )); then
  echo "toehold-build: expected one qcow2 under /disk/disk, found ${#disks[@]}" >&2
  exit 1
fi

qemu-img create -f qcow2 -b "${disks[0]}" -F qcow2 "${OVERLAY}"

args=(-a "${OVERLAY}" --copy-in "${TARBALL}:/root/" --run /usr/local/bin/extract-appliance-root)

[[ -n "${TOEHOLD_ROOT_PASSWORD:-}" ]] && args+=(--root-password "password:${TOEHOLD_ROOT_PASSWORD}")

virt-customize -vvv -x "${args[@]}"

qemu-img convert -f qcow2 -O vmdk -o subformat=streamOptimized "${OVERLAY}" "${VMDK}"
export TOEHOLD_VMDK_PATH="${VMDK}"
exec toehold-uploader
