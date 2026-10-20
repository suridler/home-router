package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdUnitKeepsBetaBoundaryNarrow(t *testing.T) {
	unit := readAsset(t, "systemd", "xray-core-helper.service")
	required := []string{
		"User=root",
		"Group=xray-core-helper",
		"ExecStart=/usr/local/sbin/xray-core-helperd -config /etc/xray-core-helper/config.toml",
		"RuntimeDirectory=xray-core-helper",
		"RuntimeDirectoryMode=0750",
		"StateDirectory=xray-core-helper",
		"StateDirectoryMode=0750",
		"ProtectSystem=strict",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK",
		"ReadWritePaths=/run/xray-core-helper /var/lib/xray-core-helper /usr/local/etc/xray /usr/local/share/xray /var/log/xray /etc/xray-core-helper /etc/systemd/system /etc/sysctl.d",
	}
	for _, setting := range required {
		if !hasLine(unit, setting) {
			t.Errorf("unit is missing %q", setting)
		}
	}
	for _, forbidden := range []string{"ListenStream=", "0.0.0.0", "AF_VSOCK", "ReadWritePaths=/ ", "ReadWritePaths=/etc ", "ReadWritePaths=/home "} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("unit contains forbidden writable/listener setting %q", forbidden)
		}
	}
}

func TestIdentityAndDirectoriesAreDeterministic(t *testing.T) {
	sysusers := strings.TrimSpace(readAsset(t, "sysusers.d", "xray-core-helper.conf"))
	if sysusers != "g xray-core-helper - -" {
		t.Fatalf("sysusers asset = %q", sysusers)
	}
	tmpfiles := readAsset(t, "tmpfiles.d", "xray-core-helper.conf")
	for _, line := range []string{
		"d /run/xray-core-helper 0750 root xray-core-helper -",
		"d /var/lib/xray-core-helper 0750 root xray-core-helper -",
	} {
		if !hasLine(tmpfiles, line) {
			t.Errorf("tmpfiles asset is missing %q", line)
		}
	}
}

func readAsset(t *testing.T, parts ...string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func hasLine(contents, want string) bool {
	for _, line := range strings.Split(contents, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func TestMaintenanceTimersAreRetiredAndRollbackRemainsExternal(t *testing.T) {
	for _, stem := range []string{"xray-core-helper-routing", "xray-core-helper-errors", "update-xray-geodata"} {
		for _, ext := range []string{".timer", ".service"} {
			if _, err := os.Stat(filepath.Join("systemd", stem+ext)); !os.IsNotExist(err) {
				t.Fatalf("obsolete maintenance unit %s%s: %v", stem, ext, err)
			}
		}
	}
	timer := readAsset(t, "systemd", "xray-core-helper-rollback.timer")
	if !hasLine(timer, "OnActiveSec=120s") {
		t.Fatal("rollback deadline removed")
	}
	service := readAsset(t, "systemd", "xray-core-helper-rollback.service")
	if !hasLine(service, "ExecStart=/usr/local/bin/xray-core-helper -config /etc/xray-core-helper/config.toml safe-apply rollback-pending") {
		t.Fatal("rollback command changed")
	}
}

func TestUpdateJournalIsWritableWithoutStateDirectoryChown(t *testing.T) {
	unit := readAsset(t, "systemd", "xray-core-helper.service")
	if !hasLine(unit, "ReadWritePaths=/var/lib/xray-core-helper-update") {
		t.Fatal("update state is not writable")
	}
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "StateDirectory=") && strings.Contains(line, "xray-core-helper-update") {
			t.Fatal("systemd must not recursively change recovery snapshot ownership")
		}
	}
}
