package templatecompare

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/templatecatalog"
)

func TestCompareExactBytes(t *testing.T) {
	tests := []struct {
		name           string
		template       *string
		system         *string
		wantState      api.TemplateState
		wantTemplate   bool
		wantSystem     bool
		wantHashesSame bool
	}{
		{name: "equal", template: text("line = 1\n"), system: text("line = 1\n"), wantState: api.TemplateSynchronized, wantTemplate: true, wantSystem: true, wantHashesSame: true},
		{name: "formatting differs", template: text("line=1\n"), system: text("line = 1\n"), wantState: api.TemplateDifferent, wantTemplate: true, wantSystem: true},
		{name: "template missing", system: text("line = 1\n"), wantState: api.TemplateMissing, wantSystem: true},
		{name: "system missing", template: text("line = 1\n"), wantState: api.TemplateSystemMissing, wantTemplate: true},
		{name: "both missing", wantState: api.TemplateInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			entry := templatecatalog.Entry{ID: templatecatalog.Sysctl, WorkingPath: filepath.Join(root, "template"), SystemPath: filepath.Join(root, "system")}
			writeOptional(t, entry.WorkingPath, test.template)
			writeOptional(t, entry.SystemPath, test.system)
			comparer, err := New(nil)
			if err != nil {
				t.Fatal(err)
			}
			status := comparer.Compare(context.Background(), entry)
			if status.State != test.wantState || status.Summary.TemplateAvailable != test.wantTemplate || status.Summary.SystemAvailable != test.wantSystem {
				t.Fatalf("status = %#v", status)
			}
			if test.wantHashesSame && status.TemplateHash != status.SystemHash {
				t.Fatalf("hashes differ: %#v", status)
			}
			if err := status.Validate(); err != nil {
				t.Fatalf("invalid API status: %v (%#v)", err, status)
			}
		})
	}
}

func TestSafeReaderRejectsSymlinkAndOversizeFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSafeReader(4)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		code Code
	}{
		{name: "symlink", path: link, code: CodeUnsafeFile},
		{name: "oversize", path: target, code: CodeTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := reader.Read(context.Background(), test.path)
			if !snapshot.Present {
				t.Fatal("existing unsafe file reported missing")
			}
			assertReadCode(t, err, test.code)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), test.path) {
				t.Fatalf("error leaked file data: %v", err)
			}
		})
	}
}

func TestSafeReaderDetectsReadRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSafeReader(MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	reader.afterRead = func(path string) {
		if err := os.WriteFile(path, []byte("changed-after-read"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := reader.Read(context.Background(), path)
	if !snapshot.Present {
		t.Fatal("raced file reported missing")
	}
	assertReadCode(t, err, CodeChanged)
}

func TestCompareProjectsUnreadableSideWithoutDetails(t *testing.T) {
	secret := errors.New("permission denied for /private/secret-config")
	reader := &fakeReader{results: map[string]readResult{
		"/template": {snapshot: Snapshot{Present: true}, err: secret},
		"/system":   {snapshot: Snapshot{Present: true, Hash: strings.Repeat("a", 64)}},
	}}
	comparer, err := New(reader)
	if err != nil {
		t.Fatal(err)
	}
	status := comparer.Compare(context.Background(), templatecatalog.Entry{ID: templatecatalog.Sysctl, WorkingPath: "/template", SystemPath: "/system"})
	if status.State != api.TemplateInvalid || status.Summary.InvalidSide != api.TemplateSideTemplate || status.Summary.Code != string(CodeUnreadable) {
		t.Fatalf("status = %#v", status)
	}
	if strings.Contains(status.Summary.Code, "secret") || strings.Contains(status.Summary.Code, "private") {
		t.Fatalf("status leaked read failure: %#v", status)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("invalid API status: %v", err)
	}
}

func TestRevisionIsStableAndChangesWithEitherSide(t *testing.T) {
	entry := templatecatalog.Entry{ID: templatecatalog.XrayConfig, WorkingPath: "/template", SystemPath: "/system"}
	reader := &fakeReader{results: map[string]readResult{
		"/template": {snapshot: Snapshot{Present: true, Hash: strings.Repeat("a", 64)}},
		"/system":   {snapshot: Snapshot{Present: true, Hash: strings.Repeat("b", 64)}},
	}}
	comparer, _ := New(reader)
	first := comparer.Compare(context.Background(), entry)
	second := comparer.Compare(context.Background(), entry)
	if first.Revision != second.Revision {
		t.Fatalf("stable inputs produced revisions %q and %q", first.Revision, second.Revision)
	}
	reader.results["/system"] = readResult{snapshot: Snapshot{Present: true, Hash: strings.Repeat("c", 64)}}
	third := comparer.Compare(context.Background(), entry)
	if third.Revision == first.Revision {
		t.Fatal("system hash change did not change revision")
	}
	if len(third.Revision) != 64 {
		t.Fatalf("revision length = %d", len(third.Revision))
	}
}

type readResult struct {
	snapshot Snapshot
	err      error
}

type fakeReader struct{ results map[string]readResult }

func (reader *fakeReader) Read(_ context.Context, path string) (Snapshot, error) {
	result := reader.results[path]
	return result.snapshot, result.err
}

func assertReadCode(t *testing.T, err error, want Code) {
	t.Helper()
	var failure *readError
	if !errors.As(err, &failure) || failure.code != want {
		t.Fatalf("error = %v; want code %q", err, want)
	}
}

func text(value string) *string { return &value }

func writeOptional(t *testing.T, path string, value *string) {
	t.Helper()
	if value == nil {
		return
	}
	if err := os.WriteFile(path, []byte(*value), 0600); err != nil {
		t.Fatal(err)
	}
}
