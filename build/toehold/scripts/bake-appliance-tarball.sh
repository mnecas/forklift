#!/usr/bin/env bash
set -euxo pipefail

root=/tmp/appliance-root
tarball=/usr/share/toehold/appliance-root.tar.gz

rm -rf "${root}"
mkdir -p "${root}" "$(dirname "${tarball}")"

dnf install -y \
  --installroot="${root}" \
  --releasever=9 \
  --nodocs \
  --nogpgcheck \
  --setopt=install_weak_deps=False \
  --repofrompath=cs9-baseos,"https://mirror.stream.centos.org/9-stream/BaseOS/x86_64/os/" \
  --repofrompath=cs9-appstream,"https://mirror.stream.centos.org/9-stream/AppStream/x86_64/os/" \
  open-vm-tools \
  podman \
  containernetworking-plugins

test -x "${root}/usr/bin/vmtoolsd"
test -x "${root}/usr/bin/VGAuthService"
test -x "${root}/usr/bin/podman"
test -x "${root}/usr/bin/conmon"
test -f "${root}/etc/containers/storage.conf"
test -f "${root}/etc/containers/policy.json"
conf="${root}/etc/containers"
if [[ ! -f "${conf}/containers.conf" ]]; then
  cat > "${conf}/containers.conf" <<EOF
[engine]
signature_policy = "/opt/toehold/appliance-root/etc/containers/policy.json"
EOF
fi
sed -i 's/^driver = "overlay"/driver = "vfs"/' "${conf}/storage.conf"
if [[ -f "${root}/etc/vmware-tools/tools.conf.example" && ! -f "${root}/etc/vmware-tools/tools.conf" ]]; then
  cp "${root}/etc/vmware-tools/tools.conf.example" "${root}/etc/vmware-tools/tools.conf"
fi

staging=/tmp/appliance-staging
rm -rf "${staging}"
mkdir -p \
  "${staging}/opt/toehold" \
  "${staging}/etc/containers" \
  "${staging}/etc/systemd/system/multi-user.target.wants" \
  "${staging}/etc/systemd/system/timers.target.wants" \
  "${staging}/etc/NetworkManager/system-connections" \
  "${staging}/usr/local/bin" \
  "${staging}/usr/bin"

cp -a "${root}" "${staging}/opt/toehold/appliance-root"
cp /usr/share/toehold/systemd/*.service /usr/share/toehold/systemd/*.timer "${staging}/etc/systemd/system/"
cp /usr/local/bin/toehold-publish-guestinfo.sh /usr/local/bin/toehold-podman.sh "${staging}/usr/local/bin/"
chmod +x "${staging}/usr/local/bin/toehold-publish-guestinfo.sh" "${staging}/usr/local/bin/toehold-podman.sh"
ln -sfn toehold-podman.sh "${staging}/usr/local/bin/toehold-podman"
ln -sfn toehold-podman "${staging}/usr/local/bin/podman"
ln -sfn ../usr/local/bin/toehold-podman "${staging}/usr/bin/podman"

aproot=/opt/toehold/appliance-root
for f in policy.json storage.conf registries.conf; do
  if [[ -f "${staging}${aproot}/etc/containers/${f}" ]]; then
    ln -sfn "${aproot}/etc/containers/${f}" "${staging}/etc/containers/${f}"
  fi
done
ln -sfn "${aproot}/etc/vmware-tools" "${staging}/etc/vmware-tools"

want="${staging}/etc/systemd/system/multi-user.target.wants"
for unit in toehold-vgauthd.service toehold-vmtoolsd.service; do
  ln -sfn "../${unit}" "${want}/${unit}"
done
ln -sfn ../toehold-guestinfo-sync.timer \
  "${staging}/etc/systemd/system/timers.target.wants/toehold-guestinfo-sync.timer"

cat > "${staging}/etc/NetworkManager/system-connections/vsphere-dhcp.nmconnection" <<'EOF'
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
chmod 600 "${staging}/etc/NetworkManager/system-connections/vsphere-dhcp.nmconnection"

tar -czf "${tarball}" -C "${staging}" .
