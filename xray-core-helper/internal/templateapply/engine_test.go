package templateapply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type checkStub struct {
	err   error
	calls *[]string
}

func (stub checkStub) Check(context.Context, templatecatalog.ID, string) error {
	*stub.calls = append(*stub.calls, "check")
	return stub.err
}

type backupStub struct {
	generation uint64
	calls      *[]string
}

func (stub *backupStub) CompletedGeneration(context.Context, templatecatalog.ID) (uint64, error) {
	*stub.calls = append(*stub.calls, "generation")
	return stub.generation, nil
}
func (stub *backupStub) Snapshot(context.Context, templatecatalog.ID, templatebackup.Side, templatebackup.State) (templatebackup.Record, error) {
	*stub.calls = append(*stub.calls, "backup")
	return templatebackup.Record{BackupID: "backup", Generation: stub.generation + 1}, nil
}
func (stub *backupStub) Restore(context.Context, templatecatalog.ID, string, uint64) (templatebackup.Record, error) {
	*stub.calls = append(*stub.calls, "restore")
	return templatebackup.Record{}, nil
}
func (stub *backupStub) RestorePending(context.Context, templatecatalog.ID, string) (templatebackup.Record, error) {
	*stub.calls = append(*stub.calls, "restore-pending")
	return templatebackup.Record{}, nil
}
func (stub *backupStub) SetState(_ context.Context, _ templatecatalog.ID, _ string, state templatebackup.State) (templatebackup.Record, error) {
	*stub.calls = append(*stub.calls, "state:"+string(state))
	if state == templatebackup.StateCompleted {
		stub.generation++
	}
	return templatebackup.Record{BackupID: "backup", Generation: stub.generation}, nil
}

type runtimeStub struct {
	calls                            *[]string
	applyErr, healthErr, rollbackErr error
}

func (stub runtimeStub) Prepare(context.Context, templatecatalog.ID) (RuntimeTransaction, error) {
	*stub.calls = append(*stub.calls, "runtime-prepare")
	return &runtimeTxStub{stub: stub}, nil
}

type runtimeTxStub struct{ stub runtimeStub }

func (tx *runtimeTxStub) Apply(context.Context, string) error {
	*tx.stub.calls = append(*tx.stub.calls, "runtime-apply")
	return tx.stub.applyErr
}
func (tx *runtimeTxStub) Healthy(context.Context) error {
	*tx.stub.calls = append(*tx.stub.calls, "health")
	return tx.stub.healthErr
}
func (tx *runtimeTxStub) Rollback(context.Context, string) error {
	*tx.stub.calls = append(*tx.stub.calls, "runtime-rollback")
	return tx.stub.rollbackErr
}

type replaceStub struct {
	calls                  *[]string
	commitErr, rollbackErr error
}

func (stub replaceStub) Prepare(string, string) (Replacement, error) {
	*stub.calls = append(*stub.calls, "replace-prepare")
	return &replacementStub{stub: stub}, nil
}

type replacementStub struct{ stub replaceStub }

func (*replacementStub) Hash() string { return "validated" }

func (item *replacementStub) Commit() error {
	*item.stub.calls = append(*item.stub.calls, "replace")
	return item.stub.commitErr
}
func (item *replacementStub) Rollback() error {
	*item.stub.calls = append(*item.stub.calls, "file-rollback")
	return item.stub.rollbackErr
}
func (*replacementStub) Close() error { return nil }

func engineTestFixture(t *testing.T, runtime runtimeStub, replacement replaceStub) (*Engine, *[]string) {
	t.Helper()
	calls := []string{}
	runtime.calls = &calls
	replacement.calls = &calls
	engine, err := NewEngine(checkStub{calls: &calls}, &backupStub{generation: 2, calls: &calls}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	engine.replacer = replacement
	engine.hash = func(string) (string, error) { return "validated", nil }
	engine.buildCandidate = func(_ context.Context, entry templatecatalog.Entry, _ bool) (candidate, error) {
		hash, err := engine.hash(entry.WorkingPath)
		return candidate{path: entry.WorkingPath, hash: hash, close: func() {}}, err
	}
	return engine, &calls
}

func TestEngineAppliesInTransactionalOrder(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
	result, err := engine.Apply(context.Background(), templatecatalog.XrayConfig, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Generation != 3 || result.BackupID != "backup" {
		t.Fatalf("result=%#v", result)
	}
	want := []string{"generation", "check", "runtime-prepare", "backup", "replace-prepare", "replace", "runtime-apply", "health", "state:completed"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls=%v want=%v", *calls, want)
	}
}

func TestEngineRollsBackRuntimeFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		runtime runtimeStub
		code    Code
	}{
		{"apply", runtimeStub{applyErr: errors.New("secret command output")}, CodeRuntime},
		{"health", runtimeStub{healthErr: errors.New("secret health output")}, CodeHealth},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, calls := engineTestFixture(t, test.runtime, replaceStub{})
			_, err := engine.Apply(context.Background(), templatecatalog.XrayConfig, 2)
			if !IsCode(err, test.code) {
				t.Fatalf("err=%v", err)
			}
			if !contains(*calls, "file-rollback") || !contains(*calls, "runtime-rollback") || !contains(*calls, "state:rollback") {
				t.Fatalf("calls=%v", *calls)
			}
		})
	}
}

func TestEngineWriteFailureDoesNotTouchRuntime(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{commitErr: errors.New("write")})
	_, err := engine.Apply(context.Background(), templatecatalog.XrayConfig, 2)
	if !IsCode(err, CodeReplace) {
		t.Fatalf("err=%v", err)
	}
	if contains(*calls, "runtime-apply") || contains(*calls, "runtime-rollback") {
		t.Fatalf("calls=%v", *calls)
	}
}

func TestEngineRejectsTemplateRaceBeforeBackup(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{}, replaceStub{})
	reads := 0
	engine.hash = func(string) (string, error) {
		reads++
		if reads == 1 {
			return "validated", nil
		}
		return "changed", nil
	}
	_, err := engine.Apply(context.Background(), templatecatalog.XrayConfig, 2)
	if !IsCode(err, CodeValidation) {
		t.Fatalf("err=%v", err)
	}
	if contains(*calls, "backup") || contains(*calls, "replace-prepare") {
		t.Fatalf("calls=%v", *calls)
	}
}

func TestEngineRollbackFailureRequiresRecovery(t *testing.T) {
	engine, calls := engineTestFixture(t, runtimeStub{healthErr: errors.New("health")}, replaceStub{rollbackErr: errors.New("disk")})
	_, err := engine.Apply(context.Background(), templatecatalog.XrayConfig, 2)
	if !IsCode(err, CodeRollback) {
		t.Fatalf("err=%v", err)
	}
	if !contains(*calls, "state:recovery_required") {
		t.Fatalf("calls=%v", *calls)
	}
}

func TestOSReplacementRestoresExactPriorBytesAndMode(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "template")
	target := filepath.Join(root, "system")
	if err := os.WriteFile(source, []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("prior\n"), 0640); err != nil {
		t.Fatal(err)
	}
	replacement, err := (osReplacer{}).Prepare(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = replacement.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = replacement.Rollback(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(target)
	info, _ := os.Stat(target)
	if string(content) != "prior\n" || info.Mode().Perm() != 0640 {
		t.Fatalf("content=%q mode=%o", content, info.Mode().Perm())
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
