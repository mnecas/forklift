#!/bin/bash
# Extract image-baked appliance installroot tarball onto the guest disk.
set -eux

tarball="${TOEHOLD_APPLIANCE_TARBALL:-/root/appliance-root.tar.gz}"
test -f "${tarball}"

tar -xzf "${tarball}" -C /

root=/opt/toehold/appliance-root
ln -sfn "${root}/etc/vmware-tools" /etc/vmware-tools
if [[ ! -f /etc/vmware-tools/tools.conf && -f /etc/vmware-tools/tools.conf.example ]]; then
  cp /etc/vmware-tools/tools.conf.example /etc/vmware-tools/tools.conf
fi

want=/etc/systemd/system/multi-user.target.wants
mkdir -p "${want}"
for unit in toehold-vgauthd.service toehold-vmtoolsd.service; do
  test -f "/etc/systemd/system/${unit}"
  ln -sfn "../${unit}" "${want}/${unit}"
done

if command -v restorecon >/dev/null 2>&1; then
  restorecon -R /opt/toehold/appliance-root || true
fi
