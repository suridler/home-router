#!/usr/bin/env bash
# Minimal U6 bootstrap: detect the Linux architecture, select one exact release,
# download the raw CLI asset, verify its checksum and execute the standalone CLI
# installer. No archive extraction or package-manager helpers are used.
set -euo pipefail
umask 077

readonly repository=suridler/home-router
readonly api_url="https://api.github.com/repos/$repository/releases"
readonly download_url="https://github.com/$repository/releases/download"
readonly update_dir=/var/lib/xray-core-helper-update
readonly installer=$update_dir/installer
readonly installer_checksum=$update_dir/installer.sha256
readonly max_pages=100
readonly max_metadata_page_bytes=$((8 * 1024 * 1024))
readonly max_metadata_total_bytes=$((32 * 1024 * 1024))
readonly max_checksum_bytes=$((1024 * 1024))
readonly max_cli_bytes=$((256 * 1024 * 1024))

version=
recover=0
web_option=

usage() {
  printf '%s\n' \
    "usage: $0 [--version helper-vVERSION] [--with-web|--without-web] [--recover]" >&2
}

fail() {
  printf 'bootstrap: %s\n' "$*" >&2
  exit 1
}

invalid_arguments() {
  usage
  exit 2
}

valid_version() {
  [[ $1 =~ ^helper-v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]
}

while (($#)); do
  case $1 in
    --version)
      (($# >= 2)) || invalid_arguments
      [[ -z $version ]] || invalid_arguments
      version=$2
      shift 2
      ;;
    --recover)
      ((recover == 0)) || invalid_arguments
      recover=1
      shift
      ;;
    --with-web|--without-web)
      [[ -z $web_option ]] || invalid_arguments
      web_option=$1
      shift
      ;;
    *) invalid_arguments;;
  esac
done

if ((recover)); then
  [[ -z $version ]] || {
    printf '%s\n' 'bootstrap: --recover does not accept --version' >&2
    exit 2
  }
elif [[ -n $version ]]; then
  valid_version "$version" || {
    printf '%s\n' 'bootstrap: invalid release version' >&2
    exit 2
  }
fi

if ((EUID != 0)); then
  command -v sudo >/dev/null 2>&1 || fail 'sudo is required for installation'
fi

run_as_root() {
  if ((EUID == 0)); then
    "$@"
  else
    sudo "$@"
  fi
}

path_info() {
  local info type uid mode
  info=$(run_as_root stat -c '%F:%u:%a' -- "$1" 2>/dev/null) || return 1
  IFS=: read -r type uid mode <<<"$info"
  [[ -n $type && $uid =~ ^[0-9]+$ && $mode =~ ^[0-7]{3,4}$ ]] || return 1
  printf '%s:%s:%s\n' "$type" "$uid" "$mode"
}

trusted_directory() {
  local info type uid mode
  info=$(path_info "$1") || return 1
  IFS=: read -r type uid mode <<<"$info"
  [[ $type == directory && $uid == 0 ]] || return 1
  (( (8#$mode & 8#022) == 0 ))
}

trusted_regular_file() {
  local executable=$2 info type uid mode
  info=$(path_info "$1") || return 1
  IFS=: read -r type uid mode <<<"$info"
  [[ $type == 'regular file' && $uid == 0 ]] || return 1
  (( (8#$mode & 8#022) == 0 )) || return 1
  if ((executable)); then
    (( (8#$mode & 8#100) != 0 ))
  fi
}

verify_saved_installer() {
  trusted_directory "$update_dir" || return 1
  trusted_regular_file "$installer" 1 || return 1
  trusted_regular_file "$installer_checksum" 0 || return 1
  local sums line digest name extra count=0 expected=
  sums=$(run_as_root cat -- "$installer_checksum") || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    [[ -n $line ]] || return 1
    read -r digest name extra <<<"$line"
    [[ $digest =~ ^[0-9a-f]{64}$ && -z ${extra:-} ]] || return 1
    if [[ -n ${name:-} ]]; then
      name=${name#\*}
      [[ $name == installer || $name == "$installer" ]] || return 1
    fi
    count=$((count + 1))
    expected=$digest
  done <<<"$sums"
  ((count == 1)) || return 1
  local actual
  actual=$(run_as_root sha256sum -- "$installer") || return 1
  actual=${actual%%[[:space:]]*}
  [[ $actual == "$expected" ]]
}

if ((recover)); then
  verify_saved_installer \
    || fail 'saved verified installer is unavailable; recovery did not use the network'
  recovery_args=(install --recover)
  [[ -n $web_option ]] && recovery_args+=("$web_option")
  run_as_root "$installer" "${recovery_args[@]}"
  exit 0
fi

detect_arch() {
  local system machine
  system=$(uname -s) || return 1
  machine=$(uname -m) || return 1
  [[ $system == Linux ]] || return 1
  case $machine in
    x86_64|amd64) printf 'amd64\n';;
    aarch64|arm64) printf 'arm64\n';;
    *) return 1;;
  esac
}

arch=$(detect_arch) || fail 'unsupported platform: Linux amd64/arm64 required'
command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'

select_version() {
  local arch=$1 directory=$2 page count total=0 output size
  mkdir -p -- "$directory"
  for ((page = 1; page <= max_pages; page++)); do
    output=$directory/page-$page.json
    if ! curl --fail --silent --show-error --location --proto '=https' \
      --proto-redir '=https' --tlsv1.2 \
      --connect-timeout 10 --max-time 120 --max-filesize "$max_metadata_page_bytes" \
      --user-agent 'xray-core-helper-bootstrap' \
      --output "$output" "$api_url?per_page=100&page=$page"; then
      fail 'release metadata request failed; refusing to guess a version'
    fi
    count=$(python3 - "$output" "$max_metadata_page_bytes" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
limit = int(sys.argv[2])
try:
    if not path.is_file() or path.stat().st_size > limit:
        raise ValueError
    releases = json.loads(path.read_text(encoding='utf-8'))
except (OSError, ValueError, UnicodeError):
    raise SystemExit(1)
if not isinstance(releases, list) or len(releases) > 100:
    raise SystemExit(1)
print(len(releases))
PY
    ) || fail 'invalid release metadata'
    read -r size < <(wc -c < "$output")
    total=$((total + size))
    ((total <= max_metadata_total_bytes)) || fail 'release metadata is too large'
    ((count < 100)) && break
  done
  python3 - "$arch" "$directory"/page-*.json <<'PY'
import json
import re
import sys
from pathlib import Path

TAG = re.compile(
    r'^helper-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
    r'(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$')
MAX_PAGE = 8 * 1024 * 1024


def parse(tag):
    match = TAG.fullmatch(tag)
    if match is None:
        return None
    major, minor, patch, prerelease = match.groups()
    if prerelease is None:
        prerelease_key = (1,)
    else:
        identifiers = []
        for item in prerelease.split('.'):
            if item.isdigit():
                if len(item) > 1 and item[0] == '0':
                    return None
                identifiers.append((0, int(item)))
            else:
                identifiers.append((1, item))
        prerelease_key = (0, tuple(identifiers))
    return (int(major), int(minor), int(patch), prerelease_key)


arch = sys.argv[1]
best = None
best_tag = None
for name in sys.argv[2:]:
    path = Path(name)
    try:
        if not path.is_file() or path.stat().st_size > MAX_PAGE:
            raise ValueError
        releases = json.loads(path.read_text(encoding='utf-8'))
    except (OSError, ValueError, UnicodeError):
        raise SystemExit(1)
    if not isinstance(releases, list) or len(releases) > 100:
        raise SystemExit(1)
    for release in releases:
        if not isinstance(release, dict) or release.get('draft', False) is not False:
            continue
        tag = release.get('tag_name')
        if not isinstance(tag, str):
            continue
        parsed = parse(tag)
        if parsed is None:
            continue
        assets = release.get('assets')
        if assets is not None:
            if not isinstance(assets, list):
                continue
            names = {asset.get('name') for asset in assets
                     if isinstance(asset, dict) and isinstance(asset.get('name'), str)}
            raw = f'xray-core-helper-{tag[len("helper-"):]}-linux-{arch}'
            if raw not in names or 'SHA256SUMS' not in names:
                continue
        if best is None or parsed > best:
            best = parsed
            best_tag = tag
if best_tag is None:
    print(f'bootstrap: no suitable published release for linux-{arch}', file=sys.stderr)
    raise SystemExit(1)
print(best_tag)
PY
}

temporary=$(mktemp -d "${TMPDIR:-/tmp}/xray-helper-bootstrap.XXXXXXXX") \
  || fail 'could not create a temporary directory'
trap 'rm -rf -- "$temporary"' EXIT HUP INT TERM

if [[ -z $version ]]; then
  command -v python3 >/dev/null 2>&1 \
    || fail 'python3 is required to select a release'
  version=$(select_version "$arch" "$temporary/metadata") || exit 1
  valid_version "$version" || fail 'invalid release version selected'
fi

asset=$(printf 'xray-core-helper-%s-linux-%s' "${version#helper-}" "$arch")
cli=$temporary/$asset
checksums=$temporary/SHA256SUMS

download_asset() {
  local name=$1 destination=$2 maximum=$3
  if ! curl --fail --silent --show-error --location --proto '=https' \
    --proto-redir '=https' --tlsv1.2 \
    --connect-timeout 10 --max-time 300 --max-filesize "$maximum" \
    --user-agent 'xray-core-helper-bootstrap' \
    --output "$destination" "$download_url/$version/$name"; then
    fail "download failed: $name"
  fi
  [[ -s $destination ]] || fail "download was empty: $name"
}

verify_release_checksum() {
  local sums=$1 name=$2 file=$3 expected actual
  [[ -s $sums && -s $file ]] || return 1
  if grep -Ev '^[0-9a-f]{64}  [A-Za-z0-9._-]+$' "$sums" >/dev/null; then
    return 1
  fi
  expected=$(awk -v name="$name" '
    $2 == name { count++; digest = $1 }
    END { if (count != 1) exit 1; print digest }
  ' "$sums") || return 1
  actual=$(sha256sum -- "$file") || return 1
  actual=${actual%%[[:space:]]*}
  [[ $actual == "$expected" ]]
}

download_asset SHA256SUMS "$checksums" "$max_checksum_bytes"
download_asset "$asset" "$cli" "$max_cli_bytes"
verify_release_checksum "$checksums" "$asset" "$cli" \
  || fail 'release checksum mismatch'
chmod 0700 -- "$cli"

install_args=(install --version "$version")
[[ -n $web_option ]] && install_args+=("$web_option")
printf 'Installing %s\n' "$version"
run_as_root "$cli" "${install_args[@]}"
