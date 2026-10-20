package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Load the real unit graph in an entirely temporary root. This checks systemd
// syntax and ordering without contacting the host manager or running commands.
func TestNativeLifecycleOfflineSystemdVerification(t *testing.T) {
	analyzer, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze unavailable; offline unit verification not performed")
	}
	root := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"sysinit", "basic", "shutdown", "multi-user", "network", "network-online", "nss-lookup"} {
		write("etc/systemd/system/"+target+".target", "[Unit]\nDefaultDependencies=no\n", 0644)
	}
	for _, name := range []string{"systemd-sysctl", "nftables"} {
		write("etc/systemd/system/"+name+".service", "[Unit]\nDefaultDependencies=no\n[Service]\nType=oneshot\nExecStart=/usr/bin/true\n", 0644)
	}
	for _, name := range []string{"usr/bin/true", "usr/bin/systemctl", "sbin/ip"} {
		write(name, "#!/bin/sh\nexit 0\n", 0755)
	}
	write("etc/systemd/system/xray.service", "[Service]\nExecStart=/usr/bin/true\n", 0644)
	write("etc/systemd/system/xray.service.d/50-tproxy.conf", readAsset(t, "systemd", "50-tproxy.conf"), 0644)
	raw, err := os.ReadFile("../templConf/systemd/xray-tproxy-routing.service")
	if err != nil {
		t.Fatal(err)
	}
	write("etc/systemd/system/xray-tproxy-routing.service", string(raw), 0644)
	output, err := exec.Command(analyzer, "--root", root, "verify", "--man=no", "xray.service", "xray-tproxy-routing.service").CombinedOutput()
	if err != nil {
		t.Fatalf("offline systemd verification: %v\n%s", err, output)
	}
}
