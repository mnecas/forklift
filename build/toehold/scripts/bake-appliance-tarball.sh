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

staging=/tmp/appliance-staging
rm -rf "${staging}"
mkdir -p "${staging}/opt/toehold" "${staging}/etc/systemd/system" "${staging}/usr/local/bin" "${staging}/usr/bin"
cp -a "${root}" "${staging}/opt/toehold/appliance-root"
cp /usr/share/toehold/systemd/*.service /usr/share/toehold/systemd/*.timer "${staging}/etc/systemd/system/"
cp /usr/local/bin/toehold-publish-guestinfo.sh /usr/local/bin/toehold-podman.sh "${staging}/usr/local/bin/"
chmod +x "${staging}/usr/local/bin/toehold-publish-guestinfo.sh" "${staging}/usr/local/bin/toehold-podman.sh"
ln -sfn toehold-podman.sh "${staging}/usr/local/bin/toehold-podman"
ln -sfn toehold-podman "${staging}/usr/local/bin/podman"
ln -sfn ../usr/local/bin/toehold-podman "${staging}/usr/bin/podman"

tar -czf "${tarball}" -C "${staging}" .
