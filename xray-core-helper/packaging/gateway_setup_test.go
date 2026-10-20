package packaging

import (
	"os"
	"strings"
	"testing"
)

func TestGatewayBootChecksReceivingSideBeforeActivatingNetwork(t *testing.T) {
	raw, err := os.ReadFile("../templConf/systemd/xray-tproxy-routing.service")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	last := -1
	for _, step := range []string{"ExecStartPre=/usr/local/bin/xray-core-helper network-check", "ExecStartPre=/usr/bin/systemctl start xray-core-helper-sysctl.service", "ExecStartPre=/usr/local/libexec/xray-core-helper-nft-load", "ExecStart=/sbin/ip route add", "ExecStart=/sbin/ip rule add"} {
		position := strings.Index(text, step)
		if position < 0 || position <= last {
			t.Fatal("unsafe activation ordering", step)
		}
		last = position
	}
	for _, required := range []string{"BindsTo=xray.service", "PartOf=xray.service", "After=xray.service", "ExecStopPost=-/sbin/ip rule del", "WantedBy=multi-user.target"} {
		if !strings.Contains(text, required) {
			t.Fatal(required)
		}
	}
	if strings.Contains(text, "--file /etc/nftables.conf") {
		t.Fatal("replays unowned nft sections")
	}
}
