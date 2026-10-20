package setup

import (
	"context"
	"home-router/xray-core-helper/internal/templatecatalog"
	"path/filepath"
	"testing"
)

func TestSetupCandidatesKeepNativeExtensions(t *testing.T) {
	store, _, _ := fileStoreFixture(t)
	extensions := map[templatecatalog.ID]string{templatecatalog.XrayConfig: ".json", templatecatalog.Nftables: ".conf", templatecatalog.TProxyUnit: ".service", templatecatalog.Sysctl: ".conf"}
	for i := range store.entries {
		store.entries[i].SystemPath += extensions[store.entries[i].ID]
	}
	original := store.validate
	store.validate = func(ctx context.Context, id templatecatalog.ID, path string) error {
		if filepath.Ext(path) != extensions[id] {
			t.Fatalf("native validator received wrong extension for %s", id)
		}
		return original(ctx, id, path)
	}
	tx, err := store.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}
