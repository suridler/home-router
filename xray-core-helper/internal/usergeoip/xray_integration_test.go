package usergeoip

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in integration uses a disposable executable copy and synthetic config,
// assets and log destinations. No host service or config is accessed.
func TestEncodedAssetsWithXray(t *testing.T) {
	binary := os.Getenv("XRAY_HELPER_TEST_XRAY_BINARY")
	if binary == "" {
		t.Skip("set XRAY_HELPER_TEST_XRAY_BINARY to a disposable Xray executable copy")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Xray fixture binary must be absolute")
	}
	for _, test := range []struct {
		name   string
		values []string
	}{
		{"empty", nil},
		{"single", []string{"192.0.2.1"}},
		{"multiple", []string{"198.51.100.7", "203.0.113.9", "192.0.2.1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			data, err := Encode(test.values)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "geoip_user.dat"), data, 0600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dir, "xray.json")
			raw := []byte(`{"log":{"access":"none","error":"none","loglevel":"none"},"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","ruleTag":"proxy-ip-permanent","ip":["ext:geoip_user.dat:USERLIST"],"outboundTag":"direct"}]}}`)
			if err := os.WriteFile(config, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "run", "-test", "-config", config)
			command.Env = []string{"XRAY_LOCATION_ASSET=" + dir}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("Xray rejected synthetic %s USERLIST: %v\n%s", test.name, err, output)
			}
		})
	}
}
