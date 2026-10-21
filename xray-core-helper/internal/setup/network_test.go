package setup

import (
	"bytes"
	"context"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkSelectionParticipatesInSetupCommitAndCrashRecovery(t *testing.T) {
	ctx := context.Background()
	s, entries, root := fileStoreFixture(t)
	s.checkRelations = true
	s.identity = func(context.Context) (int, error) { return os.Getegid(), nil }
	raw, err := os.ReadFile("../../templConf/xray/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(entries[0].WorkingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.toml")
	original := []byte("# retain exact original on rollback\n[network]\nintercept_mark = 2\npolicy_table = 321\n")
	if err = os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err = ConfigureNetwork(s, path); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AnalyzeGateway(ctx, Plan{ReplaceXray: true, Interface: "br-test", ListenAddress: "10.77.0.1", LANNetwork: "10.77.0.0/24"})
	if err != nil {
		t.Fatal(err)
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
	c, err := testconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Network.Interface != "br-test" || c.Network.Address != "10.77.0.1" || c.Network.IPv4Prefix != "10.77.0.0/24" || c.Network.InterceptMark != 2 || c.Network.PolicyTable != 321 {
		t.Fatal("network selection not persisted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal("config permissions changed")
	}
	// A fresh process reads the committed selection, then restores the durable group.
	fresh, err := newFileStore(s.root, s.entries, s.config, s.validate)
	if err != nil {
		t.Fatal(err)
	}
	if err = ConfigureNetwork(fresh, path); err != nil {
		t.Fatal(err)
	}
	if err = fresh.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatal("config rollback not exact", err)
	}
	if fresh.network.Interface != "" || fresh.network.PolicyTable != 321 {
		t.Fatal("recovered process retained stale selection")
	}
	// Recovery is idempotent.
	if err = fresh.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	// Complete keeps the selection across a new process without recovery rollback.
	tx, err = s.Prepare(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Complete(ctx); err != nil {
		t.Fatal(err)
	}
	if err = ConfigureNetwork(fresh, path); err != nil {
		t.Fatal(err)
	}
	if err = fresh.Recover(ctx); err != nil || fresh.network.Interface != "br-test" {
		t.Fatal("completed selection lost", err)
	}
}

func TestNetworkManifestCannotRedirectHelperConfig(t *testing.T) {
	s, _, root := fileStoreFixture(t)
	// Satisfy fixture's validator assertion without a host operation.
	if err := s.validate(context.Background(), s.entries[0].ID, s.entries[0].WorkingPath); err != nil {
		t.Fatal(err)
	}
	s.configPath = filepath.Join(root, "config.toml")
	m := setupManifest{Version: 4, ID: "transaction", Network: &setupFile{ID: networkID, Target: filepath.Join(root, "other.toml")}}
	if s.validateNetworkManifest(m) == nil {
		t.Fatal("redirected network target accepted")
	}
	if s.validateNetworkManifest(setupManifest{Version: 4}) == nil {
		t.Fatal("missing network evidence accepted")
	}
}
