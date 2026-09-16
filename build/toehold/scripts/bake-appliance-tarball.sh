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
  open-vm-tools

test -x "${root}/usr/bin/vmtoolsd"
test -x "${root}/usr/bin/VGAuthService"

staging=/tmp/appliance-staging
rm -rf "${staging}"
mkdir -p "${staging}/opt/toehold" "${staging}/etc/systemd/system"
cp -a "${root}" "${staging}/opt/toehold/appliance-root"
cp /usr/share/toehold/systemd/*.service "${staging}/etc/systemd/system/"

tar -czf "${tarball}" -C "${staging}" .
