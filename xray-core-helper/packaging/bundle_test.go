package packaging

import (
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
)

func TestNewInstallationGeodataSources(t *testing.T) {
	config, err := testconfig.Load("../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Geodata.Origins) != 1 || config.Geodata.Origins[0] != "https://raw.githubusercontent.com" {
		t.Fatalf("unexpected trusted origins: %v", config.Geodata.Origins)
	}
	for _, asset := range []struct {
		source bootstrap.GeodataSourceConfig
		name   string
	}{
		{config.Geodata.Geosite, "geosite"},
		{config.Geodata.Geoip, "geoip"},
	} {
		want := "https://raw.githubusercontent.com/runetfreedom/russia-blocked-" + asset.name + "/release/" + asset.name + ".dat"
		if asset.source.URL != want || asset.source.Checksum.URL != want+".sha256sum" || asset.source.Checksum.Algorithm != "sha256" {
			t.Errorf("invalid installation source for %s: %+v", asset.name, asset.source)
		}
	}
}

func TestBundlePreservesStateAndDiscoversPathsBeforeStarting(t *testing.T) {
	raw, err := os.ReadFile("install-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{"discover-xray", "20-installation-paths.conf", "--without-web", "--with-web", "--web-port", "XRAY_HELPER_WEB_ENABLED", "preserving /etc/xray-core-helper/config.toml", "install-templconf.sh", "/v1/web/settings", "systemctl enable xray-core-helper.service"} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(source, "--interface") || strings.Contains(source, "setup-select") {
		t.Fatal("installer still requires network selection")
	}
	if strings.Index(source, "discover-xray") > strings.Index(source, "systemd-sysusers \"$helper_root") {
		t.Fatal("discovery must precede mutations")
	}
}
func TestUninstallPreservesConfigurationAndBackups(t *testing.T) {
	raw, err := os.ReadFile("uninstall-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, protected := range []string{"/etc/xray-core-helper", "/etc/xrayui-web", "/var/lib/xray-core-helper"} {
		if strings.Contains(source, "rm -rf -- "+protected) || !strings.Contains(source, "Preserved:") {
			t.Fatalf("operator data not protected: %s", protected)
		}
	}
}
