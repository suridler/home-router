package safeapply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/templateapply"
	"home-router/xray-core-helper/internal/templatecatalog"
)

func baselineFixture() []BaselineFile {
	return []BaselineFile{
		{TemplateID: templatecatalog.Nftables, Present: true, Content: []byte("# private original\n"), Mode: 0640, UID: 10, GID: 20},
		{TemplateID: templatecatalog.Sysctl, Content: []byte{}},
		{TemplateID: templateapply.LifecycleScriptID, Content: []byte{}},
		{TemplateID: templateapply.LifecycleSysctlID, Content: []byte{}},
		{TemplateID: templatecatalog.TProxyUnit, Present: true, Content: []byte{}, Mode: 0600},
		{TemplateID: templateapply.LifecycleDropinID, Content: []byte{}},
	}
}

func beginBaseline(t *testing.T, store *Store, id templatecatalog.ID) Transaction {
	t.Helper()
	value, err := store.Begin(context.Background(), id, "backup", strings.Repeat("a", 64), 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBaselinePersistsCompleteGroupAndAbsence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	transaction := beginBaseline(t, store, templatecatalog.Nftables)
	files := baselineFixture()
	if err := store.RecordBaseline(ctx, transaction.ID, files); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordBaseline(ctx, transaction.ID, files); !errors.Is(err, ErrPhase) {
		t.Fatalf("overwrite: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	defer store.Close()
	got, err := store.Baseline(ctx, transaction.ID)
	if err != nil || !reflect.DeepEqual(got, files) {
		t.Fatalf("baseline mismatch (content withheld): err=%v", err)
	}
	got[0].Content[0] = '!'
	again, err := store.Baseline(ctx, transaction.ID)
	if err != nil || !reflect.DeepEqual(again, files) {
		t.Fatal("caller changed durable evidence")
	}
}

func TestBaselineRejectsInvalidOrIncompleteGroups(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "xray", "oversize", "absence-content", "absence-mode", "special-mode"} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
			defer store.Close()
			transaction := beginBaseline(t, store, templatecatalog.Sysctl)
			files := baselineFixture()
			switch name {
			case "missing":
				files = files[:2]
			case "duplicate":
				files[2] = files[0]
			case "xray":
				files[0].TemplateID = templatecatalog.XrayConfig
			case "oversize":
				files[0].Content = make([]byte, maxBaselineBytes+1)
			case "absence-content":
				files[1].Content = []byte("unexpected")
			case "absence-mode":
				files[1].Mode = 0600
			case "special-mode":
				files[0].Mode = 04755
			}
			if err := store.RecordBaseline(context.Background(), transaction.ID, files); !errors.Is(err, ErrBaseline) {
				t.Fatalf("invalid group: %v", err)
			}
			if _, err := store.Baseline(context.Background(), transaction.ID); !errors.Is(err, ErrBaseline) {
				t.Fatal("partial baseline survived")
			}
		})
	}
}

func TestBaselineInsertFailureIsAtomic(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	transaction := beginBaseline(t, store, templatecatalog.Nftables)
	_, err := store.db.Exec(`CREATE TEMP TRIGGER fail_member BEFORE INSERT ON safe_apply_baseline WHEN NEW.template_id='sysctl' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordBaseline(context.Background(), transaction.ID, baselineFixture()); err == nil {
		t.Fatal("expected insertion failure")
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM safe_apply_baseline`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial insert count=%d err=%v", count, err)
	}
}

func TestBaselineRefusesLegacyXrayAndLateCapture(t *testing.T) {
	for _, name := range []string{"legacy", "xray", "applied"} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
			defer store.Close()
			id := templatecatalog.Nftables
			if name == "xray" {
				id = templatecatalog.XrayConfig
			}
			transaction := beginBaseline(t, store, id)
			if _, err := store.Baseline(context.Background(), transaction.ID); !errors.Is(err, ErrBaseline) {
				t.Fatal("invented legacy baseline")
			}
			if name == "legacy" {
				return
			}
			if name == "applied" {
				if _, err := store.Advance(context.Background(), transaction.ID, PhasePrepared, PhaseApplied); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.RecordBaseline(context.Background(), transaction.ID, baselineFixture()); !errors.Is(err, ErrPhase) {
				t.Fatalf("late/invalid capture: %v", err)
			}
		})
	}
}

func TestGroupRetentionNeverPrunesPendingEvidence(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	ctx := context.Background()
	var terminal []string
	for i := 0; i < 3; i++ {
		op := beginBaseline(t, store, templatecatalog.Nftables)
		if err := store.RecordBaseline(ctx, op.ID, baselineFixture()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Resolve(ctx, op.ID, StateConfirmed); err != nil {
			t.Fatal(err)
		}
		terminal = append(terminal, op.ID)
	}
	pending := beginBaseline(t, store, templatecatalog.Sysctl)
	if err := store.RecordBaseline(ctx, pending.ID, baselineFixture()); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneBaselines(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Baseline(ctx, pending.ID); err != nil {
		t.Fatal("pending group pruned", err)
	}
	count := 0
	for _, id := range terminal {
		if _, err := store.Baseline(ctx, id); err == nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("terminal retained groups=%d", count)
	}
}

func TestBaselineDatabaseIsPrivateAndRejectsSymlinks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s := openTestStore(t, path)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("baseline database not private", err)
	}
	link := filepath.Join(t.TempDir(), "link.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(ctx, link); err == nil {
		store.Close()
		t.Fatal("followed database symlink")
	}
}

func TestLegacyThreeFileEvidenceIsReadOnlyAndNeverExpanded(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	op := beginBaseline(t, store, templatecatalog.TProxyUnit)
	var legacy []BaselineFile
	for _, file := range baselineFixture() {
		if systemMember(file.TemplateID) {
			legacy = append(legacy, file)
		}
	}
	if err := store.RecordBaseline(ctx, op.ID, legacy); !errors.Is(err, ErrBaseline) {
		t.Fatal("new incomplete group accepted", err)
	}
	for _, file := range legacy {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO safe_apply_baseline(transaction_id,template_id,present,content,mode,uid,gid) VALUES(?,?,?,?,?,?,?)`, op.ID, file.TemplateID, file.Present, file.Content, file.Mode, file.UID, file.GID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Baseline(ctx, op.ID)
	if err != nil || len(got) != 3 {
		t.Fatal("legacy recovery unavailable", err)
	}
	for _, file := range got {
		if !systemMember(file.TemplateID) {
			t.Fatal("synthesized auxiliary evidence")
		}
	}
}
