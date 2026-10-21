package setup

import (
	"context"
	"home-router/xray-core-helper/internal/filemetadata"
	"home-router/xray-core-helper/internal/nativeblock"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/testconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fileStoreFixture(t *testing.T) (*fileStore, []templatecatalog.Entry, string) {
	t.Helper()
	root := t.TempDir()
	var entries []templatecatalog.Entry
	for _, id := range []templatecatalog.ID{templatecatalog.XrayConfig, templatecatalog.Nftables, templatecatalog.TProxyUnit, templatecatalog.Sysctl} {
		working := filepath.Join(root, "working", string(id))
		system := filepath.Join(root, "system", string(id))
		body := "native\n"
		if id == templatecatalog.Nftables {
			body = "# helper:v1 bind ingress type=interface source=setup.interface token=\"\\\"example0\\\"\"\ndefine lan_interface = \"example0\"\n# helper:v1 bind network type=ipv4-prefix source=setup.ipv4-prefix token=\"192.0.2.0/24\"\ndefine lan_network = 192.0.2.0/24\n# helper:v1 bind socketgroup type=uint32 source=xray.service-gid token=\"65534\"\ndefine xray_gid = 65534\ntable inet xray {}\n"
		}
		if id == templatecatalog.Nftables {
			body = nativeblock.Begin + "\n" + body + nativeblock.End + "\n"
		}
		if err := os.MkdirAll(filepath.Dir(working), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(working, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, templatecatalog.Entry{ID: id, WorkingPath: working, SystemPath: system})
	}
	if err := os.MkdirAll(filepath.Dir(entries[0].SystemPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entries[0].SystemPath, nil, 0640); err != nil {
		t.Fatal(err)
	}
	validated := 0
	store, err := newFileStore(filepath.Join(root, "state"), entries, testconfig.Defaults(), func(_ context.Context, _ templatecatalog.ID, path string) error {
		validated++
		if _, err := os.Stat(path); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	store.workingMetadata = func() filemetadata.Metadata {
		return filemetadata.Metadata{Mode: 0600, UID: os.Geteuid(), GID: os.Getegid()}
	}
	store.now = func() time.Time { return time.Unix(1, 0) }
	t.Cleanup(func() {
		if validated == 0 {
			t.Error("candidates were not validated")
		}
	})
	return store, entries, root
}
func TestFileTransactionRendersNativeValuesAndRestoresExactEmptyConfig(t *testing.T) {
	store, entries, _ := fileStoreFixture(t)
	plan := Plan{Interface: "lan0", ListenAddress: "192.168.50.1", LANNetwork: "192.168.50.0/24"}
	rawTx, err := store.Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	tx := rawTx.(*fileTransaction)
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	nft, _ := os.ReadFile(entries[1].SystemPath)
	if !strings.Contains(string(nft), `"lan0"`) || !strings.Contains(string(nft), "192.168.50.0/24") {
		t.Fatalf("nft=%s", nft)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	xray, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(xray) != 0 {
		t.Fatalf("xray=%q err=%v", xray, err)
	}
	for _, entry := range entries[1:] {
		if _, err = os.Stat(entry.SystemPath); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", entry.SystemPath)
		}
	}
}
func TestFileStoreRecoversCommittedCrashFromDurableManifest(t *testing.T) {
	store, entries, _ := fileStoreFixture(t)
	tx, err := store.Prepare(context.Background(), Plan{Interface: "lan0", ListenAddress: "192.0.2.1", LANNetwork: "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(entries[0].SystemPath)
	if err != nil || len(raw) != 0 {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if _, err = os.Stat(store.pendingPath()); !os.IsNotExist(err) {
		t.Fatalf("manifest err=%v", err)
	}
}
