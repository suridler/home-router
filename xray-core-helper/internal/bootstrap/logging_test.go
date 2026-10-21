package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoggingAndUserGeoipConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := testLoad(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Logging != (LoggingConfig{AccessPath: "none", Level: "warning", AccessFilePath: "/var/log/xray/access.log", ErrorFilePath: "/var/log/xray/error.log"}) || config.Geodata.UserGeoip != "geoip_user.dat" {
		t.Fatal("unexpected initial logging or geoip settings")
	}
	if err := os.WriteFile(path, []byte("[logging]\naccess_path = ''\nerror_path = '/var/log/xray/custom.log'\nlevel = 'error'\ndns_log = true\n[geodata]\nuser_geoip = 'geoip_custom.dat'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err = testLoad(path)
	if err != nil || config.Logging.AccessPath != "" || !config.Logging.DNSLog || config.Geodata.UserGeoip != "geoip_custom.dat" {
		t.Fatal("custom logging/geoip config not loaded", err)
	}
}

func TestLoggingValidation(t *testing.T) {
	for _, value := range []string{"relative.log", "/", "/tmp/../etc/xray.log", "/tmp/./xray.log", "/tmp/xray.log\n", "/tmp/a\x00b", "/tmp/xray/"} {
		if ValidLogDestination(value) {
			t.Fatalf("unsafe destination accepted: %q", value)
		}
	}
	for _, value := range []string{"", "none", "/var/log/xray/error.log", "/tmp/a\"b.log"} {
		if !ValidLogDestination(value) {
			t.Fatalf("valid destination rejected: %q", value)
		}
	}
	for _, value := range []string{"warn", "WARNING", "", "debug\n"} {
		if (LoggingConfig{Level: value}).Validate() == nil {
			t.Fatal("invalid level accepted")
		}
	}
	for _, name := range []string{"geoip.dat", "../geoip_user.dat", "geosite_user.dat", "geoip_a/b.dat", "geoip_.dat"} {
		config := Defaults()
		config.Geodata.UserGeoip = name
		if config.Validate() == nil {
			t.Fatal("invalid user geoip name accepted")
		}
	}
}
