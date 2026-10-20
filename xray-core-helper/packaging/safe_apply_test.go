package packaging

import (
	"strings"
	"testing"
)

func TestSafeApplyTimerIsFixedAndStartsHelperForRollback(t *testing.T) {
	service := readAsset(t, "systemd", "xray-core-helper-rollback.service")
	timer := readAsset(t, "systemd", "xray-core-helper-rollback.timer")
	for _, setting := range []string{"Requires=xray-core-helper.service", "After=xray-core-helper.service", "ExecStart=/usr/local/bin/xray-core-helper -config /etc/xray-core-helper/config.toml safe-apply rollback-pending"} {
		if !hasLine(service, setting) {
			t.Errorf("rollback service missing %q", setting)
		}
	}
	for _, setting := range []string{"OnActiveSec=120s", "AccuracySec=1s", "Unit=xray-core-helper-rollback.service"} {
		if !hasLine(timer, setting) {
			t.Errorf("rollback timer missing %q", setting)
		}
	}
	for _, forbidden := range []string{"bash", "sh -c", "%i", "Environment="} {
		if strings.Contains(service+timer, forbidden) {
			t.Errorf("unsafe timer content %q", forbidden)
		}
	}
}
func TestHelperOrdersOnlyWebAndHasOnlyFixedWrites(t *testing.T) {
	unit := readAsset(t, "systemd", "xray-core-helper.service")
	if !hasLine(unit, "Before=xrayui-web.service") {
		t.Fatal("missing web ordering")
	}
	for _, path := range []string{"/etc/xray-core-helper", "/etc/systemd/system", "/etc/sysctl.d"} {
		if !strings.Contains(unit, path) {
			t.Errorf("missing fixed writable path %s", path)
		}
	}
}

func TestNativeSysctlPrivilegesAreIsolatedToFixedOneshot(t *testing.T) {
	unit := readAsset(t, "systemd", "xray-core-helper-sysctl.service")
	for _, setting := range []string{
		"Type=oneshot", "User=root", "Group=root",
		"ExecStart=/usr/sbin/sysctl -p /etc/sysctl.d/99-xray.conf",
		"ReadWritePaths=/proc/sys", "ProtectSystem=strict",
		"NoNewPrivileges=true", "PrivateTmp=true", "ProtectHome=true",
		"ProtectKernelModules=true", "ProtectControlGroups=true",
		"RestrictAddressFamilies=AF_UNIX",
	} {
		if !hasLine(unit, setting) {
			t.Errorf("sysctl oneshot missing %q", setting)
		}
	}
	var writes, execs int
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ReadWritePaths=") {
			writes++
			if line != "ReadWritePaths=/proc/sys" {
				t.Error("unexpected writable paths")
			}
		}
		if strings.HasPrefix(line, "Exec") {
			execs++
			if line != "ExecStart=/usr/sbin/sysctl -p /etc/sysctl.d/99-xray.conf" {
				t.Error("unexpected executable action")
			}
		}
	}
	if writes != 1 || execs != 1 {
		t.Fatal("sysctl writer must have one fixed action and writable scope")
	}
	for _, forbidden := range []string{"ReadOnlyPaths=/proc/sys", "ProtectKernelTunables=true", "RemainAfterExit=true", "Environment=", "EnvironmentFile=", "%i", "sh -c", "bash"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("unexpected sysctl writer setting %q", forbidden)
		}
	}
	daemon := readAsset(t, "systemd", "xray-core-helper.service")
	if !hasLine(daemon, "ProtectKernelTunables=true") {
		t.Fatal("daemon kernel protection weakened")
	}
	for _, line := range strings.Split(daemon, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ReadWritePaths=") && strings.Contains(line, "/proc") {
			t.Fatal("sysctl privilege leaked into daemon")
		}
	}
}
