package nativewrite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFixedCommitAndRemove(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "nftables.conf")
	for _, remove := range []bool{false, true} {
		raw, _ := json.Marshal(intent{Remove: remove, Content: []byte("table inet xray {}\n"), Mode: 0644, UID: os.Getuid(), GID: os.Getgid()})
		if err := os.WriteFile(filepath.Join(root, "intent.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := execute(root, target); err != nil {
			t.Fatal(err)
		}
		if remove {
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal(err)
			}
		} else {
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatal(info, err)
			}
		}
	}
}
func TestRejectUnsafeIntent(t *testing.T) {
	for _, raw := range []string{`{"Target":"/etc/hosts"}`, `{"Mode":4095}`, `{} {}`} {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(filepath.Join(root, "intent.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := execute(root, target); err == nil {
			t.Fatal("accepted unsafe intent")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("destination changed")
		}
	}
	root := t.TempDir()
	real := filepath.Join(root, "real")
	os.WriteFile(real, []byte(`{}`), 0600)
	os.Symlink(real, filepath.Join(root, "intent.json"))
	if execute(root, filepath.Join(root, "out")) == nil {
		t.Fatal("accepted symlink")
	}
}
