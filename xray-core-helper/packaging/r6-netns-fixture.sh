#!/usr/bin/env bash
# Invoked only by the opt-in Go fixture inside NEW mount + network namespaces.
set -euo pipefail
[[ ${XCH_DISPOSABLE_HOST:-} == YES && ${XCH_NETNS_CHILD:-} == YES ]] || exit 2
[[ $(readlink /proc/self/ns/net) != $(readlink /proc/1/ns/net) && $(readlink /proc/self/ns/mnt) != $(readlink /proc/1/ns/mnt) ]] || { echo 'fresh mount and network namespaces required' >&2; exit 2; }
batch=$1
port=${XCH_TEST_PORT:-52345}; mark=${XCH_TEST_MARK:-1}; bypass=${XCH_TEST_BYPASS:-255}; table=${XCH_TEST_TABLE:-100}
for value in "$port" "$mark" "$bypass" "$table"; do [[ $value =~ ^[1-9][0-9]{0,9}$ ]] && ((value <= 4294967295)) || exit 2; done
((port <= 65535 && mark != bypass)) || exit 2
# /run (including ip-netns handles) must never refer to the host filesystem.
mount -t tmpfs -o mode=0755 tmpfs /run
scratch=$(mktemp -d /tmp/xch-r6-netns.XXXXXX)
xray_pid=""
cleanup() { [[ -z $xray_pid ]] || kill "$xray_pid" 2>/dev/null || true; ip netns del lan 2>/dev/null || true; ip netns del container 2>/dev/null || true; rm -rf -- "$scratch"; }
trap cleanup EXIT
ip link set lo up
sysctl -qw net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0
ip link add dummy0 type dummy
ip link set dummy0 up
ip addr add 203.0.113.1/32 dev dummy0
ip route add default dev dummy0
for name in lan container; do
  ip netns add "$name"
  ip link add "${name}0" type veth peer name peer0 netns "$name"
  ip link set "${name}0" up
  ip -n "$name" link set lo up
  ip -n "$name" link set peer0 up
done
ip addr add 10.77.0.1/24 dev lan0
ip -n lan addr add 10.77.0.2/24 dev peer0
ip -n lan addr add 10.77.1.2/24 dev peer0
ip -n lan route add default via 10.77.0.1 src 10.77.0.2
ip addr add 10.88.0.1/24 dev container0
ip -n container addr add 10.88.0.2/24 dev peer0
ip -n container addr add 10.77.0.3/32 dev peer0
ip -n container route add default via 10.88.0.1 src 10.77.0.3
ip route add local default dev lo table "$table"
ip rule add fwmark "$mark" table "$table"
# A real transparent listener is necessary: without it TProxy can fail before
# reaching the rule's counter, making packet-scope assertions meaningless.
printf '%s\n' '{"log":{"access":"none","error":"none"},"inbounds":[{"port":52345,"protocol":"dokodemo-door","settings":{"network":"tcp,udp","followRedirect":true},"streamSettings":{"sockopt":{"tproxy":"tproxy"}}}],"outbounds":[{"protocol":"freedom","streamSettings":{"sockopt":{"mark":255}}}]}' | sed -e "s/52345/$port/g" -e "s/255/$bypass/g" > "$scratch/xray.json"
setpriv --regid 65534 --clear-groups /usr/local/bin/xray run -config "$scratch/xray.json" > "$scratch/xray.log" 2>&1 &
xray_pid=$!
for _ in {1..50}; do ss -Hlun "sport = :$port" | grep -q . && break; sleep 0.1; done
ss -Hlun "sport = :$port" | grep -q .
nft add table inet unrelated
nft add chain inet unrelated sentinel
nft list table inet unrelated > "$scratch/unrelated.before"
nft --check --file "$batch"
nft --file "$batch"
# Persistent profile must not intercept until Xray lifecycle enables both jumps.
[[ $(nft -j list chain inet xray prerouting | jq '[.nftables[] | .rule? // empty] | length') == 0 ]]
printf 'add rule inet xray prerouting jump prerouting_proxy\nadd rule inet xray output jump output_proxy\n' | nft --file -
count() { nft -j list table inet xray | jq '[.nftables[] | .rule?.expr[]? | .counter?.packets // empty] | add // 0'; }
send() { ip netns exec "$1" bash -c 'printf fixture > /dev/udp/$1/443' bash "$2"; sleep 0.2; }
before=$(count); send lan 198.51.100.1; after=$(count)
((after > before)) || { echo 'enrolled LAN traffic not intercepted' >&2; exit 1; }
ip -n lan route replace default via 10.77.0.1 src 10.77.1.2
before=$(count); send lan 198.51.100.1; (( $(count) > before ))
ip -n lan route replace default via 10.77.0.1 src 10.77.0.2
for destination in 203.0.113.1 10.99.0.1; do before=$(count); send lan "$destination"; [[ $(count) == "$before" ]]; done
before=$(count); send container 198.51.100.1; [[ $(count) == "$before" ]]
before=$(count); printf fixture > /dev/udp/198.51.100.1/443; sleep 0.2; (( $(count) > before ))
# Egress GID bypass and socket mark bypass independently avoid re-interception.
before=$(count); setpriv --regid 65534 --clear-groups bash -c 'printf fixture > /dev/udp/198.51.100.1/443'; sleep 0.2; [[ $(count) == "$before" ]]
before=$(count)
python3 -c 'import socket; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.setsockopt(socket.SOL_SOCKET,socket.SO_MARK,int(__import__("sys").argv[1])); s.sendto(b"fixture",("198.51.100.1",443)); s.close()' "$bypass"
sleep 0.2; [[ $(count) == "$before" ]]
before=$(count); printf fixture > /dev/udp/127.0.0.1/443; sleep 0.2; [[ $(count) == "$before" ]]
# Rejected atomic batch must preserve the managed table, including counters.
nft list table inet xray > "$scratch/managed.before"
printf 'add table inet xray\ndelete table inet xray\ntable inet xray { invalid_native_statement }\n' > "$scratch/rejected.nft"
if nft --file "$scratch/rejected.nft" > /dev/null 2>&1; then exit 1; fi
nft list table inet xray > "$scratch/managed.after"
cmp "$scratch/managed.before" "$scratch/managed.after"
# Disable both entry points atomically while leaving the base profile present.
printf 'flush chain inet xray prerouting\nflush chain inet xray output\n' | nft --file -
[[ $(nft -j list chain inet xray output | jq '[.nftables[] | .rule? // empty] | length') == 0 ]]
nft --file "$batch"
nft list table inet unrelated > "$scratch/unrelated.after"
cmp "$scratch/unrelated.before" "$scratch/unrelated.after"
echo N6_NATIVE_GATEWAY_NETNS_OK
