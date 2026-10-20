#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "run as root: sudo $0 {pre-reboot|post-reboot}" >&2
  exit 1
fi

phase=${1:-}
case $phase in
  pre-reboot|post-reboot) ;;
  *) echo "usage: $0 {pre-reboot|post-reboot}" >&2; exit 2 ;;
esac

helper_socket=/run/xray-core-helper/helper.sock
web_base=http://127.0.0.1:3000
exporter_base=http://127.0.0.1:9101
traffic_url=${XCH_12E_TRAFFIC_URL:-https://connectivitycheck.gstatic.com/generate_204}
state_root=/var/backups/xray-core-helper-12e
pending=$state_root/pending-reboot.json
stamp=$(date -u +%Y%m%dT%H%M%SZ)
audit=$state_root/$stamp
install -d -o root -g root -m 0700 "$audit"

helper_get() {
  curl --fail --silent --show-error --max-time 30 --unix-socket "$helper_socket" "http://helper$1"
}

wait_for_socket() {
  for _ in {1..300}; do
    [[ -S $helper_socket ]] && return 0
    sleep 0.1
  done
  return 1
}

wait_for_http() {
  local url=$1
  for _ in {1..300}; do
    curl --fail --silent --output /dev/null --max-time 2 "$url" && return 0
    sleep 0.1
  done
  return 1
}

assert_services() {
  systemctl is-active --quiet \
    xray.service xray-tproxy-routing.service xray-core-helper.service \
    xrayui-web.service xray-routing-exporter.service
}

assert_routing() {
  local diagnostics
  diagnostics=$(helper_get /v1/xray/diagnostics)
  for check in policy_rule policy_route nft_runtime native_relationships xray_service_active tproxy_service_active; do
    jq -e --arg check "$check" '.checks[] | select(.name == $check and .status == "ok")' <<<"$diagnostics" >/dev/null
  done
}

generate_traffic() {
  local label=$1 code
  code=$(curl --silent --show-error --location --max-time 20 --output /dev/null --write-out '%{http_code}' "$traffic_url")
  [[ $code =~ ^(200|204)$ ]]
  printf '%s traffic_http=%s\n' "$label" "$code" | tee -a "$audit/traffic.txt"
  systemctl is-active --quiet xray.service xray-tproxy-routing.service
}

service_timestamp() {
  journalctl --boot --unit "$1" --output=json --no-pager \
    | jq -sr 'map(select((.MESSAGE? // "") | startswith("Started "))) | first | .__MONOTONIC_TIMESTAMP // empty'
}

if [[ $phase == pre-reboot ]]; then
  cleanup() {
    local result=$?
    trap - EXIT
    systemctl start xray-core-helper.service xrayui-web.service >/dev/null 2>&1 || true
    exit "$result"
  }
  trap cleanup EXIT

  echo "[preflight] healthy connected stack"
  assert_services
  wait_for_socket
  helper_get /v1/ready | tee "$audit/helper-ready.before.json" | jq -e '.status == "ready"' >/dev/null
  assert_routing
  curl --fail --silent --show-error --max-time 10 "$exporter_base/readyz" \
    | tee "$audit/exporter-ready.before.json" | jq -e '.status == "ready"' >/dev/null
  generate_traffic baseline

  echo "[availability] stop web only"
  systemctl stop xrayui-web.service
  systemctl is-active --quiet xray.service xray-tproxy-routing.service xray-core-helper.service xray-routing-exporter.service
  generate_traffic web-stopped
  systemctl start xrayui-web.service
  wait_for_http "$web_base/api/health"

  echo "[availability] stop helper only"
  systemctl stop xray-core-helper.service
  [[ ! -S $helper_socket ]]
  systemctl is-active --quiet xray.service xray-tproxy-routing.service xrayui-web.service xray-routing-exporter.service
  generate_traffic helper-stopped
  curl --fail --silent --show-error --max-time 10 "$exporter_base/readyz" \
    | tee "$audit/exporter-ready.helper-stopped.json" | jq -e '.status == "ready"' >/dev/null
  systemctl start xray-core-helper.service
  wait_for_socket
  helper_get /v1/ready | tee "$audit/helper-ready.restored.json" | jq -e '.status == "ready"' >/dev/null
  wait_for_http "$web_base/api/health"
  assert_services
  assert_routing

  status=$(helper_get /v1/xray/status)
  exporter=$(curl --fail --silent --show-error --max-time 10 "$exporter_base/readyz")
  jq -n \
    --arg bootId "$(cat /proc/sys/kernel/random/boot_id)" \
    --argjson generation "$(jq -r '.generation' <<<"$status")" \
    --arg contentHash "$(jq -r '.contentHash' <<<"$status")" \
    --arg exporterMetadata "$(jq -r '.metadata' <<<"$exporter")" \
    --arg capturedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{bootId:$bootId,generation:$generation,contentHash:$contentHash,exporterMetadata:$exporterMetadata,capturedAt:$capturedAt}' \
    >"$audit/pre-reboot.json"
  install -o root -g root -m 0600 "$audit/pre-reboot.json" "$pending"
  trap - EXIT
  echo "12E_PRE_REBOOT_OK audit=$audit"
  echo "Run: sudo systemctl reboot"
  echo "After reconnect: sudo $0 post-reboot"
  exit 0
fi

echo "[post-reboot] baseline and new boot"
[[ -f $pending ]]
before_boot=$(jq -r '.bootId' "$pending")
current_boot=$(cat /proc/sys/kernel/random/boot_id)
[[ -n $before_boot && $before_boot != "$current_boot" ]]

echo "[post-reboot] services, sockets and readiness"
assert_services
wait_for_socket
wait_for_http "$web_base/api/health"
helper_get /v1/ready | tee "$audit/helper-ready.json" | jq -e '.status == "ready"' >/dev/null
assert_routing
exporter=$(curl --fail --silent --show-error --max-time 10 "$exporter_base/readyz")
jq -e '.status == "ready" and (.metadata == "fresh" or .metadata == "stale")' <<<"$exporter" >/dev/null
printf '%s\n' "$exporter" >"$audit/exporter-ready.json"
curl --fail --silent --show-error --max-time 20 "$exporter_base/api/v1/xray/stats" >"$audit/xray-stats.json"

echo "[post-reboot] configuration and recovery continuity"
status=$(helper_get /v1/xray/status)
[[ $(jq -r '.generation' <<<"$status") == "$(jq -r '.generation' "$pending")" ]]
[[ $(jq -r '.contentHash' <<<"$status") == "$(jq -r '.contentHash' "$pending")" ]]
[[ $(jq -r '.configState' <<<"$status") == parsed ]]
helper_get /v1/xray/geodata/update/status | tee "$audit/geodata-status.json" \
  | jq -e '.recovery.phase == "clean" and .recovery.code == "none" and .recovery.mutationsBlocked == false' >/dev/null

echo "[post-reboot] ordering and timers"
exporter_started=$(service_timestamp xray-routing-exporter.service)
xray_started=$(service_timestamp xray.service)
helper_started=$(service_timestamp xray-core-helper.service)
web_started=$(service_timestamp xrayui-web.service)
[[ $exporter_started =~ ^[0-9]+$ && $xray_started =~ ^[0-9]+$ && $helper_started =~ ^[0-9]+$ && $web_started =~ ^[0-9]+$ ]]
(( exporter_started <= xray_started ))
(( xray_started <= helper_started ))
(( helper_started <= web_started ))
# Maintenance scheduling belongs to the running helper; only rollback remains external.
for timer in update-xray-geodata.timer xray-core-helper-errors.timer xray-core-helper-routing.timer; do
  ! systemctl is-active --quiet "$timer"
done

generate_traffic post-reboot
rm -f "$pending"
echo "12E_POST_REBOOT_OK audit=$audit exporter_metadata=$(jq -r '.metadata' <<<"$exporter")"
