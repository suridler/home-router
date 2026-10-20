package webapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  [][]string
	output string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, args []string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	return []byte(f.output), f.err
}
func fixture(t *testing.T, enabled bool) (*Runtime, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	f := &fakeRunner{}
	r, err := New("/tmp/helper.sock", enabled)
	if err != nil {
		t.Fatal(err)
	}
	r.envPath = filepath.Join(root, "run", "web.env")
	r.tokenPath = filepath.Join(root, "state", "bootstrap-token")
	r.runner = f
	r.group = func() (int, error) { return os.Getegid(), nil }
	r.dependencies = func() error { return nil }
	return r, f
}
func TestModeIsExplicitAndStrict(t *testing.T) {
	for _, v := range []string{"", "1", "0", "true", "false", " 0", "2"} {
		enabled, err := Enabled(v)
		valid := v == "" || v == "1" || v == "0"
		if (err == nil) != valid || (valid && enabled != (v != "0")) {
			t.Fatalf("%q: %t %v", v, enabled, err)
		}
	}
}
func TestHeadlessNeedsNoWebDependenciesOrFiles(t *testing.T) {
	r, f := fixture(t, false)
	r.group = func() (int, error) { t.Fatal("web group accessed"); return 0, nil }
	r.dependencies = func() error { t.Fatal("web dependencies accessed"); return nil }
	if err := r.Prepare(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(r.envPath)); !os.IsNotExist(err) {
		t.Fatal("headless created runtime files")
	}
	f.output = "not-found\n"
	f.err = errors.New("unit missing")
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatal(f.calls)
	}
	f.output = "loaded\n"
	f.err = nil
	f.calls = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"show", "--property=LoadState", "--value", unit}, {"stop", unit}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatal(f.calls)
	}
}
func TestEnabledLifecycleUsesOnlyQueuedFixedUnit(t *testing.T) {
	r, f := fixture(t, true)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, [][]string{{"restart", "--no-block", unit}}) {
		t.Fatal(f.calls)
	}
	f.err = errors.New("raw secret command output")
	if err := r.Reconcile(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestBootstrapTokenPersistsUntilSetupCompletion(t *testing.T) {
	r, _ := fixture(t, true)
	ctx := context.Background()
	if err := r.Prepare(ctx, true); err != nil {
		t.Fatal(err)
	}
	token, err := readToken(r.tokenPath)
	if err != nil || len(token) != 64 {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		mode os.FileMode
	}{{r.envPath, 0640}, {r.tokenPath, 0600}} {
		info, err := os.Stat(test.path)
		if err != nil || info.Mode().Perm() != test.mode {
			t.Fatalf("metadata: %s %v", test.path, err)
		}
	}
	raw, _ := os.ReadFile(r.envPath)
	if !strings.Contains(string(raw), "XRAYUI_BOOTSTRAP_TOKEN="+token) || strings.Contains(string(raw), "HOST=") || strings.Contains(string(raw), "CONTROL_ENABLED=") {
		t.Fatal("invalid generated environment")
	}
	if err = r.Prepare(ctx, true); err != nil {
		t.Fatal(err)
	}
	again, _ := readToken(r.tokenPath)
	if token != again {
		t.Fatal("token changed during setup restart")
	}
	if err = r.Prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(r.envPath)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "TOKEN") {
		t.Fatal("token retained after completion")
	}
	if _, err = os.Stat(r.tokenPath); !os.IsNotExist(err) {
		t.Fatal("private token retained")
	}
	if err = r.Prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
}
func TestPreparationFailsClosedAndRedacted(t *testing.T) {
	for _, failure := range []string{"dependency", "identity", "token-mode", "token-symlink", "token-parent-symlink", "env-symlink", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			r, _ := fixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "dependency" {
				r.dependencies = func() error { return errors.New("secret") }
			}
			if failure == "identity" {
				r.group = func() (int, error) { return 0, errors.New("secret") }
			}
			if strings.HasPrefix(failure, "token-") {
				if err := os.MkdirAll(filepath.Dir(r.tokenPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(r.tokenPath, []byte(strings.Repeat("a", 64)), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "token-symlink" {
				target := r.tokenPath
				r.tokenPath += ".link"
				if err := os.Symlink(target, r.tokenPath); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "token-parent-symlink" {
				parent := filepath.Dir(r.tokenPath)
				link := parent + ".link"
				if err := os.Symlink(parent, link); err != nil {
					t.Fatal(err)
				}
				r.tokenPath = filepath.Join(link, "bootstrap-token")
			}
			if failure == "env-symlink" {
				if err := os.MkdirAll(filepath.Dir(r.envPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), r.envPath); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "cancelled" {
				cancel()
			}
			if err := r.Prepare(ctx, true); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("failure not safe: %v", err)
			}
		})
	}
}
