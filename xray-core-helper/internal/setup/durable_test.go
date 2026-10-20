package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDurableSetupFaultsRetainEvidenceAndRestoreOriginal(t *testing.T) {
	for _, stage := range []string{"write", "file-sync", "rename", "directory-sync"} {
		t.Run(stage, func(t *testing.T) {
			s, entries, _ := fileStoreFixture(t)
			tx, err := s.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "192.0.2.0/24"})
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Validate(context.Background()); err != nil {
				t.Fatal(err)
			}
			s.fault = func(at, path string) error {
				if at == stage && path == entries[0].SystemPath {
					return errors.New("injected")
				}
				return nil
			}
			if err = tx.Commit(context.Background()); err == nil {
				t.Fatal("commit succeeded")
			}
			if err = tx.Rollback(context.Background()); err == nil {
				t.Fatal("restore failure ignored")
			}
			if _, err = os.Stat(s.pendingPath()); err != nil {
				t.Fatal("pending evidence lost")
			}
			s.fault = nil
			if err = s.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			raw, info, err := readSafe(entries[0].SystemPath)
			if err != nil || len(raw) != 0 || info.Mode().Perm() != 0640 {
				t.Fatalf("original not restored: %v", err)
			}
			stat := info.Sys().(*syscall.Stat_t)
			if int(stat.Uid) != os.Getuid() || int(stat.Gid) != os.Getgid() {
				t.Fatal("owner changed")
			}
		})
	}
}

func TestManifestRejectsTamperingBeforeAnyTargetChanges(t *testing.T) {
	for _, kind := range []string{"target", "backup", "candidate", "version", "count", "duplicate", "hash", "symlink", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, entries, root := fileStoreFixture(t)
			tx, err := s.Prepare(context.Background(), Plan{Interface: "lan0", LANNetwork: "192.0.2.0/24"})
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Validate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			m := tx.(*fileTransaction).manifest
			switch kind {
			case "target":
				m.Items[0].Target = filepath.Join(root, "outside")
			case "backup":
				m.Items[0].Backup = filepath.Join(root, "outside")
			case "candidate":
				m.Items[0].Candidate = filepath.Join(root, "outside")
			case "version":
				m.Version = 99
			case "count":
				m.Items = nil
			case "duplicate":
				m.Items[1] = m.Items[0]
			case "hash":
				m.Items[0].Hash = contentHash([]byte("wrong"))
			case "symlink":
				if err = os.Remove(m.Items[0].Backup); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(entries[0].SystemPath, m.Items[0].Backup); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				dir := filepath.Join(s.root, m.ID)
				if err = os.Rename(dir, dir+"-real"); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(dir+"-real", dir); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(m)
			if err = os.WriteFile(s.pendingPath(), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = s.Recover(context.Background()); err == nil {
				t.Fatal("tampered recovery succeeded")
			}
			for _, entry := range entries {
				if _, err = os.Stat(entry.SystemPath); err != nil {
					t.Fatalf("target modified before validating evidence: %s", entry.ID)
				}
			}
			if _, err = os.Stat(s.pendingPath()); err != nil {
				t.Fatal("evidence deleted")
			}
		})
	}
}

func TestFailedBackupSyncDoesNotPublishManifest(t *testing.T) {
	s, entries, _ := fileStoreFixture(t)
	s.fault = func(stage, path string) error {
		if stage == "file-sync" {
			return errors.New("injected")
		}
		return nil
	}
	if _, err := s.Prepare(context.Background(), Plan{}); err == nil {
		t.Fatal("prepare succeeded")
	}
	if _, err := os.Stat(s.pendingPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manifest published before evidence synced")
	}
	raw, _ := os.ReadFile(entries[0].SystemPath)
	if len(raw) != 0 {
		t.Fatal("system changed")
	}
	// This fixture intentionally fails before candidate validation.
	s.fault = nil
	if err := s.validate(context.Background(), entries[0].ID, entries[0].WorkingPath); err != nil {
		t.Fatal(err)
	}
}
