#!/usr/bin/env bash
#
# Disconnect the NBD client devices recorded by hack/connect-disks.sh.
#
# connect-disks.sh appends one line per attached device to a state file
# (.nbd-connections in this directory), recording "nbddev host port device wwid".
# This script reads that file and, for each entry, unmounts the nbd device (and any of
# its partitions) then tears down the connection with nbd-client -d. Entries that are
# successfully cleaned up are removed from the state file; any that fail are kept so a
# later run can retry them.
#
# Requires root and nbd-client.
#
# Usage:
#   hack/disconnect-disks.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_FILE="$SCRIPT_DIR/.nbd-connections"

die() {
	echo "disconnect-disks: error: $*" >&2
	exit 1
}

[ "$(id -u)" -eq 0 ] || die "must run as root (nbd-client needs it) -- try: sudo $0"
command -v nbd-client >/dev/null 2>&1 || die "nbd-client not found"
[ -s "$STATE_FILE" ] || { echo "disconnect-disks: nothing recorded in $STATE_FILE" >&2; exit 0; }

# mounts_of prints any mount source under a device: the whole device or its partitions
# (e.g. /dev/nbd0 and /dev/nbd0p1).
mounts_of() {
	awk -v dev="$1" '$1 == dev || index($1, dev "p") == 1 { print $1 }' /proc/mounts
}

# Failed entries are re-collected here and written back to the state file at the end.
remaining="$(mktemp)"
trap 'rm -f "$remaining"' EXIT
rc=0

while IFS=$'\t' read -r nbddev host port device wwid; do
	[ -n "$nbddev" ] || continue
	keep() { printf '%s\t%s\t%s\t%s\t%s\n' "$nbddev" "$host" "$port" "$device" "$wwid" >> "$remaining"; }

	# Unmount the device (and partitions) before tearing down the connection.
	unmount_failed=0
	for m in $(mounts_of "$nbddev"); do
		umount "$m" || { echo "disconnect-disks: failed to unmount $m" >&2; unmount_failed=1; }
	done
	if [ "$unmount_failed" -eq 1 ]; then
		keep; rc=1; continue
	fi

	# Disconnect if still attached ('pid' present); a device already gone counts as done.
	if [ -e "/sys/block/${nbddev#/dev/}/pid" ] && ! nbd-client -d "$nbddev" >/dev/null 2>&1; then
		echo "disconnect-disks: failed to disconnect $nbddev" >&2
		keep; rc=1; continue
	fi
	echo "disconnected $nbddev ($device from $host)"
done < "$STATE_FILE"

# Keep only the entries we could not tear down; drop the file entirely if all succeeded.
if [ -s "$remaining" ]; then
	cp "$remaining" "$STATE_FILE"
else
	rm -f "$STATE_FILE"
fi

exit "$rc"
