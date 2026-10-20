#!/usr/bin/env bash
set -euo pipefail
[[ ${EUID} -eq 0 ]] || { echo "run with sudo" >&2; exit 1; }
project_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
"$project_root/../xray-core-helper/packaging/install-bundle.sh" "$@"
helper_socket=$(/usr/local/bin/xray-core-helper installation-settings | python3 -c 'import json,sys; print(json.load(sys.stdin)["socket"])')
settings=$(curl --fail --silent --show-error --unix-socket "$helper_socket" http://helper/v1/web/settings)
base_url=$(node -e 'const s=JSON.parse(process.argv[1]);if(!s.enabled)process.exit(1);console.log("http://"+(s.host.includes(":")?"["+s.host+"]":s.host)+":"+s.port)' "$settings")

echo "[verify] service identity and helper socket access"
for attempt in {1..30}; do
  if curl --silent --fail "$base_url/api/health" >/dev/null; then break; fi
  if [[ $attempt -eq 30 ]]; then
    systemctl status --no-pager xrayui-web.service
    exit 1
  fi
  sleep 1
done
systemctl is-active xrayui-web.service
service_pid=$(systemctl show --property=MainPID --value xrayui-web.service)
test "$(ps -o user= -p "$service_pid" | tr -d ' ')" = xrayui-web
helper_gid=$(getent group xray-core-helper | cut -d: -f3)
grep -Eq "^Groups:.*[[:space:]]${helper_gid}([[:space:]]|$)" "/proc/$service_pid/status"

echo "[verify] every browser GET API"
curl --silent --fail "$base_url/" >/dev/null
curl --silent --fail "$base_url/api/health" >/dev/null
status_json=$(curl --silent --fail "$base_url/api/status")
curl --silent --fail "$base_url/api/config" >/dev/null
curl --silent --fail "$base_url/api/alerts" >/dev/null
curl --silent --fail "$base_url/api/logs/error?limit=3" >/dev/null
curl --silent --fail "$base_url/api/logs/access?limit=3" >/dev/null
curl --silent --fail "$base_url/api/settings/geodata" >/dev/null
curl --silent --fail "$base_url/api/proxy-domains" >/dev/null
outbound_tag=$(node -e 'const value=JSON.parse(process.argv[1]); const item=value.outboundTags?.[0]; process.stdout.write(typeof item === "string" ? item : item?.tag || "")' "$status_json")
if [[ -n $outbound_tag ]]; then
  curl --silent --fail "$base_url/api/outbounds/$outbound_tag/references" >/dev/null
fi

echo "[verify] safe helper-backed mutation"
curl --silent --fail --request POST "$base_url/api/network/start" | node -e '
let input="";
process.stdin.on("data", chunk => input += chunk);
process.stdin.on("end", () => {
  const result = JSON.parse(input);
  if (result.ok !== true) process.exit(1);
  process.stdout.write(JSON.stringify(result) + "\n");
});'

echo "9F_SMOKE_OK"
