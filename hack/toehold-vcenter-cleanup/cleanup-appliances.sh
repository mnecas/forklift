#!/usr/bin/env bash
set -euo pipefail
eval "$(
  oc get secret vsphere-nfc-dc82g -n openshift-mtv -o json | python3 -c "
import json, sys, base64, shlex
s = json.load(sys.stdin)['data']
for k, env in [('url','VCENTER_URL'),('user','VCENTER_USER'),('password','VCENTER_PASSWORD')]:
    print(f'export {env}={shlex.quote(base64.b64decode(s[k]).decode())}')
print('export VCENTER_INSECURE=true')
"
)"
cd "$(dirname "$0")/../.."
go run ./hack/toehold-vcenter-cleanup/main.go cleanup-appliances
