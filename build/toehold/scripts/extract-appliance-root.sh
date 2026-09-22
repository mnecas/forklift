#!/bin/bash
# Extract image-baked appliance installroot tarball onto the guest disk.
set -eux

tarball="${TOEHOLD_APPLIANCE_TARBALL:-/root/appliance-root.tar.gz}"
test -f "${tarball}"

tar -xzf "${tarball}" -C /
rm -f "${tarball}"

if [[ -f /root/public-key ]]; then
  install -d -m 700 /root/.ssh
  cat /root/public-key >> /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
  rm -f /root/public-key
fi

root=/opt/toehold/appliance-root
mkdir -p /etc/containers
for f in policy.json storage.conf registries.conf; do
  if [[ -f "${root}/etc/containers/${f}" ]]; then
    ln -sfn "${root}/etc/containers/${f}" "/etc/containers/${f}"
  fi
done
ln -sfn "${root}/etc/vmware-tools" /etc/vmware-tools
if [[ ! -f /etc/vmware-tools/tools.conf && -f /etc/vmware-tools/tools.conf.example ]]; then
  cp /etc/vmware-tools/tools.conf.example /etc/vmware-tools/tools.conf
fi

want=/etc/systemd/system/multi-user.target.wants
timer_want=/etc/systemd/system/timers.target.wants
mkdir -p "${want}" "${timer_want}"
for unit in toehold-vgauthd.service toehold-vmtoolsd.service; do
  test -f "/etc/systemd/system/${unit}"
  ln -sfn "../${unit}" "${want}/${unit}"
done
if [[ -f /etc/systemd/system/sshd.service || -f /usr/lib/systemd/system/sshd.service ]]; then
  ln -sfn ../sshd.service "${want}/sshd.service"
fi
test -f /etc/systemd/system/toehold-guestinfo-sync.timer
ln -sfn ../toehold-guestinfo-sync.timer "${timer_want}/toehold-guestinfo-sync.timer"

if command -v restorecon >/dev/null 2>&1; then
  restorecon -R /opt/toehold/appliance-root || true
fi

# rhel-guest-image is built for KubeVirt cloud-init; on vSphere the NIC is
# present but NetworkManager has no connection profile, so Tools never reports an IP.
cat > /etc/NetworkManager/system-connections/vsphere-dhcp.nmconnection <<'EOF'
[connection]
id=vsphere-dhcp
type=ethernet
autoconnect=true
autoconnect-priority=100

[ethernet]

[ipv4]
method=auto

[ipv6]
method=ignore
EOF
chmod 600 /etc/NetworkManager/system-connections/vsphere-dhcp.nmconnection

test -f /usr/local/bin/toehold-podman.sh
install -m 755 /usr/local/bin/toehold-podman.sh /usr/local/bin/toehold-podman
ln -sfn /usr/local/bin/toehold-podman /usr/local/bin/podman
ln -sfn /usr/local/bin/toehold-podman /usr/bin/podman
