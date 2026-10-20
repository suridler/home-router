package systemsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/transaction"
)

type loggingState struct {
	state   transaction.Metadata
	pending *transaction.Pending
}

func (s *loggingState) Metadata(context.Context) (transaction.Metadata, error) { return s.state, nil }
func (s *loggingState) Pending(context.Context) (*transaction.Pending, error)  { return s.pending, nil }

func settingsFixture(t *testing.T) (*Service, *loggingState, transaction.Metadata, transaction.Metadata) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := []byte("# operator comment\n[backup]\nretain_per_template = 13\n[logging]\naccess_path = 'none'\nerror_path = ''\nlevel = 'warning'\ndns_log = false\n")
	if err := os.WriteFile(path, raw, 0640); err != nil {
		t.Fatal(err)
	}
	c, err := bootstrap.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Xray.Config = "/discovered/xray.json"
	s, err := New(path, c)
	if err != nil {
		t.Fatal(err)
	}
	before := transaction.Metadata{Generation: 4, ContentHash: digest([]byte("old Xray"))}
	after := transaction.Metadata{Generation: 5, ContentHash: digest([]byte("new Xray"))}
	state := &loggingState{state: before}
	s.SetLoggingMetadata(state)
	return s, state, before, after
}

func nextLogs() bootstrap.LoggingConfig {
	return bootstrap.LoggingConfig{AccessPath: "/custom/access.log", ErrorPath: "", Level: "info", DNSLog: true}
}

func TestLoggingSettingsCommitRollbackAndUnrelatedPut(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			s, state, before, after := settingsFixture(t)
			original, _ := os.ReadFile(s.path)
			snapshot, err := s.PrepareLogging(context.Background(), nextLogs(), before, after)
			if err != nil {
				t.Fatal(err)
			}
			if commit {
				state.state = after
				err = snapshot.Commit()
			} else {
				err = snapshot.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			c, err := bootstrap.Load(s.path)
			if err != nil {
				t.Fatal(err)
			}
			if commit {
				if c.Logging != nextLogs() || s.Logging() != nextLogs() {
					t.Fatal("logging not committed")
				}
				if s.config.Xray.Config != "/discovered/xray.json" {
					t.Fatal("service path lost")
				}
			} else {
				actual, _ := os.ReadFile(s.path)
				if !bytes.Equal(original, actual) {
					t.Fatal("rollback did not restore exact TOML")
				}
			}
			info, _ := os.Stat(s.path)
			if info.Mode().Perm() != 0640 {
				t.Fatal("metadata lost")
			}
			if _, err := s.Put(context.Background(), api.UpdateSystemSettingsRequest{Backup: api.BackupSettings{RetainPerTemplate: 17}}); err != nil {
				t.Fatal(err)
			}
			c, _ = bootstrap.Load(s.path)
			if commit && c.Logging != nextLogs() {
				t.Fatal("unrelated PUT reverted logging")
			}
			if _, err := os.Stat(s.journalPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal remains: %v", err)
			}
		})
	}
}

func TestLoggingSettingsRecoveryUsesXrayCommitAfterCrash(t *testing.T) {
	for _, phase := range []string{"intent before TOML rename", "TOML prepared", "Xray committed", "no-op Xray"} {
		t.Run(phase, func(t *testing.T) {
			s, state, before, after := settingsFixture(t)
			if phase == "no-op Xray" {
				after = before
			}
			original, _ := os.ReadFile(s.path)
			_, err := s.PrepareLogging(context.Background(), nextLogs(), before, after)
			if err != nil {
				t.Fatal(err)
			}
			// Discard the held in-process snapshot, as after process termination. Fresh
			// service below does not use its mutex or memory state.
			if phase == "intent before TOML rename" {
				if err := replace(s.path, original); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "Xray committed" {
				state.state = after
			}
			config, err := bootstrap.Load(s.path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := New(s.path, config)
			if err != nil {
				t.Fatal(err)
			}
			fresh.SetLoggingMetadata(state)
			if err := fresh.RecoverLogging(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := bootstrap.Defaults().Logging
			if phase == "Xray committed" || phase == "no-op Xray" {
				want = nextLogs()
			}
			if fresh.Logging() != want {
				t.Fatalf("recovered logging=%+v; want %+v", fresh.Logging(), want)
			}
			if err := fresh.RecoverLogging(context.Background()); err != nil {
				t.Fatalf("recovery not idempotent: %v", err)
			}
		})
	}
}

func TestLoggingSettingsRecoveryRejectsDriftAndPendingXray(t *testing.T) {
	for _, drift := range []string{"TOML", "metadata", "pending", "malformed journal", "symlink journal"} {
		t.Run(drift, func(t *testing.T) {
			s, state, before, after := settingsFixture(t)
			_, err := s.PrepareLogging(context.Background(), nextLogs(), before, after)
			if err != nil {
				t.Fatal(err)
			}
			switch drift {
			case "TOML":
				if err := os.WriteFile(s.path, []byte("[backup]\nretain_per_template=33\n"), 0640); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				state.state.Generation++
			case "pending":
				state.pending = &transaction.Pending{ID: "pending"}
			case "malformed journal":
				if err := os.WriteFile(s.journalPath(), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink journal":
				target := s.journalPath() + ".saved"
				if err := os.Rename(s.journalPath(), target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, s.journalPath()); err != nil {
					t.Fatal(err)
				}
			}
			beforeDisk, _ := os.ReadFile(s.path)
			config, _ := bootstrap.Load(s.path)
			fresh, err := New(s.path, config)
			if err != nil {
				t.Fatal(err)
			}
			fresh.SetLoggingMetadata(state)
			if err := fresh.RecoverLogging(context.Background()); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			afterDisk, _ := os.ReadFile(s.path)
			if !bytes.Equal(beforeDisk, afterDisk) {
				t.Fatal("unsafe recovery changed TOML")
			}
			if _, err := os.Lstat(s.journalPath()); err != nil {
				t.Fatal("evidence removed")
			}
		})
	}
}

func TestLoggingSettingsRollbackAfterXrayCommitKeepsMatchingTOML(t *testing.T) {
	s, state, before, after := settingsFixture(t)
	snapshot, err := s.PrepareLogging(context.Background(), nextLogs(), before, after)
	if err != nil {
		t.Fatal(err)
	}
	state.state = after
	// Another preparation's Commit may fail after the Xray DB commit. Its
	// rollback must never revert TOML to settings for the previous Xray hash.
	if err := snapshot.Rollback(); err != nil {
		t.Fatal(err)
	}
	if s.Logging() != nextLogs() {
		t.Fatal("committed Xray and TOML diverged")
	}
}

func TestLoggingSettingsRejectsExternalEditsBeforeIntent(t *testing.T) {
	s, _, before, after := settingsFixture(t)
	raw := []byte("[backup]\nretain_per_template=25\n")
	if err := os.WriteFile(s.path, raw, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareLogging(context.Background(), nextLogs(), before, after); !errors.Is(err, ErrSettingsDrift) {
		t.Fatalf("drift=%v", err)
	}
	if _, err := os.Stat(s.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("drift created journal")
	}
}

func TestLoggingJournalCannotAlterUnrelatedSettings(t *testing.T) {
	s, state, before, after := settingsFixture(t)
	_, err := s.PrepareLogging(context.Background(), nextLogs(), before, after)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.journalPath())
	var j loggingJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	j.Next = bytes.ReplaceAll(j.Next, []byte("retain_per_template = 13"), []byte("retain_per_template = 44"))
	raw, _ = json.Marshal(j)
	if err := os.WriteFile(s.journalPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	config, _ := bootstrap.Load(s.path)
	fresh, _ := New(s.path, config)
	fresh.SetLoggingMetadata(state)
	if err := fresh.RecoverLogging(context.Background()); !errors.Is(err, ErrLoggingRecovery) {
		t.Fatalf("unrelated change accepted: %v", err)
	}
}
