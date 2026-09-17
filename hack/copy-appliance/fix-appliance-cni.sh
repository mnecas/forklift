#!/usr/bin/env bash
# Install host CNI plugins so podman can start nbd containers on toehold guests.
set -euo pipefail

address="${1:?appliance IP required}"
namespace="${2:-openshift-mtv}"
provider="${3:-vsphere-q01}"
secret="toehold-ssh-keys-${provider}-private"
tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

oc get secret "${secret}" -n "${namespace}" -o jsonpath='{.data.private-key}' | base64 -d > "${tmpdir}/private-key"
chmod 600 "${tmpdir}/private-key"

ssh -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "root@${address}" bash -s <<'REMOTE'
set -eux
if ! test -x /usr/libexec/cni/bridge; then
  cat > /etc/yum.repos.d/cs9-appstream.repo <<'EOF'
[cs9-baseos]
name=CentOS Stream 9 BaseOS
baseurl=https://mirror.stream.centos.org/9-stream/BaseOS/x86_64/os/
enabled=1
gpgcheck=0

[cs9-appstream]
name=CentOS Stream 9 AppStream
baseurl=https://mirror.stream.centos.org/9-stream/AppStream/x86_64/os/
enabled=1
gpgcheck=0
EOF
  dnf install -y containernetworking-plugins iptables
elif ! command -v iptables >/dev/null; then
  cat > /etc/yum.repos.d/cs9-baseos.repo <<'EOF'
[cs9-baseos]
name=CentOS Stream 9 BaseOS
baseurl=https://mirror.stream.centos.org/9-stream/BaseOS/x86_64/os/
enabled=1
gpgcheck=0
EOF
  dnf install -y iptables
fi
test -x /usr/libexec/cni/bridge
command -v iptables
podman rm -f nbd-36000c29d4f4ee0e88ebf0954af63d2ed 2>/dev/null || true
podman run -d --restart=always --network=host --privileged --cgroups=disabled \
  --security-opt label=disable --name nbd-36000c29d4f4ee0e88ebf0954af63d2ed \
  --label nbd.wwid=36000c29d4f4ee0e88ebf0954af63d2ed --label nbd.device=/dev/sdb \
  --device /dev/sdb:/dev/nbd-export:r --entrypoint nbdkit \
  localhost/forklift-copy-appliance:fe964d5ed8f0 \
  --foreground --readonly --port 10809 file /dev/nbd-export
sleep 3
podman ps -a
ss -tlnp | grep 10809 || true
systemctl restart nbd-orchestrator
sleep 5
journalctl -u nbd-orchestrator --no-pager -n 15
podman ps -a
ss -tlnp | grep -E '8443|10809' || true
REMOTE
