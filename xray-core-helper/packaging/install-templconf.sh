#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "install-templconf.sh must run as root" >&2
    exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(dirname -- "$script_dir")
factory_root=/usr/share/xray-core-helper/templConf

install -d -o root -g root -m 0755 \
    "$factory_root/xray" \
    "$factory_root/nftables" \
    "$factory_root/systemd" \
    "$factory_root/sysctl"

install -o root -g root -m 0644 "$project_dir/templConf/xray/config.json" "$factory_root/xray/config.json"
install -o root -g root -m 0644 "$project_dir/templConf/nftables/nftables.conf" "$factory_root/nftables/nftables.conf"
install -o root -g root -m 0644 "$project_dir/templConf/systemd/xray-tproxy-routing.service" "$factory_root/systemd/xray-tproxy-routing.service"
install -o root -g root -m 0644 "$project_dir/templConf/sysctl/99-xray.conf" "$factory_root/sysctl/99-xray.conf"

# Factory source only: explicit TProxy apply installs the Xray drop-in.
install -d -o root -g root -m 0755 /usr/share/xray-core-helper/native
install -o root -g root -m 0644 "$project_dir/packaging/systemd/50-tproxy.conf" /usr/share/xray-core-helper/native/50-tproxy.conf

systemd-tmpfiles --create "$project_dir/packaging/tmpfiles.d/xray-core-helper.conf"
