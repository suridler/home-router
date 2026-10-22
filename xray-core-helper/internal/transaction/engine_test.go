package transaction

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const transactionFixture = `{
  // preserve transaction fixture comment
  "log": { "loglevel": "warning", "unknownLogOption": true },
  "routing": { "unknownRoutingOption": { "retain": 7 } }
}`

type fakeValidator struct {
	mu    sync.Mutex
	calls []string
	err   error
	fn    func(string) error
}

func (validator *fakeValidator) Validate(_ context.Context, configPath string) error {
	validator.mu.Lock()
	validator.calls = append(validator.calls, configPath)
	err := validator.err
	fn := validator.fn
	validator.mu.Unlock()
	if fn != nil {
		return fn(configPath)
	}
	return err
}

func (validator *fakeValidator) count() int {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	return len(validator.calls)
}

type fakeRestarter struct {
	mu    sync.Mutex
	calls int
	errs  []error
}

func (restarter *fakeRestarter) Restart(context.Context) error {
	restarter.mu.Lock()
	defer restarter.mu.Unlock()
	restarter.calls++
	index := restarter.calls - 1
	if index < len(restarter.errs) {
		return restarter.errs[index]
	}
	return nil
}

func (restarter *fakeRestarter) count() int {
	restarter.mu.Lock()
	defer restarter.mu.Unlock()
	return restarter.calls
}

func TestApplyCommitsNarrowJSONCEditAndKeepsBackup(t *testing.T) {
	validator := &fakeValidator{fn: func(path string) error {
		if filepath.Ext(path) != ".json" {
			return errors.New("candidate does not preserve the configuration extension")
		}
		candidate, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(candidate, []byte(`"loglevel": "debug"`)) {
			return errors.New("candidate does not contain requested update")
		}
		return nil
	}}
	engine, store, configPath := newTestEngine(t, transactionFixture, validator, nil)
	previous, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	previousInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := engine.Apply(context.Background(), 0, loglevelEdit(previous, "debug"), false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != 1 || updated.ContentHash != hashBytes(bytes.Replace(previous, []byte(`"warning"`), []byte(`"debug"`), 1)) {
		t.Fatalf("updated metadata = %#v", updated)
	}
	current, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{
		"// preserve transaction fixture comment", `"unknownLogOption": true`, `"unknownRoutingOption": { "retain": 7 }`,
	} {
		if !bytes.Contains(current, []byte(retained)) {
			t.Errorf("configuration lost %q: %s", retained, current)
		}
	}
	if !bytes.Contains(current, []byte(`"loglevel": "debug"`)) {
		t.Fatalf("configuration was not updated: %s", current)
	}
	currentInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if currentInfo.Mode().Perm() != previousInfo.Mode().Perm() {
		t.Fatalf("mode = %04o; want %04o", currentInfo.Mode().Perm(), previousInfo.Mode().Perm())
	}
	assertSameOwnership(t, previousInfo, currentInfo)
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(configPath), ".config.json.xch-backup-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup files = %#v, error = %v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(backup, previous) {
		t.Fatalf("backup = %q, error = %v; want original bytes", backup, err)
	}
	metadata, err := store.Metadata(context.Background())
	if err != nil || metadata != updated {
		t.Fatalf("stored metadata = %#v, error = %v", metadata, err)
	}
	if validator.count() != 1 {
		t.Fatalf("validator calls = %d; want 1", validator.count())
	}
}

func TestApplyRejectsStaleGenerationAndExternalDriftWithoutWriting(t *testing.T) {
	validator := &fakeValidator{}
	engine, _, configPath := newTestEngine(t, transactionFixture, validator, nil)
	before, _ := os.ReadFile(configPath)
	infoBefore, _ := os.Stat(configPath)
	if _, err := engine.Apply(context.Background(), 4, loglevelEdit(before, "debug"), false); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale Apply() error = %v; want ErrStaleGeneration", err)
	}
	assertUnchangedFile(t, configPath, before, infoBefore)
	if validator.count() != 0 {
		t.Fatalf("stale mutation reached validator %d times", validator.count())
	}

	if err := os.WriteFile(configPath, []byte(strings.Replace(transactionFixture, "retain\": 7", "retain\": 8", 1)), 0640); err != nil {
		t.Fatal(err)
	}
	external, _ := os.ReadFile(configPath)
	infoExternal, _ := os.Stat(configPath)
	if _, err := engine.Apply(context.Background(), 0, loglevelEdit(external, "debug"), false); !errors.Is(err, ErrStateDrift) {
		t.Fatalf("drift Apply() error = %v; want ErrStateDrift", err)
	}
	assertUnchangedFile(t, configPath, external, infoExternal)
	if validator.count() != 0 {
		t.Fatalf("drift mutation reached validator %d times", validator.count())
	}
}

func TestValidationFailureLeavesOriginalBytesAndOwnership(t *testing.T) {
	validator := &fakeValidator{err: errors.New("secret-bearing command output")}
	engine, store, configPath := newTestEngine(t, transactionFixture, validator, nil)
	before, _ := os.ReadFile(configPath)
	infoBefore, _ := os.Stat(configPath)
	_, err := engine.Apply(context.Background(), 0, loglevelEdit(before, "debug"), false)
	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("Apply() error = %v; want ErrValidationFailed", err)
	}
	if code, message, status := ErrorInfo(err); code != "validation_failed" || status != 422 || strings.Contains(message, "secret-bearing") {
		t.Fatalf("public error = %q %q %d", code, message, status)
	}
	assertUnchangedFile(t, configPath, before, infoBefore)
	metadata, stateErr := store.Metadata(context.Background())
	if stateErr != nil || metadata.Generation != 0 || metadata.ContentHash != hashBytes(before) {
		t.Fatalf("state after validation failure = %#v, error = %v", metadata, stateErr)
	}
	if pending, pendingErr := store.Pending(context.Background()); pendingErr != nil || pending != nil {
		t.Fatalf("journal after validation failure = %#v, error = %v", pending, pendingErr)
	}
}

func TestRestartFailureRestoresExactPreviousFileAndRestartsRollback(t *testing.T) {
	validator := &fakeValidator{}
	restarter := &fakeRestarter{errs: []error{errors.New("restart failed")}}
	engine, store, configPath := newTestEngine(t, transactionFixture, validator, restarter)
	before, _ := os.ReadFile(configPath)
	infoBefore, _ := os.Stat(configPath)
	_, err := engine.Apply(context.Background(), 0, loglevelEdit(before, "debug"), true)
	if !errors.Is(err, ErrRestartFailed) {
		t.Fatalf("Apply() error = %v; want ErrRestartFailed", err)
	}
	assertUnchangedFile(t, configPath, before, infoBefore)
	if restarter.count() != 2 {
		t.Fatalf("restart calls = %d; want candidate and rollback restarts", restarter.count())
	}
	metadata, stateErr := store.Metadata(context.Background())
	if stateErr != nil || metadata.Generation != 0 || metadata.ContentHash != hashBytes(before) {
		t.Fatalf("state after restart failure = %#v, error = %v", metadata, stateErr)
	}
	if pending, pendingErr := store.Pending(context.Background()); pendingErr != nil || pending != nil {
		t.Fatalf("journal after restart rollback = %#v, error = %v", pending, pendingErr)
	}
}

func TestCommitFailureRestoresOriginalConfiguration(t *testing.T) {
	engine, store, configPath := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	before, _ := os.ReadFile(configPath)
	infoBefore, _ := os.Stat(configPath)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_config_commit BEFORE UPDATE ON config_state
		BEGIN SELECT RAISE(FAIL, 'injected commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Apply(context.Background(), 0, loglevelEdit(before, "debug"), false)
	if err == nil {
		t.Fatal("Apply() succeeded despite injected SQLite commit failure")
	}
	assertUnchangedFile(t, configPath, before, infoBefore)
	metadata, stateErr := store.Metadata(context.Background())
	if stateErr != nil || metadata.Generation != 0 || metadata.ContentHash != hashBytes(before) {
		t.Fatalf("state after commit failure = %#v, error = %v", metadata, stateErr)
	}
	if pending, pendingErr := store.Pending(context.Background()); pendingErr != nil || pending != nil {
		t.Fatalf("journal after commit failure = %#v, error = %v", pending, pendingErr)
	}
}

func TestMutateWithPreparationRollsBackSnapshotWhenConfigCommitFails(t *testing.T) {
	engine, store, configPath := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	before, _ := os.ReadFile(configPath)
	beforeInfo := mustStat(t, configPath)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_config_commit BEFORE UPDATE ON config_state
		BEGIN SELECT RAISE(FAIL, 'injected commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	preparation := &testMutationPreparation{}
	_, err := engine.MutateWithPreparation(context.Background(), 0,
		func(source []byte) ([]ByteEdit, error) { return loglevelEdit(source, "debug"), nil },
		false,
		func(context.Context) (MutationPreparation, error) { return preparation, nil },
	)
	if err == nil {
		t.Fatal("MutateWithPreparation() succeeded despite injected config commit failure")
	}
	if preparation.commits != 0 || preparation.rollbacks != 1 {
		t.Fatalf("preparation completion = commits:%d rollbacks:%d; want 0,1",
			preparation.commits, preparation.rollbacks)
	}
	assertUnchangedFile(t, configPath, before, beforeInfo)
	metadata, metadataErr := store.Metadata(context.Background())
	if metadataErr != nil || metadata.Generation != 0 || metadata.ContentHash != hashBytes(before) {
		t.Fatalf("metadata after failed commit = %#v, %v", metadata, metadataErr)
	}
}

type testMutationPreparation struct {
	commits   int
	rollbacks int
}

func TestMutateWithPreparationPlansBeforeFilesystemSideEffects(t *testing.T) {
	engine, _, _ := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	planErr := errors.New("reject planned mutation")
	prepareCalls := 0

	_, err := engine.MutateWithPreparation(context.Background(), 0,
		func([]byte) ([]ByteEdit, error) { return nil, planErr },
		false,
		func(context.Context) (MutationPreparation, error) {
			prepareCalls++
			return &testMutationPreparation{}, nil
		},
	)
	if !errors.Is(err, planErr) {
		t.Fatalf("MutateWithPreparation() error = %v; want planner rejection", err)
	}
	if prepareCalls != 0 {
		t.Fatalf("planner rejection invoked preparation %d time(s); want zero", prepareCalls)
	}
}

func (preparation *testMutationPreparation) Commit() error {
	preparation.commits++
	return nil
}

func (preparation *testMutationPreparation) Rollback() error {
	preparation.rollbacks++
	return nil
}

func TestFailedRollbackRemainsRecoverable(t *testing.T) {
	validator := &fakeValidator{}
	restarter := &fakeRestarter{errs: []error{errors.New("candidate restart failed"), errors.New("rollback restart failed")}}
	engine, store, configPath := newTestEngine(t, transactionFixture, validator, restarter)
	before, _ := os.ReadFile(configPath)
	infoBefore, _ := os.Stat(configPath)
	_, err := engine.Apply(context.Background(), 0, loglevelEdit(before, "debug"), true)
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("Apply() error = %v; want ErrRollbackFailed", err)
	}
	assertUnchangedFile(t, configPath, before, infoBefore)
	pending, err := store.Pending(context.Background())
	if err != nil || pending == nil || pending.Phase != "restart_started" {
		t.Fatalf("pending after failed rollback = %#v, error = %v", pending, err)
	}
	if err := engine.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarter.count() != 3 {
		t.Fatalf("restart calls after recovery = %d; want 3", restarter.count())
	}
	if journal, err := store.Pending(context.Background()); err != nil || journal != nil {
		t.Fatalf("journal after recovery = %#v, error = %v", journal, err)
	}
}

func TestAdoptValidatesExternalDriftAndAdvancesGeneration(t *testing.T) {
	validator := &fakeValidator{}
	engine, store, configPath := newTestEngine(t, transactionFixture, validator, nil)
	external := []byte(strings.Replace(transactionFixture, "warning", "info", 1))
	if err := os.WriteFile(configPath, external, 0640); err != nil {
		t.Fatal(err)
	}
	if _, adopted, err := engine.Adopt(context.Background(), 2); adopted || !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale Adopt() = %v, %v; want false, ErrStaleGeneration", adopted, err)
	}
	metadata, adopted, err := engine.Adopt(context.Background(), 0)
	if err != nil || !adopted || metadata.Generation != 1 || metadata.ContentHash != hashBytes(external) {
		t.Fatalf("Adopt() = %#v, %v, %v", metadata, adopted, err)
	}
	if validator.count() != 1 {
		t.Fatalf("validator calls = %d; want 1", validator.count())
	}
	current, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(current, external) {
		t.Fatalf("adopt changed external config bytes: %q, %v", current, err)
	}
	stored, err := store.Metadata(context.Background())
	if err != nil || stored != metadata {
		t.Fatalf("stored adopted state = %#v, error = %v", stored, err)
	}
}

func TestRecoverRollsBackEveryJournalPhase(t *testing.T) {
	tests := []struct {
		name              string
		phase             string
		activeIsCandidate bool
		wantRestarts      int
	}{
		{name: "staged before rename", phase: "staged"},
		{name: "staged after rename", phase: "staged", activeIsCandidate: true},
		{name: "replaced", phase: "replaced", activeIsCandidate: true},
		{name: "restart started after rename", phase: "restart_started", activeIsCandidate: true, wantRestarts: 1},
		{name: "restart started after restore", phase: "restart_started", wantRestarts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, store, configPath := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
			restarter := &fakeRestarter{}
			engine.restarter = restarter
			previous, _ := readSnapshot(configPath)
			previousInfo := mustStat(t, configPath)
			candidate := []byte(strings.Replace(transactionFixture, "warning", "debug", 1))
			id := "recovery-fixture"
			candidatePath := filepath.Join(filepath.Dir(configPath), ".config.json.xch-candidate-"+id)
			backupPath := filepath.Join(filepath.Dir(configPath), ".config.json.xch-backup-"+id)
			if err := writeArtifact(candidatePath, candidate, previous.mode, previous.uid, previous.group); err != nil {
				t.Fatal(err)
			}
			if err := writeArtifact(backupPath, previous.content, previous.mode, previous.uid, previous.group); err != nil {
				t.Fatal(err)
			}
			pending := Pending{
				ID:                 id,
				Phase:              "staged",
				PreviousGeneration: 0,
				PreviousHash:       previous.hash,
				NextHash:           hashBytes(candidate),
				CandidatePath:      candidatePath,
				BackupPath:         backupPath,
				RestartRequired:    test.wantRestarts > 0,
			}
			if err := store.SavePending(context.Background(), pending); err != nil {
				t.Fatal(err)
			}
			if test.phase != "staged" {
				if err := store.Advance(context.Background(), id, "staged", "replaced"); err != nil {
					t.Fatal(err)
				}
			}
			if test.phase == "restart_started" {
				if err := store.Advance(context.Background(), id, "replaced", "restart_started"); err != nil {
					t.Fatal(err)
				}
			}
			if test.activeIsCandidate {
				if err := os.Rename(candidatePath, configPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := engine.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertUnchangedFile(t, configPath, previous.content, previousInfo)
			if restarter.count() != test.wantRestarts {
				t.Fatalf("restart calls = %d; want %d", restarter.count(), test.wantRestarts)
			}
			if journal, err := store.Pending(context.Background()); err != nil || journal != nil {
				t.Fatalf("journal after recovery = %#v, error = %v", journal, err)
			}
			if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backup remains after recovery: %v", err)
			}
			if _, err := os.Stat(candidatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("candidate remains after recovery: %v", err)
			}
		})
	}
}

func TestRecoverFailsClosedOnUnexpectedExternalHash(t *testing.T) {
	engine, store, configPath := newTestEngine(t, transactionFixture, &fakeValidator{}, nil)
	previous, _ := readSnapshot(configPath)
	id := "recovery-drift"
	pending := Pending{
		ID:                 id,
		Phase:              "staged",
		PreviousGeneration: 0,
		PreviousHash:       previous.hash,
		NextHash:           strings.Repeat("0", 64),
		CandidatePath:      filepath.Join(filepath.Dir(configPath), ".config.json.xch-candidate-"+id),
		BackupPath:         filepath.Join(filepath.Dir(configPath), ".config.json.xch-backup-"+id),
	}
	if err := writeArtifact(pending.BackupPath, previous.content, previous.mode, previous.uid, previous.group); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePending(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	external := []byte(`{"external":true}`)
	if err := os.WriteFile(configPath, external, 0640); err != nil {
		t.Fatal(err)
	}
	if err := engine.Recover(context.Background()); !errors.Is(err, ErrStateDrift) {
		t.Fatalf("Recover() error = %v; want ErrStateDrift", err)
	}
	current, _ := os.ReadFile(configPath)
	if !bytes.Equal(current, external) {
		t.Fatalf("recovery overwrote external config: %s", current)
	}
	journal, err := store.Pending(context.Background())
	if err != nil || journal == nil {
		t.Fatalf("journal = %#v, error = %v; want pending recovery", journal, err)
	}
}

func TestConcurrentApplySerializesAndRejectsSecondGeneration(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var active, maximum int
	var mu sync.Mutex
	validator := &fakeValidator{fn: func(string) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}}
	engine, _, configPath := newTestEngine(t, transactionFixture, validator, nil)
	initial, _ := os.ReadFile(configPath)
	firstResult := make(chan error, 1)
	go func() {
		_, err := engine.Apply(context.Background(), 0, loglevelEdit(initial, "debug"), false)
		firstResult <- err
	}()
	<-entered
	secondResult := make(chan error, 1)
	go func() {
		// The second client submits an edit from generation zero, regardless of
		// whether the first client has replaced the file by the time it runs.
		_, err := engine.Apply(context.Background(), 0, loglevelEdit(initial, "info"), false)
		secondResult <- err
	}()
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if err := <-secondResult; !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("second Apply() error = %v; want ErrStaleGeneration", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != 1 {
		t.Fatalf("concurrent validator calls = %d; want 1", maximum)
	}
}

func TestXrayValidatorUsesConfiguredAssetDirectory(t *testing.T) {
	directory := t.TempDir()
	assetDir := filepath.Join(directory, "assets")
	resultPath := filepath.Join(directory, "environment")
	binaryPath := filepath.Join(directory, "xray")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$XRAY_LOCATION_ASSET\" > \"$4.environment\"\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(resultPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	validator, err := NewXrayValidator(binaryPath, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(context.Background(), resultPath); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(resultPath + ".environment")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(content)); got != assetDir {
		t.Fatalf("XRAY_LOCATION_ASSET = %q; want %q", got, assetDir)
	}
}

func TestNewXrayValidatorRejectsUnsafePaths(t *testing.T) {
	for _, test := range []struct {
		name     string
		binary   string
		assetDir string
	}{
		{name: "relative binary", binary: "xray", assetDir: "/usr/local/share/xray"},
		{name: "unclean binary", binary: "/usr/local/../bin/xray", assetDir: "/usr/local/share/xray"},
		{name: "relative asset directory", binary: "/usr/local/bin/xray", assetDir: "assets"},
		{name: "unclean asset directory", binary: "/usr/local/bin/xray", assetDir: "/usr/local/share/../xray"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewXrayValidator(test.binary, test.assetDir); err == nil {
				t.Fatal("NewXrayValidator() error = nil; want path validation error")
			}
		})
	}
}

func newTestEngine(t *testing.T, initial string, validator Validator, restarter Restarter) (*Engine, *Store, string) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(initial), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(context.Background(), filepath.Join(directory, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := NewEngine(configPath, store, validator, restarter)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return engine, store, configPath
}

func loglevelEdit(source []byte, value string) []ByteEdit {
	needle := []byte(`"warning"`)
	start := bytes.Index(source, needle)
	if start < 0 {
		panic("test fixture loglevel not found")
	}
	return []ByteEdit{{Start: start, End: start + len(needle), Replacement: []byte(`"` + value + `"`)}}
}

func assertUnchangedFile(t *testing.T, path string, expected []byte, expectedInfo os.FileInfo) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, expected) {
		t.Fatalf("file content changed: got %q want %q", content, expected)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != expectedInfo.Mode() {
		t.Fatalf("file mode = %v, want %v", info.Mode(), expectedInfo.Mode())
	}
	assertSameOwnership(t, expectedInfo, info)
}

func assertSameOwnership(t *testing.T, expected, actual os.FileInfo) {
	t.Helper()
	expectedOwner, expectedOK := expected.Sys().(*syscall.Stat_t)
	actualOwner, actualOK := actual.Sys().(*syscall.Stat_t)
	if !expectedOK || !actualOK || expectedOwner.Uid != actualOwner.Uid || expectedOwner.Gid != actualOwner.Gid {
		t.Fatalf("ownership changed: expected=%#v actual=%#v", expected.Sys(), actual.Sys())
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
