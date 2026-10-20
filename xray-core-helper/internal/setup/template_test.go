package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/xrayconfig"
)

func setupTemplateFixture(t *testing.T) (*fileStore, string, []byte) {
	t.Helper()
	s, entries, root := fileStoreFixture(t)
	draft, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entries[0].WorkingPath, draft, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(entries[0].WorkingPath, 0640); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "helper.toml")
	config := []byte("# helper settings\n[logging]\nlevel = 'error'\ndns_log = true\n[xray]\napi = '127.0.0.1:41085'\n[geodata]\nuser_geoip = 'geoip_custom.dat'\n")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureNetwork(s, configPath); err != nil {
		t.Fatal(err)
	}
	s.checkRelations = true
	s.identity = func(context.Context) (int, error) { return os.Getegid(), nil }
	return s, configPath, draft
}

func TestSetupRendersFullXrayTemplateAndPreservesSource(t *testing.T) {
	ctx := context.Background()
	s, configPath, draft := setupTemplateFixture(t)
	xrayEntry := s.entries[0]
	plan := Plan{Interface: "br-test", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"}
	plan, err := s.AnalyzeGateway(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(xrayEntry.SystemPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"listen": "192.0.2.1"`, `"listen": "127.0.0.1:41085"`, `"loglevel": "error"`, `"dnsLog": true`, `ext:geoip_custom.dat:USERLIST`, "// ЛОГИРОВАНИЕ"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatal("missing helper binding or template comment", want)
		}
	}
	if bytes.Contains(got, []byte("${")) {
		t.Fatal("unresolved config committed")
	}
	if gateway, err := xrayconfig.InspectGateway(got); err != nil || gateway.Port != 52345 || gateway.EgressMark != 255 {
		t.Fatal("invalid initial gateway", err)
	}
	unchanged, err := os.ReadFile(xrayEntry.WorkingPath)
	if err != nil || !bytes.Equal(unchanged, draft) {
		t.Fatal("setup rewrote source template", err)
	}
	info, err := os.Stat(xrayEntry.WorkingPath)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("setup changed source metadata", err)
	}
	if err := tx.Complete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureNetwork(s, configPath); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.AnalyzeGateway(ctx, Plan{Interface: "br-test", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil || !repeated.PreserveXray || repeated.XrayCandidateHash != plan.XrayCandidateHash {
		t.Fatal("repeated setup changed rendered Xray", err)
	}
}

func TestSetupRejectsHelperSettingsDriftAfterPreview(t *testing.T) {
	s, path, _ := setupTemplateFixture(t)
	ctx := context.Background()
	plan, err := s.AnalyzeGateway(ctx, Plan{Interface: "br-test", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(raw, []byte("'error'"), []byte("'warning'"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, plan); err == nil {
		t.Fatal("stale helper settings accepted")
	}
	got, err := os.ReadFile(s.entries[0].SystemPath)
	if err != nil || len(got) != 0 {
		t.Fatal("drift failure changed system config", err)
	}
	// Satisfy the fixture validator assertion without using host tools/services.
	if err := s.validate(ctx, s.entries[0].ID, s.entries[0].SystemPath); err != nil {
		t.Fatal(err)
	}
}
