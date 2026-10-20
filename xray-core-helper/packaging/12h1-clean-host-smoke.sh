#!/usr/bin/env bash
set -euo pipefail
[[ ${EUID} -eq 0 ]] || { echo "run with sudo" >&2; exit 1; }
[[ ${XCH_DISPOSABLE_HOST:-} == YES ]] || { echo "refusing: set XCH_DISPOSABLE_HOST=YES only on a disposable clean host" >&2; exit 1; }
mode=${1:-}; scenario=${2:-}; interface_name=${3:-}
[[ $mode == cli || $mode == web ]] || { echo "usage: $0 {cli|web} {existing|create} [interface for create]" >&2; exit 2; }
[[ $scenario == existing || $scenario == create ]] || { echo 'choose existing or create scenario explicitly' >&2; exit 2; }
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
# A clean helper install may coexist with a configured Xray. Never erase evidence.
for artifact in /etc/xray-core-helper /var/lib/xray-core-helper /run/xray-core-helper /opt/xrayui-web /usr/local/bin/xray-core-helper /usr/local/libexec/xray-core-helper-nft-load /usr/local/sbin/xray-core-helperd /etc/systemd/system/xray-core-helper* /etc/systemd/system/xrayui-web* /etc/sysusers.d/xray-core-helper.conf /etc/tmpfiles.d/xray-core-helper.conf /usr/share/xray-core-helper; do
  [[ ! -e $artifact && ! -L $artifact ]] || { echo "previous helper installation found: $artifact; use a fresh instance/snapshot" >&2; exit 1; }
done
build_dir=$(mktemp -d /tmp/xray-clean-preflight.XXXXXX)
trap 'rm -rf -- "$build_dir"' EXIT
bundle_args=()
if [[ -f $root/../release.json ]]; then
  python3 "$root/packaging/verify-release.py" "$root/.."
  cp -- "$root/bin/xray-core-helper" "$build_dir/helper"
  bundle_args+=(--prebuilt)
else
  go -C "$root" build -o "$build_dir/helper" ./cmd/xray-core-helper
fi
"$build_dir/helper" discover-xray > "$build_dir/discovery.json"
config=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["config"])' "$build_dir/discovery.json")
xray_binary=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["binary"])' "$build_dir/discovery.json")
asset_dir=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["assetDir"])' "$build_dir/discovery.json")
stamp=$(date -u +%Y%m%dT%H%M%SZ); audit=/var/backups/xray-core-helper-12h1/$mode-$stamp
install -d -o root -g root -m 0700 "$audit"
cp "$build_dir/discovery.json" "$audit/discovery.json"
python3 - "$config" "$audit/before.json" <<'PYBASE'
import hashlib, json, os, stat, sys
from pathlib import Path
paths=[sys.argv[1], '/etc/nftables.conf','/etc/sysctl.d/99-xray.conf','/etc/systemd/system/xray-tproxy-routing.service']
manifest={}
for name in paths:
    p=Path(name)
    if not p.exists(): manifest[name]=None; continue
    info=p.lstat()
    if not stat.S_ISREG(info.st_mode): raise SystemExit('baseline must be regular files')
    manifest[name]=dict(sha256=hashlib.sha256(p.read_bytes()).hexdigest(),mode=stat.S_IMODE(info.st_mode),uid=info.st_uid,gid=info.st_gid)
Path(sys.argv[2]).write_text(json.dumps(manifest,indent=2))
PYBASE
args=()
[[ $mode != cli ]] || args+=(--without-web)
"$root/packaging/install-bundle.sh" "${bundle_args[@]}" "${args[@]}" | sed '/^Bootstrap token:/d' | tee "$audit/install.txt"
helper_socket=$(/usr/local/bin/xray-core-helper installation-settings | python3 -c 'import json,sys; print(json.load(sys.stdin)["socket"])')
settings=$(curl --fail --silent --unix-socket "$helper_socket" http://helper/v1/web/settings)
curl --fail --silent --unix-socket "$helper_socket" http://helper/v1/setup > "$audit/initial-setup.json"
jq -e '.setupRequired == true and .recoveryRequired == false' "$audit/initial-setup.json" >/dev/null
if [[ $scenario == existing ]]; then
  jq -e '.configState == "existing" and (.configFingerprint|length == 64)' "$audit/initial-setup.json" >/dev/null
else
  jq -e '.configState != "existing"' "$audit/initial-setup.json" >/dev/null
  [[ -n $interface_name ]] || { echo 'create scenario requires explicit interface' >&2; exit 1; }
fi
if [[ $mode == cli ]]; then
  if [[ $scenario == existing ]]; then
    /usr/local/bin/xray-core-helper setup --adopt-existing --non-interactive | tee "$audit/setup.json"
  else
    /usr/local/bin/xray-core-helper setup --interface "$interface_name" --non-interactive | tee "$audit/setup.json"
  fi
else
  token=$(sed -n 's/^XRAYUI_BOOTSTRAP_TOKEN=//p' /run/xray-core-helper/web.env)
  host=$(jq -r .host <<<"$settings")
  [[ -n $token && -n $host ]]
  port=$(jq -r .port <<<"$settings")
  if [[ $host == *:* ]]; then host="[$host]"; fi
  base="http://$host:$port"
  for _ in {1..100}; do curl --fail --silent --max-time 2 "$base/api/setup" >"$audit/setup-status.json" && break; sleep 0.1; done
  if [[ $scenario == existing ]]; then
    jq '{configFingerprint}' "$audit/initial-setup.json" | curl --fail --silent --show-error -H "X-Bootstrap-Token: $token" -H 'Content-Type: application/json' --data-binary @- "$base/api/setup/adopt" | tee "$audit/setup.json"
  else
  curl --fail --silent --show-error -H "X-Bootstrap-Token: $token" "$base/api/setup/interfaces" >"$audit/interfaces.json"
  curl --fail --silent --show-error -H "X-Bootstrap-Token: $token" -H 'Content-Type: application/json' -d "{\"interface\":\"$interface_name\"}" "$base/api/setup/plan" >"$audit/plan.json"
  jq '.plan' "$audit/plan.json" | curl --fail --silent --show-error -H "X-Bootstrap-Token: $token" -H 'Content-Type: application/json' --data-binary @- "$base/api/setup/apply" | tee "$audit/setup.json"
  fi
fi
jq -e '.status == "configured_direct" and .routingEnabled == false' "$audit/setup.json" >/dev/null
# Observe automatic mode handoff; a manual restart would mask an R11 failure.
for _ in {1..100}; do
  curl --fail --silent --max-time 2 --unix-socket "$helper_socket" http://helper/v1/setup > "$audit/final-setup-status.json" && jq -e '.setupRequired == false and .operation.status == "configured_direct"' "$audit/final-setup-status.json" >/dev/null && break
  sleep 0.2
done
jq -e '.setupRequired == false and .operation.status == "configured_direct" and (.operation.id | length > 0)' "$audit/final-setup-status.json" >/dev/null
for service in xray.service xray-core-helper.service; do systemctl is-active --quiet "$service"; done
if [[ $mode == web ]]; then systemctl is-active --quiet xrayui-web.service; else ! systemctl is-active --quiet xrayui-web.service; fi
if [[ $scenario == existing ]]; then
python3 - "$audit/before.json" <<'PYVERIFY'
import hashlib,json,stat,sys
from pathlib import Path
for name,expected in json.loads(Path(sys.argv[1]).read_text()).items():
    p=Path(name)
    if expected is None:
        if p.exists() or p.is_symlink(): raise SystemExit('adoption created native file')
        continue
    i=p.lstat()
    actual=dict(sha256=hashlib.sha256(p.read_bytes()).hexdigest(),mode=stat.S_IMODE(i.st_mode),uid=i.st_uid,gid=i.st_gid)
    if actual!=expected: raise SystemExit('adoption changed baseline')
PYVERIFY
else
  ! systemctl is-active --quiet xray-tproxy-routing.service
  ! ip rule show | grep -Eq 'fwmark 0x1.*lookup 100|fwmark 0x1.*table 100'
  if nft list table inet xray >/dev/null 2>&1; then echo 'unexpected active inet xray table' >&2; exit 1; fi
fi
if ! XRAY_LOCATION_ASSET="$asset_dir" "$xray_binary" run -test -config "$config" >/dev/null 2>&1; then echo 'Xray native validation failed; inspect locally' >&2; exit 1; fi
curl --fail --silent --unix-socket "$helper_socket" http://helper/v1/health >"$audit/helper-health.json"
if [[ $mode == web ]]; then
  for _ in {1..100}; do curl --silent --max-time 2 "$base/api/setup" > "$audit/web-final-status.json" && jq -e '.setupRequired == false' "$audit/web-final-status.json" >/dev/null && break; sleep 0.2; done
  jq -e '.setupRequired == false' "$audit/web-final-status.json" >/dev/null
  revoked_http=$(curl --silent --max-time 5 -o /dev/null -w '%{http_code}' -H "X-Bootstrap-Token: $token" "$base/api/setup/interfaces")
  [[ $revoked_http == 401 || $revoked_http == 410 ]]
  unset token
fi
echo "12H1_CLEAN_HOST_OK mode=$mode scenario=$scenario audit=$audit baseline=$audit/before.json"
