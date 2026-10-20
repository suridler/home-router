package setup

import (
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/xrayconfig"
	"os"
	"strings"
	"testing"
)

func TestNativeGatewayDefaultsAndAnnotations(t *testing.T) {
	raw, err := os.ReadFile("../../templConf/nftables/nftables.conf")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderNative(templatecatalog.Nftables, raw, Plan{Interface: "br-lan", LANNetwork: "10.20.0.0/24", xrayGID: 4242, Gateway: &xrayconfig.Gateway{Port: 52345, EgressMark: 255}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	for _, bad := range []string{"ip saddr != @proxy_clients", "988", "hostlan0", "192.0.2.0/24"} {
		if strings.Contains(text, bad) {
			t.Fatalf("developer/host interception default: %s", bad)
		}
	}
	for _, want := range []string{`define lan_interface = "br-lan"`, `define lan_network = 10.20.0.0/24`, `iifname != { $lan_interface, "lo" } accept`, `define xray_gid = 4242`, `meta skgid $xray_gid accept`, `chain output`, `fib daddr type local accept`} {
		if !strings.Contains(text, want) {
			t.Fatal(want)
		}
	}
	if _, err = nativeblock.Build(nil, rendered, true); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(text, "source=helper.network.interface", "source=unknown.interface", 1), text + "\n# helper:v1 bind ingress type=interface source=setup.interface token=\"\\\"x\\\"\"\ndefine other = \"x\""} {
		if _, err = renderNative(templatecatalog.Nftables, []byte(invalid), Plan{}); err == nil {
			t.Fatal("ambiguous annotation accepted")
		}
	}
}

func TestUnmarkedSystemTemplatesNeedNoSetupValues(t *testing.T) {
	for _, id := range []templatecatalog.ID{templatecatalog.Nftables, templatecatalog.Sysctl, templatecatalog.TProxyUnit} {
		raw := []byte("# native comment\r\nunrestricted native content\n")
		got, err := renderNative(id, raw, Plan{})
		if err != nil || string(got) != string(raw) {
			t.Fatalf("unannotated %s requires implicit bindings: %v", id, err)
		}
	}
}

func TestMarkedRenderingPreservesNativeVariableNames(t *testing.T) {
	raw := []byte("# helper:v1 bind selected type=interface source=setup.interface token=\"\\\"old0\\\"\"\n  define not_lan_interface = \"old0\" # retain\n")
	got, err := renderNative(templatecatalog.Nftables, raw, Plan{Interface: "br-test"})
	if err != nil || !strings.Contains(string(got), "  define not_lan_interface = \"br-test\" # retain\n") {
		t.Fatalf("renderer imposes native name or formatting: %v", err)
	}
}
