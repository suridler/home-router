#!/usr/bin/env bash
set -euo pipefail
[[ ${EUID} -eq 0 ]] || { echo "run with sudo" >&2; exit 1; }
helper_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
# Published bundles use the same standalone installer, journal and recovery as
# the GitHub bootstrap. The source-build path below remains a developer tool.
standalone_prebuilt=0
standalone_arguments=()
for argument in "$@"; do
 if [[ $argument == --prebuilt ]]; then standalone_prebuilt=1
 else standalone_arguments+=("$argument"); fi
done
if ((standalone_prebuilt)); then
 python3 "$helper_root/packaging/verify-release.py" "$helper_root/.."
 exec "$helper_root/bin/xray-core-helper" install --bundle "$helper_root/.." "${standalone_arguments[@]}"
fi

web_mode=${XRAY_HELPER_WEB_ENABLED:-}; web_port=3000; explicit_port=0; prebuilt=0
while (($#)); do
  case "$1" in
    --prebuilt) prebuilt=1; shift;;
    --web-port)
      (($# >= 2)) || { echo "missing option value" >&2; exit 2; }
      web_port=$2; explicit_port=1
      shift 2;;
    --without-web) web_mode=0; shift;; --with-web) web_mode=1; shift;;
    *) echo "usage: $0 [--prebuilt] [--web-port PORT] [--without-web|--with-web]" >&2; exit 2;;
  esac
done
if [[ -z $web_mode && -e /etc/xray-core-helper/helper.env ]]; then web_mode=$(sed -n 's/^XRAY_HELPER_WEB_ENABLED=//p' /etc/xray-core-helper/helper.env); fi
if [[ -z $web_mode && -t 0 && -t 1 ]]; then
  while true; do
    echo 'Установить web-панель?'
    echo '  1) Да'
    echo '  2) Нет, только CLI'
    read -r -p 'Введите номер (Enter — 1): ' web_answer || exit 1
    case ${web_answer:-1} in 1) web_mode=1; break;; 2) web_mode=0; break;; *) echo 'Введите 1 или 2.';; esac
  done
fi
web_mode=${web_mode:-1}
[[ $web_mode == 0 || $web_mode == 1 ]] || { echo "XRAY_HELPER_WEB_ENABLED must be 0 or 1" >&2; exit 2; }
[[ $web_port =~ ^[0-9]{1,5}$ ]] && ((10#$web_port >= 1 && 10#$web_port <= 65535)) || { echo "invalid web port" >&2; exit 2; }
if ((explicit_port)) && [[ -e /etc/xray-core-helper/config.toml ]]; then echo "existing helper config preserved: edit [web].port there instead" >&2; exit 2; fi
for dependency in python3 busctl systemd-sysusers systemd-tmpfiles systemd-analyze systemctl; do command -v "$dependency" >/dev/null || { echo "preflight_binary_unavailable: $dependency" >&2; exit 1; }; done
if [[ $web_mode == 1 ]]; then
  for dependency in node curl; do command -v "$dependency" >/dev/null || { echo "preflight_binary_unavailable: $dependency" >&2; exit 1; }; done
  [[ -x /usr/bin/node ]] || { echo "required: /usr/bin/node" >&2; exit 1; }
  web_root=$(cd -- "$helper_root/../xrayui-web" && pwd)
  [[ -d $web_root/node_modules ]] || { echo "run npm ci --omit=dev in xrayui-web first" >&2; exit 1; }
fi
build_dir=$(mktemp -d /tmp/xray-core-helper-bundle.XXXXXX)
trap 'rm -rf -- "$build_dir"' EXIT
if ((prebuilt)); then
  python3 "$helper_root/packaging/verify-release.py" "$helper_root/.."
  cp -- "$helper_root/bin/xray-core-helperd" "$build_dir/xray-core-helperd"
  cp -- "$helper_root/bin/xray-core-helper" "$build_dir/xray-core-helper"
  cp -- "$helper_root/bin/domain-list-community" "$build_dir/domain-list-community"
  cp -- "$helper_root/licenses/domain-list-community.txt" "$build_dir/domain-list-community.LICENSE"
else
  command -v go >/dev/null || { echo 'preflight_binary_unavailable: go' >&2; exit 1; }
  go -C "$helper_root" build -o "$build_dir/xray-core-helperd" ./cmd/xray-core-helperd
  go -C "$helper_root" build -o "$build_dir/xray-core-helper" ./cmd/xray-core-helper
  python3 "$helper_root/packaging/dlc_builder.py" "$build_dir/domain-list-community" "$build_dir/domain-list-community.LICENSE"
fi
# Discovery and config parsing must finish before installed files/services change.
"$build_dir/xray-core-helper" discover-xray > "$build_dir/discovery.json"
config_input=/etc/xray-core-helper/config.toml
if [[ ! -e $config_input ]]; then
  sed -e "s/^port = 3000$/port = $((10#$web_port))/" "$helper_root/config.example.toml" > "$build_dir/config.toml"
  config_input="$build_dir/config.toml"
fi
"$build_dir/xray-core-helper" -config "$config_input" discover-xray --systemd > "$build_dir/paths.conf"
"$build_dir/xray-core-helper" -config "$config_input" installation-settings > "$build_dir/installation.json"
helper_socket=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["socket"])' "$build_dir/installation.json")
# Retire former maintenance triggers before starting the internal scheduler.
# Do not interrupt an in-flight update or collection; retry after it completes.
for stem in xray-core-helper-routing xray-core-helper-errors update-xray-geodata; do
  if [[ -f /etc/systemd/system/$stem.timer ]]; then systemctl disable --now "$stem.timer"; fi
  if systemctl is-active --quiet "$stem.service"; then
    echo "Maintenance job $stem is still running; wait for completion and retry installation." >&2
    exit 1
  fi
done
for stem in xray-core-helper-routing xray-core-helper-errors update-xray-geodata; do
  rm -f -- "/etc/systemd/system/$stem.timer" "/etc/systemd/system/$stem.service"
done
install -d -o root -g root -m 0700 /var/lib/xray-core-helper-update
systemd-sysusers "$helper_root/packaging/sysusers.d/xray-core-helper.conf"
install -Dm0755 "$build_dir/xray-core-helperd" /usr/local/sbin/xray-core-helperd
install -Dm0755 "$build_dir/xray-core-helper" /usr/local/bin/xray-core-helper
install -Dm0755 "$build_dir/domain-list-community" /usr/local/bin/domain-list-community
install -Dm0644 "$build_dir/domain-list-community.LICENSE" /usr/local/share/licenses/xray-core-helper/domain-list-community.txt
"$helper_root/packaging/install-templconf.sh"
install -d -o root -g xray-core-helper -m 0750 /etc/xray-core-helper
if [[ ! -e /etc/xray-core-helper/config.toml ]]; then
  install -o root -g xray-core-helper -m 0640 "$build_dir/config.toml" /etc/xray-core-helper/config.toml
else echo "preserving /etc/xray-core-helper/config.toml"; fi
printf 'XRAY_HELPER_WEB_ENABLED=%s\n' "$web_mode" > "$build_dir/helper.env"
install -o root -g root -m 0600 "$build_dir/helper.env" /etc/xray-core-helper/helper.env
for unit in xray-core-helper.service xray-core-helper-nft-commit.service xray-core-helper-sysctl.service xray-core-helper-rollback.service xray-core-helper-rollback.timer; do install -Dm0644 "$helper_root/packaging/systemd/$unit" "/etc/systemd/system/$unit"; done
install -Dm0644 "$helper_root/packaging/sysusers.d/xray-core-helper.conf" /etc/sysusers.d/xray-core-helper.conf
install -Dm0644 "$helper_root/packaging/tmpfiles.d/xray-core-helper.conf" /etc/tmpfiles.d/xray-core-helper.conf
systemd-tmpfiles --create /etc/tmpfiles.d/xray-core-helper.conf
if [[ $web_mode == 1 ]]; then
  install -Dm0644 "$web_root/packaging/sysusers.d/xrayui-web.conf" /etc/sysusers.d/xrayui-web.conf
  systemd-sysusers /etc/sysusers.d/xrayui-web.conf
  install -d -o root -g root -m 0755 /opt/xrayui-web
  for entry in app.js server.js package.json package-lock.json config controllers lib middleware public routes services node_modules; do cp -a "$web_root/$entry" /opt/xrayui-web/; done
  chown -R root:root /opt/xrayui-web
  find /opt/xrayui-web -type d -exec chmod 0755 {} +
  find /opt/xrayui-web -type f -exec chmod 0644 {} +
  install -Dm0644 "$web_root/xrayui-web.service" /etc/systemd/system/xrayui-web.service
fi
# Web is a static unit, never independently enabled at boot.
if [[ $(systemctl is-enabled xrayui-web.service 2>/dev/null || true) == enabled ]]; then systemctl disable xrayui-web.service; fi
# The reviewed native script is installed only by explicit setup/apply.
# Own exactly one drop-in; leave administrator files untouched.
dropin=/etc/systemd/system/xray-core-helper.service.d/20-installation-paths.conf
install -d -o root -g root -m 0755 "$(dirname -- "$dropin")"
if [[ -L $dropin || ( -e $dropin && ! -f $dropin ) ]]; then echo 'unsafe installer drop-in' >&2; exit 1; fi
had_dropin=0
if [[ -f $dropin ]]; then cp -p -- "$dropin" "$build_dir/paths.before"; had_dropin=1; fi
restore_dropin() {
  echo 'Installation failed; restoring the previous installer-owned path drop-in. Inspect service state before retry.' >&2
  if ((had_dropin)); then install -m 0644 "$build_dir/paths.before" "$dropin"; else rm -f -- "$dropin"; fi
  systemctl daemon-reload || true
}
trap restore_dropin ERR
install -m 0644 "$build_dir/paths.conf" "$dropin.new"
mv -f -- "$dropin.new" "$dropin"
systemd-analyze verify /etc/systemd/system/xray-core-helper.service /etc/systemd/system/xray-core-helper-rollback.service /etc/systemd/system/xray-core-helper-rollback.timer
if [[ $web_mode == 1 ]]; then systemd-analyze verify /etc/systemd/system/xrayui-web.service; fi
systemctl daemon-reload
systemctl enable xray-core-helper.service
systemctl restart xray-core-helper.service
for attempt in {1..100}; do
  if /usr/local/bin/xray-core-helper health > /dev/null 2>&1; then break; fi
  [[ $attempt -lt 100 ]] || { echo 'Helper не запустился. Проверьте: sudo systemctl status xray-core-helper.service' >&2; exit 1; }; sleep 0.1
done
if [[ $web_mode == 1 ]]; then
  for attempt in {1..100}; do
    if curl --fail --silent --max-time 1 --unix-socket "$helper_socket" http://helper/v1/web/settings > "$build_dir/settings.json"; then break; fi
    [[ $attempt -lt 100 ]] || { echo "helper settings unavailable" >&2; exit 1; }; sleep 0.1
  done
  base_url=$(node -e 'const s=require(process.argv[1]);if(!s.enabled)process.exit(1);console.log(`http://${s.host.includes(":")?`[${s.host}]`:s.host}:${s.port}`)' "$build_dir/settings.json")
  for attempt in {1..100}; do
    if curl --noproxy '*' --fail --silent --max-time 1 "$base_url/" >/dev/null; then break; fi
    [[ $attempt -lt 100 ]] || { echo "web listener unavailable; inspect systemctl status xrayui-web" >&2; exit 1; }; sleep 0.1
  done
  echo "Setup URL: $base_url/"
  echo 'Bootstrap token is stored privately in /var/lib/xray-core-helper/web/bootstrap-token; do not include it in reports.'
else echo "Headless mode: sudo xray-core-helper setup"; fi
trap - ERR
if [[ -t 0 && -t 1 ]]; then
  /usr/local/bin/xray-core-helper setup
else
  echo 'Установка компонентов завершена. Интерактивный setup не запускался (нет TTY). Продолжить: sudo xray-core-helper setup'
fi
