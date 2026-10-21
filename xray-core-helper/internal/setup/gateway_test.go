package setup

import (
	"bytes"
	"context"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/testconfig"
	"home-router/xray-core-helper/internal/xrayconfig"
	"os"
	"strings"
	"testing"
)

func TestGatewayPlanUsesExistingPortAndMarkAndRecoversWorkingTemplates(t *testing.T) {
	ctx := context.Background()
	s, entries, _ := fileStoreFixture(t)
	for _, entry := range entries {
		names := map[templatecatalog.ID]string{templatecatalog.XrayConfig: "xray/config.json", templatecatalog.Nftables: "nftables/nftables.conf", templatecatalog.TProxyUnit: "systemd/xray-tproxy-routing.service", templatecatalog.Sysctl: "sysctl/99-xray.conf"}
		raw, err := os.ReadFile("../../templConf/" + names[entry.ID])
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(entry.WorkingPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	defaults, _ := os.ReadFile(entries[0].WorkingPath)
	config := testconfig.Defaults()
	config.Network.Interface, config.Network.Address, config.Network.IPv4Prefix = "lan0", "10.0.0.1", "10.0.0.0/24"
	values := setupTemplateValues(config)
	existing, err := xrayconfig.RenderSetupTemplate(defaults, []byte(`{"inbounds":[{"tag":"tproxy-in","port":61000}],"outbounds":[{"streamSettings":{"sockopt":{"mark":42}}}]}`), values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entries[0].SystemPath, existing, 0600); err != nil {
		t.Fatal(err)
	}
	s.identity = func(context.Context) (int, error) { return 4242, nil }
	s.checkRelations = true
	plan, err := s.AnalyzeGateway(ctx, Plan{Interface: "lan0", ListenAddress: "10.0.0.1", LANNetwork: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PreserveXray || plan.Gateway.Port != 61000 || plan.Gateway.EgressMark != 42 {
		t.Fatal("custom settings lost")
	}
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	workingXray, err := os.ReadFile(entries[0].WorkingPath)
	if err != nil || !bytes.Equal(workingXray, defaults) {
		t.Fatal("setup changed Xray source template", err)
	}
	nft, _ := os.ReadFile(entries[1].SystemPath)
	if !strings.Contains(string(nft), "define tproxy_port = 61000") || !strings.Contains(string(nft), "define egress_mark = 42") {
		t.Fatal("network not bound to existing Xray")
	}
	working, _ := os.ReadFile(entries[1].WorkingPath)
	factoryBefore, err := os.ReadFile("../../templConf/nftables/nftables.conf")
	if err != nil || !bytes.Equal(factoryBefore, working) {
		t.Fatal("setup changed native template", err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(entries[0].SystemPath)
	if !bytes.Equal(existing, restored) {
		t.Fatal("existing Xray not restored")
	}
	factory, _ := os.ReadFile("../../templConf/nftables/nftables.conf")
	restored, _ = os.ReadFile(entries[1].WorkingPath)
	if !bytes.Equal(factory, restored) {
		t.Fatal("working template backup not restored")
	}
}
func TestGatewayReplacementRequiresExplicitPlanAndChecksFingerprint(t *testing.T) {
	ctx := context.Background()
	s, entries, _ := fileStoreFixture(t)
	raw, _ := os.ReadFile("../../templConf/xray/config.json")
	os.WriteFile(entries[0].WorkingPath, raw, 0600)
	os.WriteFile(entries[0].SystemPath, []byte("broken existing config"), 0600)
	s.checkRelations = true
	if _, err := s.AnalyzeGateway(ctx, Plan{}); err == nil {
		t.Fatal("implicit replacement")
	}
	plan, err := s.AnalyzeGateway(ctx, Plan{ReplaceXray: true, Interface: "lan0", ListenAddress: "10.0.0.1", LANNetwork: "10.0.0.0/24"})
	if err != nil || plan.PreserveXray {
		t.Fatal(err)
	}
	os.WriteFile(entries[0].SystemPath, []byte("external change"), 0600)
	if _, _, err := s.gatewayCandidate(plan); err == nil {
		t.Fatal("stale plan accepted")
	}
	// satisfy the fixture's native-validator assertion without touching a system service.
	s.validate(ctx, entries[0].ID, entries[0].WorkingPath)
}

func TestGatewayPlanRejectsNativeDriftBeforeAnyReplacement(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	ctx := context.Background()
	refs, err := s.planReferences()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(entries[1].WorkingPath, []byte("external edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Prepare(ctx, Plan{NativeFingerprints: refs}); err == nil {
		t.Fatal("native drift accepted")
	}
	s.validate(ctx, entries[0].ID, entries[0].WorkingPath)
}

func TestGatewayRecoveryKeepsSeparateWorkingAndSystemBackups(t *testing.T) {
	ctx := context.Background()
	s, entries, _ := fileStoreFixture(t)
	for _, entry := range entries {
		names := map[templatecatalog.ID]string{templatecatalog.XrayConfig: "xray/config.json", templatecatalog.Nftables: "nftables/nftables.conf", templatecatalog.TProxyUnit: "systemd/xray-tproxy-routing.service", templatecatalog.Sysctl: "sysctl/99-xray.conf"}
		raw, _ := os.ReadFile("../../templConf/" + names[entry.ID])
		os.WriteFile(entry.WorkingPath, raw, 0600)
	}
	prior := []byte(`{"log":{"loglevel":"warning"},"unknown":"preserve"}`)
	os.WriteFile(entries[0].SystemPath, prior, 0600)
	s.checkRelations = true
	s.identity = func(context.Context) (int, error) { return 4242, nil }
	plan, err := s.AnalyzeGateway(ctx, Plan{Interface: "lan0", ListenAddress: "10.0.0.1", LANNetwork: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreserveXray {
		t.Fatal("partial config treated as complete")
	}
	tx, err := s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(entries[0].SystemPath)
	if !bytes.Equal(prior, restored) {
		t.Fatal("system restored from working backup")
	}
	factory, _ := os.ReadFile("../../templConf/xray/config.json")
	working, _ := os.ReadFile(entries[0].WorkingPath)
	if !bytes.Equal(working, factory) {
		t.Fatal("working restored from system backup")
	}
}
