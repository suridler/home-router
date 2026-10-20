package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
)

func put(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, p string) string {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestSnapshotRollbackRemovesNewFilesPreservesMetadataAndOriginal(t *testing.T) {
	root := t.TempDir()
	fs := Files{SystemRoot: root}
	state := NewStore(filepath.Join(t.TempDir(), "update"))
	put(t, fs.Path("/opt/web/old.js"), "old")
	if err := os.Chmod(fs.Path("/opt/web/old.js"), 0640); err != nil {
		t.Fatal(err)
	}
	op := &Operation{SchemaVersion: 1, ID: ID(), TargetVersion: "helper-v2.0.0", State: "applying"}
	var err error
	op.Before, err = fs.Inspect([]string{"/opt/web", "/usr/local/bin/new"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(state.Dir(op.ID), "snapshot")
	save := func() error { return state.Save(op) }
	if err = fs.Snapshot(op, snapshot, save); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	put(t, filepath.Join(src, "new.js"), "new")
	if err = fs.Apply(op, []Target{{Path: "/opt/web", Source: src}}, save); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(fs.Path("/opt/web/old.js")); !os.IsNotExist(err) {
		t.Fatal("obsolete file retained")
	}
	put(t, fs.Path("/usr/local/bin/new"), "new")
	if err = fs.Restore(op, snapshot, save); err != nil {
		t.Fatal(err)
	}
	if got := read(t, fs.Path("/opt/web/old.js")); got != "old" {
		t.Fatal(got)
	}
	st, _ := os.Stat(fs.Path("/opt/web/old.js"))
	if st.Mode().Perm() != 0640 {
		t.Fatal("metadata changed")
	}
	if _, err = os.Stat(fs.Path("/usr/local/bin/new")); !os.IsNotExist(err) {
		t.Fatal("new file survived rollback")
	}
	// A subsequent attempt cannot replace the original snapshot with mixed data.
	put(t, fs.Path("/opt/web/old.js"), "changed again")
	if err = fs.Snapshot(op, snapshot, save); err != nil {
		t.Fatal(err)
	}
	if err = fs.Restore(op, snapshot, save); err != nil {
		t.Fatal(err)
	}
	if read(t, fs.Path("/opt/web/old.js")) != "old" {
		t.Fatal("original snapshot overwritten")
	}
}

func TestDamagedSnapshotFailsBeforeAnyRestore(t *testing.T) {
	fs := Files{SystemRoot: t.TempDir()}
	put(t, fs.Path("/a"), "old-a")
	put(t, fs.Path("/b"), "old-b")
	op := &Operation{SchemaVersion: 1, ID: ID()}
	op.Before, _ = fs.Inspect([]string{"/a", "/b"})
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err := fs.Snapshot(op, snapshot, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	put(t, fs.Path("/a"), "new-a")
	put(t, fs.Path("/b"), "new-b")
	put(t, filepath.Join(snapshot, "1"), "damaged")
	if err := fs.Restore(op, snapshot, func() error { return nil }); err == nil {
		t.Fatal("damaged snapshot accepted")
	}
	if read(t, fs.Path("/a")) != "new-a" {
		t.Fatal("partial restore before validation")
	}
}

func TestJournalFailureStopsBeforeDestinationMutation(t *testing.T) {
	fs := Files{SystemRoot: t.TempDir()}
	put(t, fs.Path("/a"), "old")
	src := filepath.Join(t.TempDir(), "candidate")
	put(t, src, "new")
	op := &Operation{}
	err := fs.Apply(op, []Target{{Path: "/a", Source: src}}, func() error { return errors.New("disk full") })
	if err == nil || read(t, fs.Path("/a")) != "old" {
		t.Fatal("write happened before durable intent")
	}
}

func TestRejectDestinationAndParentSymlinks(t *testing.T) {
	fs := Files{SystemRoot: t.TempDir()}
	outside := t.TempDir()
	if err := os.Symlink(outside, fs.Path("/linked")); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "candidate")
	put(t, src, "new")
	if err := replaceTree(src, fs.Path("/linked/file")); err == nil {
		t.Fatal("followed parent link")
	}
}

func TestStorePrivateDurableAndUnsupportedSchema(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "journal"))
	op := &Operation{SchemaVersion: 1, ID: ID(), State: "prepared", TargetVersion: "helper-v1.0.0"}
	if err := s.Save(op); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got.ID != op.ID {
		t.Fatalf("%+v %v", got, err)
	}
	st, _ := os.Stat(filepath.Join(s.Dir(op.ID), "state.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatal("journal not private")
	}
	got.SchemaVersion = 2
	b, _ := json.Marshal(got)
	put(t, filepath.Join(s.Dir(op.ID), "state.json"), string(b))
	if _, err = s.Load(); err == nil {
		t.Fatal("future schema accepted")
	}
}

func TestRuntimeExclusiveLockAndNoSymlink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime.lock")
	release, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Lock(p); !errors.Is(err, ErrBusy) {
		t.Fatal("second writer admitted")
	}
	release()
	if err = os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "other"), p); err != nil {
		t.Fatal(err)
	}
	if _, err = Lock(p); err == nil {
		t.Fatal("lock symlink followed")
	}
}

func TestDowngradeFloorAndExplicitVersions(t *testing.T) {
	for _, v := range []string{"helper-v1.0.0", "helper-v2.0.0", "helper-v3.0.0-beta.1"} {
		if err := CheckDowngrade(Manifest{}, v); err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{MinimumVersion: "helper-v2.0.0"}
	if err := CheckDowngrade(m, "helper-v1.9.9"); err == nil {
		t.Fatal("downgrade floor ignored")
	}
	if err := CheckDowngrade(m, "helper-v2.0.0"); err != nil {
		t.Fatal(err)
	}
}

type fakeRunner struct {
	calls  []string
	fail   string
	output []byte
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if f.fail != "" && strings.Contains(line, f.fail) {
		return nil, errors.New("failure")
	}
	return f.output, nil
}
func TestPreflightRejectsMalformedSettingsWithoutWrites(t *testing.T) {
	e := &Engine{Files: Files{SystemRoot: t.TempDir()}, ConfigPath: bootstrap.DefaultConfigPath}
	put(t, e.Files.Path(e.ConfigPath), "this is not TOML")
	if err := e.Preflight(context.Background()); err == nil {
		t.Fatal("invalid settings accepted")
	}
}
