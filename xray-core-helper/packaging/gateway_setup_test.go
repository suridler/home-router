package packaging

import (
	"os"
	"strings"
	"testing"
)

func TestGatewayBootIsIndependentOfHelper(t *testing.T) {
	raw, err := os.ReadFile("../templConf/systemd/xray-tproxy-routing.service")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"xray-core-helper", "After=xray.service", "BindsTo=", "PartOf=", "network-check", "ConditionPathExists", "[Install]", "xray-tproxy/network"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("control-plane dependency in native lifecycle", forbidden)
		}
	}
	for _, required := range []string{"ExecStart=/sbin/ip route add local default dev lo table ${TPROXY_TABLE}", "ExecStart=/sbin/ip rule add fwmark ${TPROXY_MARK} table ${TPROXY_TABLE}", "ExecStopPost=-/sbin/ip rule del fwmark ${TPROXY_MARK} table ${TPROXY_TABLE}"} {
		if !strings.Contains(text, required) {
			t.Fatal(required)
		}
	}
	dropin := readAsset(t, "systemd", "50-tproxy.conf")
	for _, required := range []string{"ExecStartPost=+/usr/bin/systemctl start xray-tproxy-routing.service", "ExecStop=+/usr/bin/systemctl stop xray-tproxy-routing.service", "ExecStopPost=+/usr/bin/systemctl stop xray-tproxy-routing.service"} {
		if !hasLine(dropin, required) {
			t.Fatal(required)
		}
	}
	if strings.Contains(dropin, "ExecStartPost=-") || strings.Contains(dropin, "ExecStart=") {
		t.Fatal("strict lifecycle or vendor ExecStart changed")
	}
	daemon := readAsset(t, "systemd", "xray-core-helper.service")
	if strings.Contains(daemon, "Before=xray.service") || strings.Contains(daemon, "Before=xray-tproxy") {
		t.Fatal("helper orders router boot")
	}
}

func TestNativeFactorySourcesAreSeparateFromInstalledNetwork(t *testing.T) {
	script := readAsset(t, "install-templconf.sh")
	for _, want := range []string{"/usr/share/xray-core-helper/native", "50-tproxy.conf"} {
		if !strings.Contains(script, want) {
			t.Fatal(want)
		}
	}
	uninstall := readAsset(t, "uninstall-bundle.sh")
	for _, path := range []string{"/etc/systemd/system/xray.service.d/50-tproxy.conf"} {
		if strings.Contains(uninstall, "rm -f -- "+path) {
			t.Fatal("uninstall removes native asset", path)
		}
	}
	if !strings.Contains(uninstall, "native_network_adoption_required") {
		t.Fatal("old-unit dangling dependency guard absent")
	}
	tmpfiles := readAsset(t, "tmpfiles.d", "xray-core-helper.conf")
	for _, path := range []string{"/etc/sysctl.d", "/usr/local/etc/xray", "/usr/local/share/xray", "/var/log/xray"} {
		if strings.Contains(tmpfiles, "d "+path+" ") {
			t.Fatal("tmpfiles mutates admin directory", path)
		}
	}
}
