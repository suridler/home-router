package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/xrayservice"
)

func TestXrayArchiveChecksumAndVersionComparison(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	if got, err := xrayArchiveSHA256("MD5=bad\nSHA2-256= " + hash + "\nSHA2-512=other\n"); err != nil || got != hash {
		t.Fatalf("%s %v", got, err)
	}
	for _, s := range []string{"", "SHA2-512=" + hash, "SHA2-256=zz", "SHA2-256=" + strings.Repeat("a", 63)} {
		if _, err := xrayArchiveSHA256(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, tt := range []struct {
		latest, current string
		want            bool
	}{{"v26.10.17", "26.3.27", true}, {"v26.10.17", "26.10.17", false}, {"v26.3.27", "26.10.17", false}, {"v26.10.17", "unknown", false}} {
		if got := xrayVersionNewer(tt.latest, tt.current); got != tt.want {
			t.Fatalf("%v: %t", tt, got)
		}
	}
}
func TestXrayBinaryExtractionRejectsMissingDuplicateAndSymlink(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "duplicate", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			var buf bytes.Buffer
			writer := zip.NewWriter(&buf)
			header := &zip.FileHeader{Name: "xray"}
			header.SetMode(0755)
			if kind == "missing" {
				header.Name = "../xray"
			}
			if kind == "symlink" {
				header.SetMode(os.ModeSymlink | 0777)
			}
			f, _ := writer.CreateHeader(header)
			io.WriteString(f, "binary")
			if kind == "duplicate" {
				f, _ = writer.Create("xray")
				io.WriteString(f, "duplicate")
			}
			writer.Close()
			reader, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			target := filepath.Join(t.TempDir(), "candidate")
			hash, err := extractXrayBinary(reader, target)
			if kind == "valid" {
				if err != nil || len(hash) != 64 {
					t.Fatal(hash, err)
				}
			} else if err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}
func TestReplaceXrayBinaryVerifiesHashBeforeChangingDestination(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "new"), filepath.Join(dir, "xray")
	os.WriteFile(source, []byte("new"), 0755)
	os.WriteFile(target, []byte("old"), 0750)
	if err := replaceXrayBinary(source, target, strings.Repeat("0", 64)); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "old" {
		t.Fatal("destination changed")
	}
	hash, _ := fileHash(source)
	if err := replaceXrayBinary(source, target, hash); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0750 {
		t.Fatal("mode changed")
	}
}
func coreUpgradeFixture(t *testing.T) (*lifecycleEnv, *XrayUpgrade, string) {
	t.Helper()
	env := newLifecycleEnv(t)
	dir := t.TempDir()
	binary, config, assets := filepath.Join(dir, "xray"), filepath.Join(dir, "config.json"), filepath.Join(dir, "assets")
	os.Mkdir(assets, 0755)
	os.WriteFile(binary, []byte("original binary"), 0755)
	os.WriteFile(config, []byte("original config"), 0600)
	env.e.XrayInstallation = func(context.Context) (xrayservice.Installation, error) {
		return xrayservice.Installation{Binary: binary, Config: config, AssetDir: assets}, nil
	}
	candidate := "#!/bin/sh\nexit 0\n"
	env.e.XrayDownload = func(_ context.Context, _ string, target string) (string, error) {
		if err := os.WriteFile(target, []byte(candidate), 0755); err != nil {
			return "", err
		}
		return fileHash(target)
	}
	s := &XrayUpgrade{ID: ID(), ActionID: ID(), Action: "apply", Version: "v26.10.17", PreviousVersion: "26.3.27", State: "launching", Stage: "launching", Binary: binary, Config: config, Assets: assets}
	if err := env.e.saveXrayUpgrade(s); err != nil {
		t.Fatal(err)
	}
	return env, s, candidate
}
func TestCoreUpgradeFailureOffersExplicitRecoveryWithoutAutomaticRestore(t *testing.T) {
	env, s, candidate := coreUpgradeFixture(t)
	env.runner.failWith = "restart xray.service"
	if err := env.e.RunXrayUpgrade(context.Background(), s.ID, s.ActionID); err == nil {
		t.Fatal("restart failure ignored")
	}
	failed, err := env.e.xrayUpgradeState()
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || failed.Error != "xray_restart_failed" || !failed.BackupReady {
		t.Fatalf("%+v", failed)
	}
	if raw, _ := os.ReadFile(s.Binary); string(raw) != candidate {
		t.Fatal("automatic rollback occurred")
	}
	if raw, _ := os.ReadFile(s.Config); string(raw) != "original config" {
		t.Fatal("config changed")
	}
	if len(env.runner.calls) == 0 || env.runner.calls[len(env.runner.calls)-1] != "/usr/bin/systemctl start xray-core-helper.service" {
		t.Fatal("helper recovery not started")
	}
	// Explicit rollback: only the binary is restored, then both services return.
	env.runner.failWith = ""
	failed.State = "launching"
	failed.Action = "rollback"
	failed.ActionID = ID()
	env.e.saveXrayUpgrade(failed)
	if err = env.e.RunXrayUpgrade(context.Background(), failed.ID, failed.ActionID); err != nil {
		t.Fatal(err)
	}
	restored, _ := env.e.xrayUpgradeState()
	if restored.State != "rolled_back" {
		t.Fatal(restored.State)
	}
	if raw, _ := os.ReadFile(s.Binary); string(raw) != "original binary" {
		t.Fatal("not restored")
	}
	if raw, _ := os.ReadFile(s.Config); string(raw) != "original config" {
		t.Fatal("config changed")
	}
}
func TestCoreUpgradeValidationFailureLeavesBinaryAndRestartsHelper(t *testing.T) {
	env, s, _ := coreUpgradeFixture(t)
	env.e.XrayDownload = func(_ context.Context, _ string, path string) (string, error) {
		os.WriteFile(path, []byte("#!/bin/sh\nexit 23\n"), 0755)
		return fileHash(path)
	}
	if err := env.e.RunXrayUpgrade(context.Background(), s.ID, s.ActionID); err == nil {
		t.Fatal("invalid config accepted")
	}
	state, _ := env.e.xrayUpgradeState()
	if state.BackupReady || state.Changed || state.Error != "xray_config_validation_failed" {
		t.Fatalf("%+v", state)
	}
	if raw, _ := os.ReadFile(s.Binary); string(raw) != "original binary" {
		t.Fatal("binary changed")
	}
	for _, call := range env.runner.calls {
		if strings.Contains(call, "restart xray.service") {
			t.Fatal("Xray restarted after failed test")
		}
	}
}
func TestCoreUpgradeSuccessAndRetryDoNotRebuildOrTouchGeodata(t *testing.T) {
	env, s, candidate := coreUpgradeFixture(t)
	if err := env.e.RunXrayUpgrade(context.Background(), s.ID, s.ActionID); err != nil {
		t.Fatal(err)
	}
	state, _ := env.e.xrayUpgradeState()
	if state.State != "succeeded" {
		t.Fatal(state)
	}
	state.Action = "retry"
	state.ActionID = ID()
	state.State = "launching"
	env.e.saveXrayUpgrade(state)
	env.e.XrayDownload = func(context.Context, string, string) (string, error) {
		t.Fatal("download on restart retry")
		return "", nil
	}
	if err := env.e.RunXrayUpgrade(context.Background(), state.ID, state.ActionID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(s.Binary); string(raw) != candidate {
		t.Fatal("wrong binary")
	}
	if entries, _ := os.ReadDir(s.Assets); len(entries) != 0 {
		t.Fatal("geodata changed")
	}
}
func TestCoreUpgradeDownloadFailureNeverStopsHelper(t *testing.T) {
	env, s, _ := coreUpgradeFixture(t)
	env.e.XrayDownload = func(context.Context, string, string) (string, error) { return "", errors.New("xray_download_failed") }
	if err := env.e.RunXrayUpgrade(context.Background(), s.ID, s.ActionID); err == nil {
		t.Fatal("failure ignored")
	}
	if len(env.runner.calls) != 0 {
		t.Fatal(env.runner.calls)
	}
	state, _ := env.e.xrayUpgradeState()
	if state.State != "failed" || state.BackupReady {
		t.Fatal(state)
	}
}
func TestCoreUpgradeHTTPAllowsResponseExtensionButRejectsCommandExtension(t *testing.T) {
	env := newLifecycleEnv(t)
	handler := env.e.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, body := range []string{`{"actionId":"` + ID() + `","binary":"/tmp/evil"}`, `{"actionId":"` + ID() + `","version":"v26.1.1"}`, `{"actionId":"bad"}`} {
		request := httptest.NewRequest("POST", "/v1/xray-upgrade/apply", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
	}
	s := &XrayUpgrade{ID: ID(), ActionID: ID(), Version: "v26.10.17", State: "running", UpdatedAt: time.Now()}
	env.e.saveXrayUpgrade(s)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/v1/network-probe", nil))
	if response.Code != 409 || !strings.Contains(response.Body.String(), "xray_upgrade_pending") {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestCoreUpgradeInterruptedWorkerBecomesRecoverable(t *testing.T) {
	env, s, _ := coreUpgradeFixture(t)
	s.State = "running"
	s.Stage = "restart_xray"
	s.BackupReady = true
	s.UpdatedAt = time.Now().Add(-time.Minute)
	if err := writeJSON(filepath.Join(env.e.Store.Root, "xray-upgrade.json"), s); err != nil {
		t.Fatal(err)
	}
	env.runner.output = []byte("inactive\n")
	state, err := env.e.XrayUpgradeStatus(context.Background())
	if err != nil || state.State != "failed" || state.Error != "xray_upgrade_interrupted" || !state.BackupReady {
		t.Fatalf("%+v %v", state, err)
	}
}
