package templatebackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/templatecatalog"
)

func fixture(t *testing.T, retain int) (*Store, templatecatalog.ID, string, string) {
	t.Helper()
	root := t.TempDir()
	template := filepath.Join(root, "template.conf")
	system := filepath.Join(root, "system.conf")
	if err := os.WriteFile(template, []byte("template-v1\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, []byte("system-v1\n"), 0604); err != nil {
		t.Fatal(err)
	}
	id := templatecatalog.ID("test")
	store, err := newStore(filepath.Join(root, "backups"), map[templatecatalog.ID]templatecatalog.Entry{id: {ID: id, WorkingPath: template, SystemPath: system}}, func() int { return retain }, func(context.Context, templatecatalog.ID, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC)
	tick := 0
	store.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Nanosecond) }
	return store, id, template, system
}

func TestSnapshotUsesServiceConfigDestination(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "service.json")
	if err := os.WriteFile(active, []byte("service-selected config"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := templatecatalog.ForXrayConfig(active)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewWithCatalog(func() int { return 10 }, nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	store.root = filepath.Join(root, "backups")
	record, err := store.Snapshot(context.Background(), templatecatalog.XrayConfig, SideSystem, StateCompleted)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("service-selected config"))
	if record.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("backed up another config")
	}
}

func TestSnapshotIsPrivateDurableMetadataAndExactBytes(t *testing.T) {
	store, id, template, _ := fixture(t, 10)
	record, err := store.Snapshot(context.Background(), id, SideTemplate, StateCompleted)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(store.root, string(id), record.BackupID)
	for _, test := range []struct {
		name string
		mode os.FileMode
	}{{"", 0700}, {"content", 0600}, {"metadata.json", 0600}} {
		info, err := os.Stat(filepath.Join(directory, test.name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != test.mode {
			t.Errorf("%s mode=%o", test.name, info.Mode().Perm())
		}
	}
	want, _ := os.ReadFile(template)
	got, _ := os.ReadFile(filepath.Join(directory, "content"))
	if string(got) != string(want) {
		t.Fatalf("content=%q want=%q", got, want)
	}
	if record.Hash == "" || record.Mode != 0640 || record.Generation != 1 || record.TemplateID != "test" {
		t.Fatalf("record=%#v", record)
	}
}

func TestSystemBackupCannotBypassGroupRecovery(t *testing.T) {
	store, oldID, template, system := fixture(t, 10)
	entry := store.entries[oldID]
	delete(store.entries, oldID)
	entry.ID = templatecatalog.Nftables
	store.entries[entry.ID] = entry
	op, err := store.Snapshot(context.Background(), entry.ID, SideSystem, StateCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, []byte("current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(context.Background(), entry.ID, op.BackupID, op.Generation); !errors.Is(err, ErrSafeApply) {
		t.Fatal("unsafe system restore", err)
	}
	raw, _ := os.ReadFile(system)
	if string(raw) != "current\n" {
		t.Fatal("system modified without journal")
	}
	working, err := store.Snapshot(context.Background(), entry.ID, SideTemplate, StateCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(template, []byte("edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(context.Background(), entry.ID, working.BackupID, working.Generation); err != nil {
		t.Fatal("working-template restore rejected", err)
	}
}

func TestSnapshotRejectsSymlinkSource(t *testing.T) {
	store, id, template, _ := fixture(t, 10)
	real := template + ".real"
	if err := os.Rename(template, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, template); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Snapshot(context.Background(), id, SideTemplate, StateCompleted); !errors.Is(err, ErrFilesystem) {
		t.Fatalf("err=%v", err)
	}
}

func TestRetentionIsPerTemplateAndProtectsEvidence(t *testing.T) {
	store, id, template, _ := fixture(t, 2)
	states := []State{StateCompleted, StatePending, StateCompleted, StateRollback, StateCompleted, StateRecoveryRequired, StateLastKnownGood}
	for index, state := range states {
		if err := os.WriteFile(template, []byte{byte('a' + index)}, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Snapshot(context.Background(), id, SideTemplate, state); err != nil {
			t.Fatal(err)
		}
	}
	records, generation, err := store.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 7 || len(records) != 6 {
		t.Fatalf("generation=%d records=%#v", generation, records)
	}
	counts := map[State]int{}
	for _, record := range records {
		counts[record.State]++
	}
	if counts[StateCompleted] != 2 || counts[StatePending] != 1 || counts[StateRollback] != 1 || counts[StateRecoveryRequired] != 1 || counts[StateLastKnownGood] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	if err := store.Cleanup(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestRestorePendingIsIdempotentAndDoesNotAdvanceGeneration(t *testing.T) {
	store, id, _, system := fixture(t, 10)
	pending, err := store.Snapshot(context.Background(), id, SideSystem, StatePending)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(system, []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = store.RestorePending(context.Background(), id, pending.BackupID); err != nil {
			t.Fatal(err)
		}
	}
	contents, err := os.ReadFile(system)
	if err != nil || string(contents) != "system-v1\n" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	records, generation, err := store.List(context.Background(), id)
	if err != nil || generation != pending.Generation || len(records) != 1 {
		t.Fatalf("generation=%d records=%#v err=%v", generation, records, err)
	}
}

func TestPendingSnapshotCanCompleteAndTriggerRetention(t *testing.T) {
	store, id, template, _ := fixture(t, 1)
	first, err := store.Snapshot(context.Background(), id, SideTemplate, StateCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(template, []byte("next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Snapshot(context.Background(), id, SideTemplate, StatePending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetState(context.Background(), id, pending.BackupID, StateCompleted); err != nil {
		t.Fatal(err)
	}
	records, _, err := store.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].BackupID != pending.BackupID || records[0].BackupID == first.BackupID {
		t.Fatalf("records=%#v", records)
	}
}

func TestRestoreChecksGenerationValidatesAndIsTransactional(t *testing.T) {
	store, id, template, _ := fixture(t, 10)
	backup, err := store.Snapshot(context.Background(), id, SideTemplate, StateCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(template, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(context.Background(), id, backup.BackupID, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	store.validate = func(context.Context, templatecatalog.ID, string) error { return errors.New("invalid") }
	if _, err := store.Restore(context.Background(), id, backup.BackupID, 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("validation err=%v", err)
	}
	got, _ := os.ReadFile(template)
	if string(got) != "changed\n" {
		t.Fatal("validation failure changed target")
	}
	store.validate = func(context.Context, templatecatalog.ID, string) error { return nil }
	store.replace = func(string, []byte, os.FileMode, int, int) error { return ErrFilesystem }
	if _, err := store.Restore(context.Background(), id, backup.BackupID, 1); !errors.Is(err, ErrFilesystem) {
		t.Fatalf("replace err=%v", err)
	}
	got, _ = os.ReadFile(template)
	if string(got) != "changed\n" {
		t.Fatal("replace failure changed target")
	}
	_, generation, err := store.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	store.replace = atomicReplace
	if _, err := store.Restore(context.Background(), id, backup.BackupID, generation); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(template)
	if string(got) != "template-v1\n" {
		t.Fatalf("restored=%q", got)
	}
	info, _ := os.Stat(template)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
