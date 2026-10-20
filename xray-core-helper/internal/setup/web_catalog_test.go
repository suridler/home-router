package setup

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestRetiredWebManifestFailsClosedAndRetainsEvidence(t *testing.T) {
	s, _, _ := fileStoreFixture(t)
	tx, err := s.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	manifest := tx.(*fileTransaction).manifest
	manifest.Items = append(manifest.Items, setupFile{ID: templatecatalog.ID("web-config"), Target: "/etc/xrayui-web/xrayui-web.env"})
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.pendingPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(context.Background()); err == nil {
		t.Fatal("obsolete manifest accepted")
	}
	actual, err := os.ReadFile(s.pendingPath())
	if err != nil || string(actual) != string(raw) {
		t.Fatal("recovery evidence discarded")
	}
}
