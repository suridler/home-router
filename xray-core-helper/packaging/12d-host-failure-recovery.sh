#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID} -eq 0 ]]; then
  echo "run as the trusted operator, not root" >&2
  exit 1
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
socket=/run/xray-core-helper/helper.sock
web_base=http://127.0.0.1:3000
stamp=$(date -u +%Y%m%dT%H%M%SZ)
audit_root=${XCH_12D_AUDIT_ROOT:-/tmp/xray-core-helper-12d}
audit="$audit_root/$stamp"
mkdir -p "$audit"
chmod 0700 "$audit"

helper_get() {
  curl --fail --silent --show-error --unix-socket "$socket" "http://helper$1"
}

assert_live() {
  helper_get /v1/ready | jq -e '.status == "ready"' >/dev/null
  local diagnostics
  diagnostics=$(helper_get /v1/xray/diagnostics)
  for check in policy_rule policy_route nft_runtime native_relationships xray_service_active tproxy_service_active; do
    jq -e --arg check "$check" '.checks[] | select(.name == $check and .status == "ok")' <<<"$diagnostics" >/dev/null
  done
  systemctl is-active --quiet xray.service xray-tproxy-routing.service xray-core-helper.service xrayui-web.service xray-routing-exporter.service
}

echo "[preflight] live state and routing"
assert_live
before=$(helper_get /v1/xray/status)
generation=$(jq -r '.generation' <<<"$before")
content_hash=$(jq -r '.contentHash' <<<"$before")
[[ $generation =~ ^[1-9][0-9]*$ ]]

echo "[live] stale generation is rejected without mutation"
logging=$(helper_get /v1/xray/logging)
payload=$(jq -cn \
  --argjson generation "$((generation - 1))" \
  --arg access "$(jq -r '.access' <<<"$logging")" \
  --arg error "$(jq -r '.error' <<<"$logging")" \
  --arg level "$(jq -r '.loglevel' <<<"$logging")" \
  --argjson dns "$(jq -r '.dnsLog' <<<"$logging")" \
  '{expectedGeneration:$generation,restart:false,access:{mode:(if $access == "/var/log/xray/access.log" then "file" elif $access == "none" then "disabled" else "journal" end)},error:{mode:(if $error == "/var/log/xray/error.log" then "file" elif $error == "none" then "disabled" else "journal" end)},loglevel:$level,dnsLog:$dns}')
stale_code=$(curl --silent --show-error --request PUT --header 'Content-Type: application/json' \
  --data "$payload" --output "$audit/stale-generation.json" --write-out '%{http_code}' \
  "$web_base/api/settings/logs")
[[ $stale_code == 409 ]]
jq -e '.ok == false and .code == "generation_conflict"' "$audit/stale-generation.json" >/dev/null

echo "[fixtures] validation failure and restart rollback"
cd "$repo_root"
GOCACHE=${XCH_12D_GOCACHE:-/tmp/xray-core-helper-12d-go-cache} \
  go test -count=1 -run 'Test(ValidationFailureLeavesOriginalBytesAndOwnership|RestartFailureRestoresExactPreviousFileAndRestartsRollback|FailedRollbackRemainsRecoverable)$' \
  ./internal/transaction | tee "$audit/transaction-tests.txt"

echo "[fixtures] persisted runtime-sync failure and recovery"
GOCACHE=${XCH_12D_GOCACHE:-/tmp/xray-core-helper-12d-go-cache} \
  go test -count=1 -run 'TestFailedRuntimeSyncReportsPossiblyPersistedMutationAndApplyRecovers$' \
  ./internal/domainservice | tee "$audit/domain-tests.txt"

echo "[fixtures] pending geodata rollback and recovery gate"
GOCACHE=${XCH_12D_GOCACHE:-/tmp/xray-core-helper-12d-go-cache} \
  go test -count=1 -run 'Test(RollbackFailureRetainsEvidenceAndCanBeRetried|ExecutorMarksRollbackFailureRecoveryRequired)$' \
  ./internal/geodataupdate ./internal/domainstore ./internal/geodataadmin | tee "$audit/geodata-tests.txt"

echo "[verify] production state was not changed by failure fixtures"
after=$(helper_get /v1/xray/status)
[[ $(jq -r '.generation' <<<"$after") == "$generation" ]]
[[ $(jq -r '.contentHash' <<<"$after") == "$content_hash" ]]
[[ $(jq -r '.configState' <<<"$after") == parsed ]]
assert_live

echo "12D_FAILURE_RECOVERY_OK generation=$generation audit=$audit"
