#!/usr/bin/env bash
set -euo pipefail
[[ ${EUID} -eq 0 ]] || { echo "run with sudo" >&2; exit 1; }
systemctl disable --now xray-core-helper-routing.timer xray-core-helper-routing.service xray-core-helper-errors.timer xray-core-helper-errors.service xrayui-web.service xray-core-helper.service update-xray-geodata.timer 2>/dev/null || true
for path in /etc/systemd/system/xray-core-helper-routing.service /etc/systemd/system/xray-core-helper-routing.timer /etc/systemd/system/xray-core-helper-errors.service /etc/systemd/system/xray-core-helper-errors.timer /etc/systemd/system/xrayui-web.service /etc/systemd/system/xray-core-helper.service /etc/systemd/system/xray-core-helper-nft-commit.service /etc/systemd/system/xray-core-helper-sysctl.service /etc/systemd/system/xray-core-helper-rollback.service /etc/systemd/system/xray-core-helper-rollback.timer /etc/systemd/system/update-xray-geodata.service /etc/systemd/system/update-xray-geodata.timer /usr/local/sbin/xray-core-helperd /usr/local/bin/xray-core-helper; do rm -f -- "$path"; done
rm -rf -- /opt/xrayui-web
rm -f -- /usr/local/bin/domain-list-community /usr/local/share/licenses/xray-core-helper/domain-list-community.txt
systemctl daemon-reload
echo "Preserved: /etc/xray-core-helper /etc/xrayui-web /var/lib/xray-core-helper and native system configuration"
