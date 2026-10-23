package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type xrayRoundTripper func(*http.Request) (*http.Response, error)

func (f xrayRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestXrayPrerequisiteDetection(t *testing.T) {
	for _, tt := range []struct {
		name, load, leftover string
		failed, confirm      bool
		want                 string
		calls                int
	}{
		{name: "existing", load: "loaded"},
		{name: "masked", load: "masked"},
		{name: "detection failure", failed: true, want: "xray_detection_failed"},
		{name: "empty response", want: "xray_detection_failed"},
		{name: "binary without service", load: "not-found", leftover: "/usr/local/bin/xray", want: "xray_partial_installation"},
		{name: "config without service", load: "not-found", leftover: "/usr/local/etc/xray", want: "xray_partial_installation"},
		{name: "declined", load: "not-found", want: "xray_install_declined", calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newLifecycleEnv(t)
			env.runner.output = []byte(tt.load)
			if tt.failed {
				env.runner.failWith = "show"
			}
			if tt.leftover != "" {
				writeLifecycleFile(t, env.fs.Path(tt.leftover), "existing")
			}
			op := &Operation{ID: ID()}
			confirmations := 0
			err := env.e.ensureXrayInstalled(context.Background(), op, func() bool { confirmations++; return tt.confirm }, io.Discard,
				&http.Client{Transport: xrayRoundTripper(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected download"); return nil, nil })},
				func(context.Context, string, string, string) error { t.Fatal("unexpected installation"); return nil })
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error=%v, want=%s", err, tt.want)
			}
			if confirmations != tt.calls {
				t.Fatalf("confirmation count %d", confirmations)
			}
		})
	}
}

func TestXrayPrerequisiteInstallAndRetry(t *testing.T) {
	env := newLifecycleEnv(t)
	env.runner.output = []byte("not-found\n")
	op := &Operation{SchemaVersion: 1, ID: ID(), State: "preparing", Stage: "xray_install"}
	versions := 0
	client := &http.Client{Transport: xrayRoundTripper(func(r *http.Request) (*http.Response, error) {
		body := "#!/bin/bash\nexit 0\n"
		switch r.URL.String() {
		case xrayLatestURL:
			versions++
			body = `{"tag_name":"v26.10.17","prerelease":false,"draft":false}`
		case xrayInstallerURL:
		default:
			t.Fatalf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	count := 0
	run := func(_ context.Context, script, version, log string) error {
		count++
		if version != "v26.10.17" || filepath.Dir(script) != env.e.Store.Dir(op.ID) || filepath.Dir(log) != filepath.Dir(script) {
			t.Fatal("wrong installation inputs")
		}
		saved, err := env.e.Store.LoadID(op.ID)
		if err != nil || !saved.XrayInstallStarted || saved.XrayVersion != version {
			t.Fatalf("missing retry evidence: %v %v", saved, err)
		}
		if count == 1 {
			return errors.New("interrupted")
		}
		return nil
	}
	confirmations := 0
	confirm := func() bool { confirmations++; return true }
	if err := env.e.ensureXrayInstalled(context.Background(), op, confirm, io.Discard, client, run); err == nil || !strings.Contains(err.Error(), "xray_install_failed") {
		t.Fatal(err)
	}
	// A partial service created by the first attempt is owned by this operation.
	env.runner.output = []byte("loaded\n")
	if err := env.e.ensureXrayInstalled(context.Background(), op, confirm, io.Discard, client, run); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || confirmations != 1 || count != 2 {
		t.Fatalf("versions=%d confirms=%d runs=%d", versions, confirmations, count)
	}
	if got := readLifecycleFile(t, env.fs.Path("/etc/systemd/system/xray.service.d/20-assets.conf")); !strings.Contains(got, "XRAY_LOCATION_ASSET=/usr/local/share/xray") {
		t.Fatal(got)
	}
	for _, call := range env.runner.calls {
		if strings.Contains(call, "restart") || strings.Contains(call, "stop ") || strings.Contains(call, "tproxy") {
			t.Fatalf("unexpected data-plane action %s", call)
		}
	}
}

func TestXrayPrerequisiteRejectsNonStableAndHTTPFailure(t *testing.T) {
	for _, body := range []string{`{"tag_name":"v26.1.1","prerelease":true}`, `{"tag_name":"v26.1.1","draft":true}`, `{"tag_name":"v26.1.1-beta"}`, `not json`, `{"tag_name":"../../evil"}`} {
		t.Run(body, func(t *testing.T) {
			env := newLifecycleEnv(t)
			env.runner.output = []byte("not-found")
			client := &http.Client{Transport: xrayRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			err := env.e.ensureXrayInstalled(context.Background(), &Operation{ID: ID()}, func() bool { return true }, io.Discard, client, nil)
			if err == nil || !strings.Contains(err.Error(), "xray_release_invalid") {
				t.Fatal(err)
			}
		})
	}
	_, err := xrayDownload(context.Background(), &http.Client{Transport: xrayRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}, nil
	})}, xrayInstallerURL)
	if err == nil {
		t.Fatal("HTTP failure accepted")
	}
}

func TestStandaloneXrayHookIsFreshOnly(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		t.Run(map[bool]string{true: "fresh", false: "update"}[fresh], func(t *testing.T) {
			env := newLifecycleEnv(t)
			writeLifecycleFile(t, env.e.InstallerSource, "installer")
			if !fresh {
				writeLifecycleFile(t, env.fs.Path("/usr/local/sbin/xray-core-helperd"), "installed")
				writeLifecycleFile(t, env.fs.Path("/etc/xray-core-helper/helper.env"), "XRAY_HELPER_WEB_ENABLED=0\n")
			}
			env.e.EnsureXray = func(_ context.Context, op *Operation) error {
				if !fresh {
					t.Fatal("update attempted prerequisite installation")
				}
				if op.Stage != "xray_install" {
					t.Fatal(op.Stage)
				}
				op.XrayVersion = "v26.10.17"
				op.XrayInstallStarted = true
				if err := env.e.Store.Save(op); err != nil {
					t.Fatal(err)
				}
				return errors.New("xray_install_failed")
			}
			web := false
			op, err := env.e.PrepareLocal(context.Background(), localReleaseFixture(t), &web)
			if fresh {
				if err == nil || op.Stage != "xray_install" || op.State != "failed" {
					t.Fatalf("%v %v", op, err)
				}
				saved, err := env.e.Store.LoadID(op.ID)
				if err != nil || saved.XrayVersion != "v26.10.17" || !saved.XrayInstallStarted {
					t.Fatalf("%v %v", saved, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestXrayPrerequisitePreservesConflictingAssetsDropIn(t *testing.T) {
	env := newLifecycleEnv(t)
	env.runner.output = []byte("loaded")
	target := env.fs.Path("/etc/systemd/system/xray.service.d/20-assets.conf")
	writeLifecycleFile(t, target, "operator settings")
	op := &Operation{SchemaVersion: 1, ID: ID(), XrayVersion: "v26.10.17", XrayInstallStarted: true}
	client := &http.Client{Transport: xrayRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("script"))}, nil
	})}
	err := env.e.ensureXrayInstalled(context.Background(), op, nil, io.Discard, client, func(context.Context, string, string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "xray_assets_conflict") {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "operator settings" {
		t.Fatal("overwritten")
	}
}

func TestXrayInstallerArgumentsEnvironmentAndLog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "installer.sh")
	// Only a hermetic fixture is executed; no system installer or host units.
	if err := os.WriteFile(script, []byte("printf '%s\\n' \"$@\" \"$DAT_PATH\" \"$JSON_PATH\" \"$JSONS_PATH\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAT_PATH", "/operator/custom")
	t.Setenv("JSON_PATH", "/operator/config")
	t.Setenv("JSONS_PATH", "/operator/confdir")
	log := filepath.Join(dir, "install.log")
	if err := runXrayInstaller(context.Background(), script, "v26.10.17", log); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "install\n--version\nv26.10.17\n--logrotate\n04:00:00\n/usr/local/share/xray\n/usr/local/etc/xray\n") || strings.Contains(string(raw), "/operator/") {
		t.Fatal(string(raw))
	}
	info, _ := os.Stat(log)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
