package acceptsystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
	"home-router/xray-core-helper/internal/templatevalidation"
)

type projectionFake struct {
	current, fresh api.TemplateStatusResponse
	calls          *[]string
}

func (fake *projectionFake) Current() (api.TemplateStatusResponse, bool) { return fake.current, true }
func (fake *projectionFake) Refresh(context.Context) api.TemplateStatusResponse {
	*fake.calls = append(*fake.calls, "refresh")
	return fake.fresh
}

type validatorFake struct {
	err   error
	calls *[]string
}

func (fake validatorFake) Validate(context.Context, templatecatalog.ID, string) (templatevalidation.Result, error) {
	*fake.calls = append(*fake.calls, "validate")
	return templatevalidation.Result{}, fake.err
}

type backupsFake struct {
	generation uint64
	calls      *[]string
}

func (fake *backupsFake) CompletedGeneration(context.Context, templatecatalog.ID) (uint64, error) {
	*fake.calls = append(*fake.calls, "generation")
	return fake.generation, nil
}
func (fake *backupsFake) Snapshot(context.Context, templatecatalog.ID, templatebackup.Side, templatebackup.State) (templatebackup.Record, error) {
	*fake.calls = append(*fake.calls, "snapshot")
	return templatebackup.Record{BackupID: "backup-1", Generation: fake.generation + 1}, nil
}
func (fake *backupsFake) SetState(context.Context, templatecatalog.ID, string, templatebackup.State) (templatebackup.Record, error) {
	*fake.calls = append(*fake.calls, "complete")
	fake.generation++
	return templatebackup.Record{BackupID: "backup-1", Generation: fake.generation}, nil
}

type resolverFake struct{ entry templatecatalog.Entry }

func (fake resolverFake) Lookup(id templatecatalog.ID) (templatecatalog.Entry, error) {
	if id != fake.entry.ID {
		return templatecatalog.Entry{}, errors.New("unknown")
	}
	return fake.entry, nil
}

type candidateFake struct{ calls *[]string }

func (fake *candidateFake) Path() string { return "/candidate" }
func (fake *candidateFake) Commit(string) error {
	*fake.calls = append(*fake.calls, "replace")
	return nil
}
func (fake *candidateFake) Close() error { return nil }

type preparerFake struct{ calls *[]string }

func (fake preparerFake) Prepare(templatecatalog.ID, string, string) (Candidate, error) {
	*fake.calls = append(*fake.calls, "prepare")
	return &candidateFake{calls: fake.calls}, nil
}

func status(state api.TemplateState, revision string) api.TemplateStatusResponse {
	return api.TemplateStatusResponse{Templates: []api.TemplateStatus{{TemplateID: "test", State: state, Revision: revision}}}
}
func engineFixture(t *testing.T, state api.TemplateState) (*Engine, *[]string) {
	t.Helper()
	calls := []string{}
	projection := &projectionFake{current: status(state, "old"), fresh: status(api.TemplateSynchronized, "new"), calls: &calls}
	backups := &backupsFake{generation: 3, calls: &calls}
	engine, err := newEngine(projection, validatorFake{calls: &calls}, backups, resolverFake{templatecatalog.Entry{ID: "test", WorkingPath: "/template", SystemPath: "/system"}}, preparerFake{calls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	return engine, &calls
}

func TestAcceptValidatesBacksUpReplacesThenPublishes(t *testing.T) {
	engine, calls := engineFixture(t, api.TemplateDifferent)
	result, err := engine.Accept(context.Background(), "test", 3, "old")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.Generation != 4 || result.Revision != "new" || result.State != api.TemplateSynchronized {
		t.Fatalf("result=%#v", result)
	}
	want := []string{"generation", "prepare", "validate", "snapshot", "replace", "complete", "refresh"}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls=%v want=%v", *calls, want)
	}
}
func TestAcceptSynchronizedIsNoOp(t *testing.T) {
	engine, calls := engineFixture(t, api.TemplateSynchronized)
	result, err := engine.Accept(context.Background(), "test", 3, "old")
	if err != nil || result.Accepted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(*calls, []string{"generation"}) {
		t.Fatalf("calls=%v", *calls)
	}
}
func TestAcceptStablePreconditions(t *testing.T) {
	tests := []struct {
		name       string
		state      api.TemplateState
		generation uint64
		revision   string
		code       Code
	}{{"revision", api.TemplateDifferent, 3, "stale", CodeRevisionConflict}, {"generation", api.TemplateDifferent, 2, "old", CodeGenerationConflict}, {"system missing", api.TemplateSystemMissing, 3, "old", CodeSourceMissing}, {"template missing", api.TemplateMissing, 3, "old", CodeTargetMissing}, {"invalid", api.TemplateInvalid, 3, "old", CodeStateInvalid}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, _ := engineFixture(t, test.state)
			_, err := engine.Accept(context.Background(), "test", test.generation, test.revision)
			if !IsCode(err, test.code) {
				t.Fatalf("err=%v want=%s", err, test.code)
			}
		})
	}
}

func TestOSCandidateRejectsTemplateChangeAfterBackup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "system")
	target := filepath.Join(root, "template")
	if err := os.WriteFile(source, []byte("system\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	expected := hashOf(t, target)
	candidate, err := (osPreparer{}).Prepare(templatecatalog.Sysctl, source, target)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	if err := os.WriteFile(target, []byte("newer operator change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Commit(expected); err == nil {
		t.Fatal("Commit accepted changed target")
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != "newer operator change\n" {
		t.Fatalf("target=%q", contents)
	}
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func TestOSCandidateCopiesValidatedBytesAndPreservesTargetMetadata(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "system")
	target := filepath.Join(root, "template")
	if err := os.WriteFile(source, []byte("system bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0640); err != nil {
		t.Fatal(err)
	}
	candidate, err := (osPreparer{}).Prepare(templatecatalog.Sysctl, source, target)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	validated, err := os.ReadFile(candidate.Path())
	if err != nil || string(validated) != "system bytes\n" {
		t.Fatalf("candidate=%q err=%v", validated, err)
	}
	if err := os.WriteFile(source, []byte("raced bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Commit(hashOf(t, target)); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(target)
	info, _ := os.Stat(target)
	if string(contents) != "system bytes\n" || info.Mode().Perm() != 0640 {
		t.Fatalf("contents=%q mode=%o", contents, info.Mode().Perm())
	}
}
