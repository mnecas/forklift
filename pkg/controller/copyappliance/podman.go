package copyappliance

import (
	"strings"

	"golang.org/x/crypto/ssh"
)

// toeholdPodmanScript installs the podman wrapper used by toehold appliance
// templates that ship /opt/toehold/appliance-root but omit the symlinks.
const toeholdPodmanScript = `#!/bin/bash
set -euo pipefail
root=/opt/toehold/appliance-root
conf="${root}/etc/containers"
export PATH="${root}/usr/bin:${root}/usr/sbin:${PATH}"
if [[ -x "${root}/usr/bin/conmon" ]]; then
  export CONMON_BINARY="${root}/usr/bin/conmon"
fi
exec env \
  LD_LIBRARY_PATH="${root}/usr/lib64:${root}/usr/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}" \
  CONTAINERS_STORAGE_CONF="${conf}/storage.conf" \
  CONTAINERS_CONF="${conf}/containers.conf" \
  CONTAINERS_POLICY="${conf}/policy.json" \
  "${root}/usr/bin/podman" --storage-driver vfs "$@"
`

// EnsurePodmanWrapper makes podman usable on toehold-based appliance guests.
// Older templates may ship the installroot without /usr/bin/podman symlinks.
func (r *ApplianceContext) EnsurePodmanWrapper(client *ssh.Client) (err error) {
	ok, err := r.Probe(client, "test -x /opt/toehold/appliance-root/usr/bin/podman")
	if err != nil || !ok {
		return err
	}
	err = r.RunWithStdin(client,
		"(umask 022 && install -m 755 /dev/stdin /usr/local/bin/toehold-podman)",
		strings.NewReader(toeholdPodmanScript))
	if err != nil {
		return err
	}
	err = r.RunCommands(client,
		"ln -sfn /usr/local/bin/toehold-podman /usr/local/bin/podman",
		"ln -sfn /usr/local/bin/toehold-podman /usr/bin/podman")
	if err != nil {
		return err
	}
	// Older toehold templates may omit policy.json; podman load requires one.
	return r.RunCommands(client,
		"install -d -m 755 /etc/containers",
		"if test -f /opt/toehold/appliance-root/etc/containers/policy.json; then "+
			"ln -sfn /opt/toehold/appliance-root/etc/containers/policy.json /etc/containers/policy.json; "+
			"else printf '%s\\n' '{\"default\":[{\"type\":\"insecureAcceptAnything\"}]}' > /etc/containers/policy.json; fi",
		"podman --version")
}
