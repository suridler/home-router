package consolidation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatainstall"
)

const (
	testAssetPath                = "/usr/local/share/xray"
	defaultTestGeositeName       = "geosite_user.dat"
	defaultTestCandidateName     = defaultTestGeositeName
	defaultTestBackupName        = defaultTestGeositeName + ".previous"
	defaultTestLiveCandidateName = "." + defaultTestGeositeName + ".xch-candidate"
)

type fakeRunner struct {
	err           error
	mode          string
	calls         int
	input         string
	binary        string
	args          []string
	root          string
	candidateName string
}

func (runner *fakeRunner) Run(_ context.Context, binary string, args []string) error {
	runner.calls++
	runner.binary = binary
	runner.args = append([]string(nil), args...)
	if runner.err != nil {
		return runner.err
	}
	input, _ := os.ReadFile(filepath.Join(runner.root, pendingName, inputName))
	runner.input = string(input)
	if runner.mode == "no-output" {
		return nil
	}
	candidateName := runner.candidateName
	if candidateName == "" {
		candidateName = defaultTestCandidateName
	}
	output := filepath.Join(runner.root, pendingName, candidateName)
	if runner.mode == "symlink" {
		return os.Symlink(filepath.Join(filepath.Dir(output), "missing"), output)
	}
	content := []byte("custom geosite candidate\x00\n")
	if runner.mode == "empty" {
		content = nil
	}
	return os.WriteFile(output, content, 0600)
}

type fakeValidator struct {
	err           error
	onValidate    func(string) error
	calls         int
	root          string
	candidateName string
}

func (validator *fakeValidator) ValidateCustomGeosite(_ context.Context, path, expectedHash string) error {
	validator.calls++
	candidateName := validator.candidateName
	if candidateName == "" {
		candidateName = defaultTestCandidateName
	}
	if validator.onValidate != nil {
		return validator.onValidate(filepath.Join(validator.root, pendingName, candidateName))
	}
	candidate, err := os.ReadFile(filepath.Join(validator.root, pendingName, candidateName))
	if err != nil || hashBytes(candidate) != expectedHash {
		return ErrBuilderOutput
	}
	return validator.err
}

func (validator *fakeValidator) ValidateCustomAssets(ctx context.Context, path, hash, ipPath, ipHash string) error {
	if err := validator.ValidateCustomGeosite(ctx, path, hash); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(validator.root, pendingName, filepath.Base(ipPath)))
	if err != nil || hashBytes(data) != ipHash {
		return ErrBuilderOutput
	}
	return nil
}

type testFileSystem struct {
	base     string
	mu       sync.Mutex
	failures map[string]map[int]bool
	hits     map[string]int
	events   []string
}

func newTestFileSystem(base string) *testFileSystem {
	return &testFileSystem{
		base: base, failures: make(map[string]map[int]bool), hits: make(map[string]int),
	}
}

func (files *testFileSystem) OpenDir(path string) (geodatainstall.Directory, error) {
	label := "asset"
	if path == testAssetPath {
		path = files.base
	}
	directory, err := geodatainstall.OpenDirectory(path)
	if err != nil {
		return nil, err
	}
	return &testDirectory{Directory: directory, files: files, label: label}, nil
}

func (files *testFileSystem) OpenSource(path string) (geodatainstall.File, error) {
	return geodatainstall.OpenSourceFile(path)
}

func (files *testFileSystem) failOn(event string, occurrences ...int) {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.failures[event] = make(map[int]bool)
	for _, occurrence := range occurrences {
		files.failures[event][occurrence] = true
	}
}

func (files *testFileSystem) record(event string) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.events = append(files.events, event)
	files.hits[event]++
	if files.failures[event][files.hits[event]] {
		return fmt.Errorf("injected failure at %s", event)
	}
	return nil
}

func (files *testFileSystem) count(event string) int {
	files.mu.Lock()
	defer files.mu.Unlock()
	return files.hits[event]
}

func (files *testFileSystem) eventsCopy() []string {
	files.mu.Lock()
	defer files.mu.Unlock()
	return append([]string(nil), files.events...)
}

type testDirectory struct {
	geodatainstall.Directory
	files *testFileSystem
	label string
}

func (directory *testDirectory) OpenDir(name string) (geodatainstall.Directory, error) {
	opened, err := directory.Directory.OpenDir(name)
	if err != nil {
		return nil, err
	}
	label := name
	if directory.label == "asset" && name == pendingName {
		label = "stage"
	}
	return &testDirectory{Directory: opened, files: directory.files, label: label}, nil
}

func (directory *testDirectory) Mkdir(name string, mode os.FileMode) error {
	if err := directory.files.record("mkdir:" + directory.label + "/" + name); err != nil {
		return err
	}
	return directory.Directory.Mkdir(name, mode)
}

func (directory *testDirectory) Chmod(mode os.FileMode) error {
	if err := directory.files.record("chmod:" + directory.label); err != nil {
		return err
	}
	return directory.Directory.Chmod(mode)
}

func (directory *testDirectory) OpenFile(name string, flags int, mode os.FileMode) (geodatainstall.File, error) {
	event := "open:" + directory.label + "/" + name
	if err := directory.files.record(event); err != nil {
		return nil, err
	}
	file, err := directory.Directory.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &testFile{File: file, files: directory.files, label: directory.label + "/" + name}, nil
}

func (directory *testDirectory) Rename(oldName, newName string) error {
	if err := directory.files.record("rename:" + directory.label + "/" + newName); err != nil {
		return err
	}
	return directory.Directory.Rename(oldName, newName)
}

func (directory *testDirectory) Remove(name string) error {
	if err := directory.files.record("remove:" + directory.label + "/" + name); err != nil {
		return err
	}
	return directory.Directory.Remove(name)
}

func (directory *testDirectory) RemoveDir(name string) error {
	if err := directory.files.record("removedir:" + directory.label + "/" + name); err != nil {
		return err
	}
	return directory.Directory.RemoveDir(name)
}

func (directory *testDirectory) Sync() error {
	if err := directory.files.record("sync:" + directory.label); err != nil {
		return err
	}
	return directory.Directory.Sync()
}

type testFile struct {
	geodatainstall.File
	files *testFileSystem
	label string
}

func (file *testFile) Sync() error {
	if err := file.files.record("syncfile:" + file.label); err != nil {
		return err
	}
	return file.File.Sync()
}

func (file *testFile) Chmod(mode os.FileMode) error {
	if err := file.files.record("chmodfile:" + file.label); err != nil {
		return err
	}
	return file.File.Chmod(mode)
}

type testFixture struct {
	root      string
	assetRoot string
	store     *domainstore.Store
	service   *Service
	runner    *fakeRunner
	validator *fakeValidator
	files     *testFileSystem
}

type finalizationRecordingStore struct {
	Store
	files *testFileSystem
}

func (store finalizationRecordingStore) FinalizeConsolidation(ctx context.Context, prepared []string) error {
	if err := store.Store.FinalizeConsolidation(ctx, prepared); err != nil {
		return err
	}
	_ = store.files.record("db:finalized")
	return nil
}

type finalizationFailStore struct {
	Store
}

func (store finalizationFailStore) FinalizeConsolidation(context.Context, []string) error {
	return errors.New("injected domain-store commit failure")
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	return newFixtureWithGeositeName(t, "")
}

func newFixtureWithGeositeName(t *testing.T, geositeName string) *testFixture {
	t.Helper()
	root := t.TempDir()
	assetRoot := filepath.Join(root, "assets")
	if err := os.Mkdir(assetRoot, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := domainstore.Open(context.Background(), filepath.Join(root, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidateName := geositeName
	if candidateName == "" {
		candidateName = defaultTestGeositeName
	}
	runner := &fakeRunner{root: assetRoot, candidateName: candidateName}
	validator := &fakeValidator{root: assetRoot, candidateName: candidateName}
	files := newTestFileSystem(assetRoot)
	service, err := New(store, testAssetPath, "/usr/local/bin/domain-list-community", validator, Options{
		Files: files, Runner: runner, GeositeName: geositeName,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testFixture{
		root: root, assetRoot: assetRoot, store: store, service: service,
		runner: runner, validator: validator, files: files,
	}
}

func (fixture *testFixture) addDomains(t *testing.T) {
	t.Helper()
	if err := fixture.store.AddDesired(context.Background(), "Z.example."); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.AddDesired(context.Background(), "a.example"); err != nil {
		t.Fatal(err)
	}
}

func (fixture *testFixture) assertFrozen(t *testing.T, want bool) {
	t.Helper()
	snapshot, err := fixture.store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConsolidationActive != want {
		t.Fatalf("consolidation active = %t; want %t", snapshot.ConsolidationActive, want)
	}
}

func TestInspectClassifiesCustomEvidenceReadOnly(t *testing.T) {
	for _, test := range []struct {
		name                string
		setup               func(*testing.T, *testFixture)
		phase               InspectionPhase
		live                LiveGeositePhase
		prepared, finalized bool
	}{
		{name: "clean", setup: func(*testing.T, *testFixture) {}, phase: InspectionClean},
		{name: "prepared candidate live", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			fixture.addDomains(t)
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionManifest, live: LiveGeositeCandidate, prepared: true, finalized: true},
		{name: "external live drift", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), []byte("external"), 0644); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionManifest, live: LiveGeositeDrift, prepared: true, finalized: true},
		{name: "duplicate-key manifest", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.assetRoot, pendingName, manifestName)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`{`), []byte(`{"version":2,`), 1)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionInvalid},
		{name: "missing required backup", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), []byte("prior"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(fixture.assetRoot, pendingName, defaultTestBackupName)); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionManifest, live: LiveGeositeCandidate, finalized: true},
		{name: "extra symlink entry", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(fixture.root, "outside"), filepath.Join(fixture.assetRoot, pendingName, "extra")); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionInvalid},
		{name: "partial preparation", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			stage := filepath.Join(fixture.assetRoot, pendingName)
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, inputName), []byte("partial input"), 0600); err != nil {
				t.Fatal(err)
			}
		}, phase: InspectionPartial},
		{name: "finalized cleanup after DB commit", setup: func(t *testing.T, fixture *testFixture) {
			t.Helper()
			fixture.addDomains(t)
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Fail after staged artifacts are removed, while the manifest remains.
			// Directory iteration order is filesystem-dependent; failing a named
			// removal may leave either complete or partial preparation evidence.
			fixture.files.failOn("sync:stage", fixture.files.count("sync:stage")+1)
			var committed *FinalizationCommittedError
			if err := fixture.service.Finalize(context.Background()); !errors.As(err, &committed) {
				t.Fatalf("Finalize() = %v; want committed cleanup failure", err)
			}
		}, phase: InspectionManifest, live: LiveGeositeCandidate, finalized: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			test.setup(t, fixture)
			eventsBefore := mutatingEventCount(fixture.files.eventsCopy())
			first, err := fixture.service.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			second, err := fixture.service.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if first.Phase != test.phase || first.Phase != second.Phase ||
				first.Live != test.live || first.Live != second.Live ||
				first.PreparedEvidence != test.prepared || first.FinalizedEvidence != test.finalized ||
				mutatingEventCount(fixture.files.eventsCopy()) != eventsBefore {
				t.Fatalf("Inspect() = %#v then %#v; want phase=%v live=%v prepared=%t finalized=%t and no writes",
					first, second, test.phase, test.live, test.prepared, test.finalized)
			}
		})
	}
}

func mutatingEventCount(events []string) int {
	count := 0
	for _, event := range events {
		if strings.HasPrefix(event, "mkdir:") || strings.HasPrefix(event, "chmod:") ||
			strings.HasPrefix(event, "chmodfile:") || strings.HasPrefix(event, "sync:") ||
			strings.HasPrefix(event, "syncfile:") || strings.HasPrefix(event, "remove:") ||
			strings.HasPrefix(event, "removedir:") || strings.HasPrefix(event, "rename:") {
			count++
		}
	}
	return count
}

func TestRecoverIncompletePrepareClearsOnlyPreManifestEvidence(t *testing.T) {
	t.Run("freeze before directory creation", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := fixture.store.BeginConsolidation(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.RecoverIncompletePrepareWhileLocked(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.assertFrozen(t, false)
	})
	t.Run("partial private stage", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := fixture.store.BeginConsolidation(context.Background()); err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(fixture.assetRoot, pendingName)
		if err := os.Mkdir(stage, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, inputName), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.RecoverIncompletePrepareWhileLocked(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.assertFrozen(t, false)
		if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial staging directory remains: %v", err)
		}
	})
	t.Run("durable manifest is never treated as partial", func(t *testing.T) {
		fixture := newFixture(t)
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(fixture.assetRoot, pendingName, manifestName)
		if err := fixture.service.RecoverIncompletePrepareWhileLocked(context.Background()); !errors.Is(err, ErrPending) {
			t.Fatalf("manifest-bearing recovery = %v; want refusal", err)
		}
		if _, err := os.Stat(manifestPath); err != nil {
			t.Fatalf("manifest evidence was removed: %v", err)
		}
		fixture.assertFrozen(t, true)
	})
}

func TestFinalizeCommitsBaselineBeforeCleaningEvidence(t *testing.T) {
	fixture := newFixture(t)
	fixture.addDomains(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.service.store = finalizationRecordingStore{Store: fixture.store, files: fixture.files}

	if err := fixture.service.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	snapshot, err := fixture.store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := domainstore.Snapshot{
		Desired: []string{"a.example", "z.example"}, Baseline: []string{"a.example", "z.example"},
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() after finalization = %#v; want %#v", snapshot, want)
	}
	if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending evidence remains after finalization: %v", err)
	}
	events := fixture.files.eventsCopy()
	liveHash := eventIndex(events, "open:asset/"+defaultTestCandidateName)
	commit := eventIndex(events, "db:finalized")
	cleanup := eventIndex(events, "remove:stage/"+inputName)
	if liveHash < 0 || commit <= liveHash || cleanup <= commit {
		t.Fatalf("finalization order must be live hash, DB commit, cleanup: %v", events)
	}
	if err := fixture.service.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() after complete cleanup = %v", err)
	}
}

func TestFinalizeRejectsStaleDesiredSetAndRetainsFreezeAndEvidence(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.AddDesired(context.Background(), "prepared.example"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(fixture.root, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES ('later.example', 1, 0)"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Finalize(context.Background()); !errors.Is(err, domainstore.ErrPreparedDomainsMismatch) {
		t.Fatalf("Finalize() error = %v; want prepared-domain mismatch", err)
	}
	fixture.assertFrozen(t, true)
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingName, manifestName)); err != nil {
		t.Fatalf("pending manifest not retained after mismatch: %v", err)
	}
}

func TestFinalizeRejectsLiveDriftAndRetainsFreezeAndEvidence(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), []byte("external geosite"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Finalize(context.Background()); !errors.Is(err, ErrExternalDrift) {
		t.Fatalf("Finalize() error = %v; want external drift refusal", err)
	}
	fixture.assertFrozen(t, true)
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingName, manifestName)); err != nil {
		t.Fatalf("pending manifest not retained after live drift: %v", err)
	}
}

func TestVerifyWhileLockedRejectsLiveCustomGeositeDrift(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), []byte("external drift"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.VerifyWhileLocked(context.Background()); !errors.Is(err, ErrExternalDrift) {
		t.Fatalf("VerifyWhileLocked() error = %v; want external drift", err)
	}
	fixture.assertFrozen(t, true)
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingName, manifestName)); err != nil {
		t.Fatalf("verification changed recovery evidence: %v", err)
	}
}

func TestFinalizeRejectsMissingPendingEvidenceWhileFrozen(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(fixture.assetRoot, pendingName)
	preserved := filepath.Join(fixture.root, "preserved-pending")
	if err := os.Rename(pending, preserved); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Finalize(context.Background()); !errors.Is(err, ErrPending) {
		t.Fatalf("Finalize() error = %v; want pending-evidence refusal", err)
	}
	fixture.assertFrozen(t, true)
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("pending evidence was not retained: %v", err)
	}
}

func TestFinalizeDatabaseFailureRetainsFreezeAndEvidence(t *testing.T) {
	fixture := newFixture(t)
	fixture.addDomains(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.service.store = finalizationFailStore{Store: fixture.store}
	if err := fixture.service.Finalize(context.Background()); err == nil {
		t.Fatal("Finalize() succeeded despite database failure")
	}
	fixture.assertFrozen(t, true)
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingName, manifestName)); err != nil {
		t.Fatalf("pending manifest not retained after database failure: %v", err)
	}
}

func TestFinalizeCleanupFailureIsRetryableWithoutRefreezing(t *testing.T) {
	fixture := newFixture(t)
	fixture.addDomains(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.files.failOn("sync:stage", fixture.files.count("sync:stage")+1)
	err := fixture.service.Finalize(context.Background())
	var committedErr *FinalizationCommittedError
	if !errors.Is(err, ErrFilesystem) || !errors.As(err, &committedErr) {
		t.Fatalf("Finalize() error = %v; want typed post-commit cleanup failure", err)
	}
	snapshot, err := fixture.store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := domainstore.Snapshot{
		Desired: []string{"a.example", "z.example"}, Baseline: []string{"a.example", "z.example"},
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot() after cleanup failure = %#v; want committed baseline %#v", snapshot, want)
	}
	if err := fixture.service.Finalize(context.Background()); err != nil {
		t.Fatalf("retry Finalize() = %v", err)
	}
	fixture.assertFrozen(t, false)
	if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending evidence remains after cleanup retry: %v", err)
	}
	if err := fixture.service.Finalize(context.Background()); err != nil {
		t.Fatalf("second idempotent Finalize() = %v", err)
	}
}

func TestPrepareBuildsSortedPrivateInputAndLeavesDurablePendingState(t *testing.T) {
	fixture := newFixture(t)
	fixture.addDomains(t)
	old := []byte("old custom bytes\x00exact\n")
	if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), old, 0640); err != nil {
		t.Fatal(err)
	}
	fixture.validator.onValidate = func(path string) error {
		snapshot, err := fixture.store.Snapshot(context.Background())
		if err != nil || !snapshot.ConsolidationActive ||
			!reflect.DeepEqual(snapshot.Desired, []string{"a.example", "z.example"}) {
			t.Errorf("readable snapshot during validation = %#v, %v", snapshot, err)
		}
		if err := fixture.store.AddDesired(context.Background(), "blocked.example"); !errors.Is(err, domain.ErrConsolidationActive) {
			t.Errorf("AddDesired() during prepare = %v; want conflict", err)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("candidate passed to validator = %v, %v", info, err)
		}
		return nil
	}

	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	fixture.assertFrozen(t, true)
	if fixture.runner.binary != "/usr/local/bin/domain-list-community" ||
		!reflect.DeepEqual(fixture.runner.args, []string{
			"-datapath", filepath.Join(testAssetPath, pendingName),
			"-outputname", defaultTestCandidateName,
			"-outputdir", filepath.Join(testAssetPath, pendingName),
		}) {
		t.Fatalf("builder command = %q %#v", fixture.runner.binary, fixture.runner.args)
	}
	if fixture.runner.input != "domain:a.example\ndomain:z.example\n" {
		t.Fatalf("builder input = %q; want normalized sorted entries", fixture.runner.input)
	}
	sourcePath := filepath.Join(fixture.assetRoot, pendingName, "userlist")
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil || !sourceInfo.Mode().IsRegular() {
		t.Fatalf("builder category source `userlist` = %v, %v", sourceInfo, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName, "domains.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected generic builder input file domains.txt: %v", err)
	}
	installed, err := os.ReadFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName))
	if err != nil || !bytes.Equal(installed, []byte("custom geosite candidate\x00\n")) {
		t.Fatalf("installed candidate = %q, %v", installed, err)
	}
	manifest, err := fixture.service.readManifest(mustOpenStage(t, fixture.assetRoot))
	if err != nil {
		t.Fatalf("read pending manifest = %v", err)
	}
	if manifest.GeositeName != defaultTestGeositeName ||
		!reflect.DeepEqual(manifest.Domains, []string{"a.example", "z.example"}) ||
		manifest.PreviousHash != hashBytes(old) || !manifest.PreviousExists {
		t.Fatalf("pending manifest = %#v", manifest)
	}
	backup, err := os.ReadFile(filepath.Join(fixture.assetRoot, pendingName, defaultTestBackupName))
	if err != nil || !bytes.Equal(backup, old) {
		t.Fatalf("previous-file backup = %q, %v", backup, err)
	}
	if err := fixture.service.Prepare(context.Background()); !errors.Is(err, domain.ErrConsolidationActive) {
		t.Fatalf("overlapping Prepare() = %v; want active conflict", err)
	}
}

func TestManifestBackupAndCandidatesAreDurableBeforeLiveRename(t *testing.T) {
	fixture := newFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), []byte("previous geosite"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := fixture.files.eventsCopy()
	backupSync := eventIndex(events, "syncfile:stage/"+defaultTestBackupName)
	manifestSync := eventIndex(events, "syncfile:stage/"+manifestCandidate)
	manifestRename := eventIndex(events, "rename:stage/"+manifestName)
	pendingSync := lastEventIndex(events, "sync:stage")
	liveFileSync := eventIndex(events, "syncfile:asset/"+defaultTestLiveCandidateName)
	liveRename := eventIndex(events, "rename:asset/"+defaultTestCandidateName)
	if backupSync < 0 || manifestSync <= backupSync || manifestRename <= manifestSync ||
		pendingSync <= manifestRename || liveFileSync <= pendingSync || liveRename <= liveFileSync {
		t.Fatalf("durability/install sequence invalid: %v", events)
	}
}

func TestPrepareUsesReservedSentinelForEmptyDesiredSet(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.runner.input != "domain:"+emptyDomain+"\n" {
		t.Fatalf("empty-list builder input = %q", fixture.runner.input)
	}
}

func TestPrepareFailureBeforeReplacementUnfreezesAndCleansStaging(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testFixture)
		wantErr error
	}{
		{name: "builder failure", setup: func(f *testFixture) { f.runner.err = errors.New("private builder output") }, wantErr: ErrBuilder},
		{name: "missing output", setup: func(f *testFixture) { f.runner.mode = "no-output" }, wantErr: ErrBuilderOutput},
		{name: "symlink output", setup: func(f *testFixture) { f.runner.mode = "symlink" }, wantErr: ErrBuilderOutput},
		{name: "empty output", setup: func(f *testFixture) { f.runner.mode = "empty" }, wantErr: ErrBuilderOutput},
		{name: "candidate validation", setup: func(f *testFixture) { f.validator.err = errors.New("private Xray diagnostics") }, wantErr: ErrValidation},
		{name: "stage creation", setup: func(f *testFixture) { f.files.failOn("mkdir:asset/"+pendingName, 1) }, wantErr: ErrFilesystem},
		{name: "pending parent sync", setup: func(f *testFixture) { f.files.failOn("sync:asset", 1) }, wantErr: ErrFilesystem},
		{name: "pending directory permissions", setup: func(f *testFixture) { f.files.failOn("chmod:stage", 1) }, wantErr: ErrFilesystem},
		{name: "input open", setup: func(f *testFixture) { f.files.failOn("open:stage/"+inputName, 1) }, wantErr: ErrFilesystem},
		{name: "input permissions", setup: func(f *testFixture) { f.files.failOn("chmodfile:stage/"+inputName, 1) }, wantErr: ErrFilesystem},
		{name: "input sync", setup: func(f *testFixture) { f.files.failOn("syncfile:stage/"+inputName, 1) }, wantErr: ErrFilesystem},
		{name: "manifest file sync", setup: func(f *testFixture) { f.files.failOn("syncfile:stage/"+manifestCandidate, 1) }, wantErr: ErrFilesystem},
		{name: "manifest rename", setup: func(f *testFixture) { f.files.failOn("rename:stage/"+manifestName, 1) }, wantErr: ErrFilesystem},
		{name: "manifest directory sync", setup: func(f *testFixture) { f.files.failOn("sync:stage", 2) }, wantErr: ErrFilesystem},
		{name: "live candidate sync", setup: func(f *testFixture) { f.files.failOn("sync:asset", 2) }, wantErr: ErrFilesystem},
		{name: "live rename", setup: func(f *testFixture) { f.files.failOn("rename:asset/"+defaultTestCandidateName, 1) }, wantErr: ErrFilesystem},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			fixture.addDomains(t)
			test.setup(fixture)
			if err := fixture.service.Prepare(context.Background()); !errors.Is(err, test.wantErr) {
				t.Fatalf("Prepare() error = %v; want %v", err, test.wantErr)
			}
			fixture.assertFrozen(t, false)
			if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("staging directory remains after failed prepare recovery: %v", err)
			}
		})
	}
}

func TestPreparePreviousBackupAndManifestFailuresUnfreezeWithoutChangingLiveFile(t *testing.T) {
	tests := []struct {
		name  string
		event string
		at    int
	}{
		{name: "backup open", event: "open:stage/" + defaultTestBackupName, at: 1},
		{name: "backup sync", event: "syncfile:stage/" + defaultTestBackupName, at: 1},
		{name: "backup directory sync", event: "sync:stage", at: 2},
		{name: "manifest open", event: "open:stage/" + manifestCandidate, at: 1},
		{name: "manifest permissions", event: "chmodfile:stage/" + manifestCandidate, at: 1},
		{name: "manifest sync", event: "syncfile:stage/" + manifestCandidate, at: 1},
		{name: "manifest directory sync", event: "sync:stage", at: 3},
		{name: "live candidate open", event: "open:asset/" + defaultTestLiveCandidateName, at: 1},
		{name: "live candidate permissions", event: "chmodfile:asset/" + defaultTestLiveCandidateName, at: 1},
		{name: "live candidate file sync", event: "syncfile:asset/" + defaultTestLiveCandidateName, at: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			previous := []byte("prior custom data")
			path := filepath.Join(fixture.assetRoot, defaultTestCandidateName)
			if err := os.WriteFile(path, previous, 0600); err != nil {
				t.Fatal(err)
			}
			fixture.files.failOn(test.event, test.at)
			if err := fixture.service.Prepare(context.Background()); err == nil {
				t.Fatalf("Prepare() unexpectedly succeeded; events=%v hits=%d",
					fixture.files.eventsCopy(), fixture.files.count(test.event))
			}
			fixture.assertFrozen(t, false)
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, previous) {
				t.Fatalf("live previous file = %q, %v", got, err)
			}
			if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending area remains after fully cleaned failure: %v", err)
			}
		})
	}
}

func TestPrepareFailureAfterLiveRenameRollsBackPreviousOrAbsence(t *testing.T) {
	for _, withPrevious := range []bool{true, false} {
		name := "initially absent"
		if withPrevious {
			name = "previous file"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t)
			old := []byte("previous custom geosite\n")
			path := filepath.Join(fixture.assetRoot, defaultTestCandidateName)
			if withPrevious {
				if err := os.WriteFile(path, old, 0640); err != nil {
					t.Fatal(err)
				}
			}
			fixture.files.failOn("sync:asset", 3)
			if err := fixture.service.Prepare(context.Background()); !errors.Is(err, ErrFilesystem) {
				t.Fatalf("Prepare() error = %v; want post-rename directory sync failure", err)
			}
			fixture.assertFrozen(t, false)
			if withPrevious {
				got, err := os.ReadFile(path)
				info, statErr := os.Stat(path)
				if err != nil || statErr != nil || !bytes.Equal(got, old) || info.Mode().Perm() != 0640 {
					t.Fatalf("restored asset = %q, %v / %v; want exact bytes/mode", got, err, statErr)
				}
			} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("asset exists after absent-file rollback: %v", err)
			}
			fixture.assertFrozen(t, false)
			if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending staging remains after failed prepare recovery: %v", err)
			}
			if err := fixture.service.Rollback(context.Background()); err != nil {
				t.Fatalf("idempotent second Rollback() = %v", err)
			}
		})
	}
}

func TestRollbackRefusesUnknownExternalDriftAndCanRetryAfterRestoreFailure(t *testing.T) {
	t.Run("external drift", func(t *testing.T) {
		for _, withPrevious := range []bool{false, true} {
			fixture := newFixture(t)
			path := filepath.Join(fixture.assetRoot, defaultTestCandidateName)
			if withPrevious {
				if err := os.WriteFile(path, []byte("old geosite"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.service.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("unrecognized external replacement"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.Rollback(context.Background()); !errors.Is(err, ErrExternalDrift) {
				t.Fatalf("Rollback() error = %v; want external drift refusal (previous=%t)", err, withPrevious)
			}
			fixture.assertFrozen(t, true)
		}
	})

	t.Run("restore failure and retry", func(t *testing.T) {
		fixture := newFixture(t)
		old := []byte("old custom geosite")
		path := filepath.Join(fixture.assetRoot, defaultTestCandidateName)
		if err := os.WriteFile(path, old, 0640); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.files.failOn("rename:asset/"+defaultTestCandidateName, 2)
		if err := fixture.service.Rollback(context.Background()); !errors.Is(err, ErrRollback) {
			t.Fatalf("Rollback() error = %v; want injected restore failure", err)
		}
		fixture.assertFrozen(t, true)
		if err := fixture.service.Rollback(context.Background()); err != nil {
			t.Fatalf("retry Rollback() = %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, old) {
			t.Fatalf("restored bytes = %q, %v", got, err)
		}
		fixture.assertFrozen(t, false)
	})

	t.Run("rollback directory sync failure and retry", func(t *testing.T) {
		fixture := newFixture(t)
		old := []byte("old asset requiring exact restore")
		path := filepath.Join(fixture.assetRoot, defaultTestCandidateName)
		if err := os.WriteFile(path, old, 0600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.files.failOn("sync:asset", fixture.files.count("sync:asset")+1)
		if err := fixture.service.Rollback(context.Background()); !errors.Is(err, ErrRollback) {
			t.Fatalf("Rollback() error = %v; want restore fsync failure", err)
		}
		fixture.assertFrozen(t, true)
		if err := fixture.service.Rollback(context.Background()); err != nil {
			t.Fatalf("retry Rollback() = %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, old) {
			t.Fatalf("restored bytes = %q, %v", got, err)
		}
		fixture.assertFrozen(t, false)
	})

	t.Run("retry syncs an already restored file before unfreezing", func(t *testing.T) {
		fixture := newFixture(t)
		old := []byte("durable old geosite")
		path := filepath.Join(fixture.assetRoot, defaultTestCandidateName)
		if err := os.WriteFile(path, old, 0600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.files.failOn("sync:asset", fixture.files.count("sync:asset")+3)
		if err := fixture.service.Rollback(context.Background()); !errors.Is(err, ErrRollback) {
			t.Fatalf("Rollback() error = %v; want post-restore fsync failure", err)
		}
		fixture.assertFrozen(t, true)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, old) {
			t.Fatalf("asset after failed directory fsync = %q, %v", got, err)
		}
		if err := fixture.service.Rollback(context.Background()); err != nil {
			t.Fatalf("retry Rollback() = %v", err)
		}
		fixture.assertFrozen(t, false)
	})

	t.Run("staging cleanup follows durable unfreeze and is retryable", func(t *testing.T) {
		fixture := newFixture(t)
		old := []byte("old asset")
		if err := os.WriteFile(filepath.Join(fixture.assetRoot, defaultTestCandidateName), old, 0600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.files.failOn("sync:stage", fixture.files.count("sync:stage")+1)
		if err := fixture.service.Rollback(context.Background()); !errors.Is(err, ErrFilesystem) {
			t.Fatalf("Rollback() error = %v; want staging cleanup fsync failure; events=%v hits=%d",
				err, fixture.files.eventsCopy(), fixture.files.count("sync:stage"))
		}
		fixture.assertFrozen(t, false)
		if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingName)); err != nil {
			t.Fatalf("pending evidence removed before cleanup was durable: %v", err)
		}
		if err := fixture.service.Rollback(context.Background()); err != nil {
			entries, _ := os.ReadDir(filepath.Join(fixture.assetRoot, pendingName))
			t.Fatalf("retry cleanup rollback = %v; entries=%v events=%v", err, entries, fixture.files.eventsCopy())
		}
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending area remains after retry: %v", err)
		}
	})
}

func TestPrepareRejectsReservedSentinelDomainAndBuilderOutputBoundaries(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.store.AddDesired(context.Background(), emptyDomain); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Prepare(context.Background()); !errors.Is(err, ErrReservedDomain) {
		t.Fatalf("Prepare() with reserved sentinel = %v", err)
	}
	fixture.assertFrozen(t, false)
	if fixture.runner.calls != 0 || fixture.validator.calls != 0 {
		t.Fatalf("builder/validator invoked for reserved input: %d/%d", fixture.runner.calls, fixture.validator.calls)
	}
}

func mustOpenStage(t *testing.T, root string) geodatainstall.Directory {
	t.Helper()
	asset, err := geodatainstall.OpenDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := asset.OpenDir(pendingName)
	_ = asset.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stage.Close() })
	return stage
}

func eventIndex(events []string, event string) int {
	for index, value := range events {
		if value == event {
			return index
		}
	}
	return -1
}

func lastEventIndex(events []string, event string) int {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index] == event {
			return index
		}
	}
	return -1
}

func TestGeositeNameDefaultsAndValidation(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		fixture := newFixture(t)
		if fixture.service.candidateName != defaultTestCandidateName ||
			fixture.service.backupName != defaultTestBackupName ||
			fixture.service.liveCandidateName != defaultTestLiveCandidateName {
			t.Fatalf("default geosite names = %q/%q/%q",
				fixture.service.candidateName,
				fixture.service.backupName, fixture.service.liveCandidateName)
		}
		recovery, err := NewRecoveryService(fixture.store, testAssetPath, "", "", fixture.files)
		if err != nil {
			t.Fatal(err)
		}
		if recovery.candidateName != defaultTestCandidateName {
			t.Fatalf("recovery default geosite name = %q", recovery.candidateName)
		}
	})

	t.Run("unsafe filenames", func(t *testing.T) {
		fixture := newFixture(t)
		for _, name := range []string{
			"geosite_.dat",
			"geosite_x.json",
			"geosite_x",
			"geosite_x.DAT",
			"../geosite_x.dat",
			"geosite_x/evil.dat",
			"geosite_-x.dat",
			"geosite_" + strings.Repeat("a", 49) + ".dat",
		} {
			if _, err := New(fixture.store, testAssetPath, "/usr/local/bin/domain-list-community",
				fixture.validator, Options{GeositeName: name}); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("New(GeositeName=%q) error = %v; want ErrInvalidInput", name, err)
			}
			if _, err := NewRecoveryService(fixture.store, testAssetPath, name, "", fixture.files); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("NewRecoveryService(geositeName=%q) error = %v; want ErrInvalidInput", name, err)
			}
		}
	})
}

func TestCustomGeositeNamePrepareRollbackAndFinalize(t *testing.T) {
	const (
		customName      = "geosite_custom-list.dat"
		customCandidate = customName
		customBackup    = customName + ".previous"
		customLive      = "." + customName + ".xch-candidate"
	)

	t.Run("rollback", func(t *testing.T) {
		fixture := newFixtureWithGeositeName(t, customName)
		fixture.addDomains(t)
		old := []byte("previous custom geosite\n")
		if err := os.WriteFile(filepath.Join(fixture.assetRoot, customCandidate), old, 0640); err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatalf("Prepare() error = %v", err)
		}
		if !reflect.DeepEqual(fixture.runner.args, []string{
			"-datapath", filepath.Join(testAssetPath, pendingName),
			"-outputname", customCandidate,
			"-outputdir", filepath.Join(testAssetPath, pendingName),
		}) {
			t.Fatalf("custom builder command = %#v", fixture.runner.args)
		}
		manifest, err := fixture.service.readManifest(mustOpenStage(t, fixture.assetRoot))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.GeositeName != customName {
			t.Fatalf("manifest geositeName = %q; want %q", manifest.GeositeName, customName)
		}
		if _, err := os.Stat(filepath.Join(fixture.assetRoot, pendingName, customBackup)); err != nil {
			t.Fatalf("custom backup = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, defaultTestCandidateName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("default geosite created for custom name: %v", err)
		}
		if err := fixture.service.Rollback(context.Background()); err != nil {
			t.Fatalf("Rollback() error = %v", err)
		}
		restored, err := os.ReadFile(filepath.Join(fixture.assetRoot, customCandidate))
		if err != nil || !bytes.Equal(restored, old) {
			t.Fatalf("restored custom geosite = %q, %v", restored, err)
		}
		fixture.assertFrozen(t, false)
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("custom pending evidence remains after rollback: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, customLive)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("custom live candidate remains after rollback: %v", err)
		}
	})

	t.Run("finalize", func(t *testing.T) {
		fixture := newFixtureWithGeositeName(t, customName)
		fixture.addDomains(t)
		if err := fixture.service.Prepare(context.Background()); err != nil {
			t.Fatalf("Prepare() error = %v", err)
		}
		if err := fixture.service.Finalize(context.Background()); err != nil {
			t.Fatalf("Finalize() error = %v", err)
		}
		snapshot, err := fixture.store.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := domainstore.Snapshot{
			Desired: []string{"a.example", "z.example"}, Baseline: []string{"a.example", "z.example"},
		}
		if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("Snapshot() after custom finalization = %#v; want %#v", snapshot, want)
		}
		if _, err := os.Stat(filepath.Join(fixture.assetRoot, customCandidate)); err != nil {
			t.Fatalf("custom live geosite = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, defaultTestCandidateName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("default geosite created for custom name: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.assetRoot, pendingName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("custom pending evidence remains after finalization: %v", err)
		}
	})
}

func TestDifferentGeositeNameRejectsPendingEvidenceWithoutWrites(t *testing.T) {
	const (
		preparedName = "geosite_prepared.dat"
		otherName    = "geosite_other.dat"
	)
	fixture := newFixtureWithGeositeName(t, preparedName)
	fixture.addDomains(t)
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	manifestPath := filepath.Join(fixture.assetRoot, pendingName, manifestName)
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(fixture.assetRoot, preparedName)
	liveBefore, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	otherFiles := newTestFileSystem(fixture.assetRoot)
	other, err := NewRecoveryService(fixture.store, testAssetPath, otherName, "", otherFiles)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := other.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Phase != InspectionInvalid {
		t.Fatalf("Inspect() with different service name = %#v; want invalid evidence", inspection)
	}
	if _, err := other.readManifestMetadata(mustOpenStage(t, fixture.assetRoot)); !errors.Is(err, ErrManifest) {
		t.Fatalf("readManifestMetadata() with different service name = %v; want ErrManifest", err)
	}
	if err := other.VerifyWhileLocked(context.Background()); !errors.Is(err, ErrManifest) {
		t.Fatalf("VerifyWhileLocked() error = %v; want ErrManifest", err)
	}
	if err := other.RollbackWhileLocked(context.Background()); !errors.Is(err, ErrManifest) {
		t.Fatalf("RollbackWhileLocked() error = %v; want ErrManifest", err)
	}
	if err := other.FinalizeWhileLocked(context.Background()); !errors.Is(err, ErrManifest) {
		t.Fatalf("FinalizeWhileLocked() error = %v; want ErrManifest", err)
	}
	if count := mutatingEventCount(otherFiles.eventsCopy()); count != 0 {
		t.Fatalf("different-name service performed %d mutating operations: %v", count, otherFiles.eventsCopy())
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil || !bytes.Equal(manifestAfter, manifestBefore) {
		t.Fatalf("pending manifest changed = %q, %v", manifestAfter, err)
	}
	liveAfter, err := os.ReadFile(livePath)
	if err != nil || !bytes.Equal(liveAfter, liveBefore) {
		t.Fatalf("live geosite changed = %q, %v", liveAfter, err)
	}
	fixture.assertFrozen(t, true)
}

func TestDomainInputIsDeterministic(t *testing.T) {
	if got := domainInput([]string{"a.example", "z.example"}); got != "domain:a.example\ndomain:z.example\n" {
		t.Fatalf("domainInput() = %q", got)
	}
	if strings.Contains(domainInput(nil), "\n\n") {
		t.Fatal("empty input contains an unintended empty record")
	}
}

func TestDefaultConsolidationLeavesUnmanagedOldFileUntouched(t *testing.T) {
	fixture := newFixture(t)
	fixture.addDomains(t)
	oldPath := filepath.Join(fixture.assetRoot, "geosite_IR.dat")
	oldContent := []byte("unmanaged former geosite")
	if err := os.WriteFile(oldPath, oldContent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(oldPath)
	if err != nil || !bytes.Equal(got, oldContent) {
		t.Fatalf("unmanaged file changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.assetRoot, "geosite_user.dat")); err != nil {
		t.Fatal(err)
	}
}
