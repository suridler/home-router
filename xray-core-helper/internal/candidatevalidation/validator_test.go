package candidatevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatalimits"
)

type runnerCall struct {
	executable  string
	args        []string
	environment []string
}

func TestValidateAcceptsGeositeAbovePrevious64MiBLimit(t *testing.T) {
	fixture := newFixture(t, `{}`)
	writeAsset(t, fixture.download.GeositePath, "")
	const size = 73703302
	if err := os.Truncate(fixture.download.GeositePath, size); err != nil {
		t.Fatal(err)
	}
	writeAsset(t, fixture.download.GeoipPath, "candidate geoip")
	runner := &fakeRunner{onRun: func(_ context.Context, _ string, _ []string, environment []string) error {
		dir := strings.TrimPrefix(environment[0], "XRAY_LOCATION_ASSET=")
		info, err := os.Stat(filepath.Join(dir, "geosite.dat"))
		if err != nil || info.Size() != size {
			t.Fatalf("staged asset: %v, %v", info, err)
		}
		return nil
	}}
	if err := fixture.validator(t, runner).Validate(context.Background(), fixture.download); err != nil {
		t.Fatal(err)
	}
	if !runner.called {
		t.Fatal("candidate never reached Xray validator")
	}
}

type fakeRunner struct {
	call   runnerCall
	err    error
	onRun  func(context.Context, string, []string, []string) error
	called bool
	calls  int
}

func (runner *fakeRunner) Run(ctx context.Context, executable string, args, environment []string) error {
	runner.called = true
	runner.calls++
	runner.call = runnerCall{
		executable:  executable,
		args:        append([]string(nil), args...),
		environment: append([]string(nil), environment...),
	}
	if runner.onRun != nil {
		return runner.onRun(ctx, executable, args, environment)
	}
	return runner.err
}

type testFileSystem struct {
	osFileSystem
	managedRoot string
	backingRoot string
	removed     []string
	removeErr   error
	onSource    func(string)
}

func (files *testFileSystem) OpenSource(path string) (SourceFile, error) {
	if strings.HasPrefix(path, files.managedRoot+string(filepath.Separator)) {
		relative, err := filepath.Rel(files.managedRoot, path)
		if err != nil {
			return nil, err
		}
		path = filepath.Join(files.backingRoot, relative)
	}
	if files.onSource != nil {
		files.onSource(path)
	}
	return files.osFileSystem.OpenSource(path)
}

func (files *testFileSystem) RemoveAll(path string) error {
	files.removed = append(files.removed, path)
	if files.removeErr != nil {
		return files.removeErr
	}
	return files.osFileSystem.RemoveAll(path)
}

func TestValidateUsesIsolatedCandidateAndFixedCommand(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:userlist"]}]}}`)
	configBefore, err := os.ReadFile(fixture.config.Xray.Config)
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, fixture.download.GeositePath, "candidate geosite")
	writeAsset(t, fixture.download.GeoipPath, "candidate geoip")
	writeAsset(t, filepath.Join(fixture.companionDir, "geosite_user.dat"), "managed userlist")
	writeAsset(t, filepath.Join(fixture.companionDir, "unreferenced.dat"), "must not be copied")
	_ = os.Chmod(fixture.download.GeositePath, 0600)
	_ = os.Chmod(fixture.download.GeoipPath, 0600)
	_ = os.Chmod(filepath.Join(fixture.companionDir, "geosite_user.dat"), 0640)

	runner := &fakeRunner{}
	files := fixture.files
	runner.onRun = func(_ context.Context, executable string, args, environment []string) error {
		if executable != fixture.config.Xray.Binary {
			t.Errorf("executable = %q", executable)
		}
		if !reflect.DeepEqual(args, []string{"run", "-test", "-config", fixture.config.Xray.Config}) {
			t.Errorf("args = %#v", args)
		}
		if len(environment) != 1 || !strings.HasPrefix(environment[0], "XRAY_LOCATION_ASSET=") {
			t.Fatalf("environment = %#v", environment)
		}
		candidateDir := strings.TrimPrefix(environment[0], "XRAY_LOCATION_ASSET=")
		if !filepath.IsAbs(candidateDir) || filepath.Dir(candidateDir) != fixture.root {
			t.Fatalf("candidate directory = %q", candidateDir)
		}
		info, err := os.Stat(candidateDir)
		if err != nil || info.Mode().Perm() != candidateDirectoryMode {
			t.Fatalf("candidate directory mode = %v, error = %v", info, err)
		}
		entries, err := os.ReadDir(candidateDir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
			info, err := entry.Info()
			if err != nil || info.Mode().Perm() != candidateAssetMode {
				t.Errorf("%s mode = %v, error = %v", entry.Name(), info, err)
			}
		}
		if !reflect.DeepEqual(names, []string{"geoip.dat", "geosite.dat", "geosite_user.dat"}) {
			t.Errorf("candidate files = %#v", names)
		}
		for name, want := range map[string]string{
			"geosite.dat":      "candidate geosite",
			"geoip.dat":        "candidate geoip",
			"geosite_user.dat": "managed userlist",
		} {
			content, err := os.ReadFile(filepath.Join(candidateDir, name))
			if err != nil || string(content) != want {
				t.Errorf("%s content = %q, error = %v", name, content, err)
			}
		}
		return nil
	}

	validator := fixture.validator(t, runner)
	t.Setenv("XRAY_LOCATION_ASSET", "/secret/live/assets")
	t.Setenv("CANDIDATE_TEST_SECRET", "must not be inherited")
	if err := validator.Validate(context.Background(), fixture.download); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !runner.called {
		t.Fatal("Xray runner was not called")
	}
	if len(files.removed) != 1 {
		t.Fatalf("candidate cleanup calls = %d", len(files.removed))
	}
	if _, err := os.Stat(files.removed[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate remains after success: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.companionDir, "unreferenced.dat")); err != nil {
		t.Fatalf("live companion changed: %v", err)
	}
	liveCompanion := filepath.Join(fixture.companionDir, "geosite_user.dat")
	liveContent, err := os.ReadFile(liveCompanion)
	if err != nil || string(liveContent) != "managed userlist" {
		t.Fatalf("live companion content = %q, error = %v", liveContent, err)
	}
	liveInfo, err := os.Stat(liveCompanion)
	if err != nil || liveInfo.Mode().Perm() != 0640 {
		t.Fatalf("live companion mode = %v, error = %v", liveInfo, err)
	}
	configAfter, err := os.ReadFile(fixture.config.Xray.Config)
	if err != nil || string(configAfter) != string(configBefore) {
		t.Fatalf("configured JSONC changed, error = %v", err)
	}
}

func TestValidateCleansCandidateOnFailureAndCancellation(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool
		want   error
	}{
		{name: "xray failure", want: ErrXrayValidation},
		{name: "canceled", cancel: true, want: ErrValidationCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, `{"routing":{"rules":[]}}`)
			writeAsset(t, fixture.download.GeositePath, "geosite")
			writeAsset(t, fixture.download.GeoipPath, "geoip")
			runner := &fakeRunner{err: errors.New("private config /secret/config password=top-secret")}
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				runner.onRun = func(ctx context.Context, _ string, _, _ []string) error {
					cancel()
					return ctx.Err()
				}
			}
			validator := fixture.validator(t, runner)
			err := validator.Validate(ctx, fixture.download)
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v; want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "/secret/config") || strings.Contains(err.Error(), "top-secret") {
				t.Fatalf("validation error leaked command detail: %v", err)
			}
			if len(fixture.files.removed) != 1 {
				t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
			}
			if _, statErr := os.Stat(fixture.files.removed[0]); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("candidate remains after failure: %v", statErr)
			}
		})
	}
}

func TestValidateCleansCandidateWhenCanceledDuringAssetCopy(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[]}}`)
	writeAsset(t, fixture.download.GeositePath, "geosite")
	writeAsset(t, fixture.download.GeoipPath, "geoip")
	ctx, cancel := context.WithCancel(context.Background())
	fixture.files.onSource = func(path string) {
		if path == fixture.download.GeositePath {
			cancel()
		}
	}
	runner := &fakeRunner{}
	err := fixture.validator(t, runner).Validate(ctx, fixture.download)
	if !errors.Is(err, ErrValidationCanceled) {
		t.Fatalf("Validate() error = %v; want canceled validation", err)
	}
	if runner.called {
		t.Fatal("Xray ran after validation was canceled")
	}
	if len(fixture.files.removed) != 1 {
		t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
	}
	if _, statErr := os.Stat(fixture.files.removed[0]); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("candidate remains after cancellation: %v", statErr)
	}
}

func TestValidateRejectsSymlinkSources(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:userlist"]}]}}`)
	writeAsset(t, fixture.download.GeositePath, "geosite")
	writeAsset(t, fixture.download.GeoipPath, "geoip")
	target := filepath.Join(t.TempDir(), "outside.dat")
	writeAsset(t, target, "outside")
	if err := os.Symlink(target, filepath.Join(fixture.companionDir, "geosite_user.dat")); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	err := fixture.validator(t, runner).Validate(context.Background(), fixture.download)
	if !errors.Is(err, ErrCandidateSetup) {
		t.Fatalf("Validate() error = %v; want candidate setup failure", err)
	}
	if runner.called {
		t.Fatal("Xray ran with a symlinked source")
	}
	if len(fixture.files.removed) != 1 {
		t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
	}
}

func TestValidateRejectsInvalidDownloadPaths(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[]}}`)
	runner := &fakeRunner{}
	fixture.download.GeositePath = filepath.Join(fixture.root, "unexpected.dat")
	err := fixture.validator(t, runner).Validate(context.Background(), fixture.download)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Validate() error = %v; want invalid input", err)
	}
	if runner.called || len(fixture.files.removed) != 0 {
		t.Fatalf("runner called = %t, cleanup calls = %d", runner.called, len(fixture.files.removed))
	}

	fixture.download.Directory = fixture.config.Xray.AssetDir
	fixture.download.GeositePath = filepath.Join(fixture.download.Directory, "geosite.dat")
	fixture.download.GeoipPath = filepath.Join(fixture.download.Directory, "geoip.dat")
	err = fixture.validator(t, runner).Validate(context.Background(), fixture.download)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("live asset-directory result error = %v; want invalid input", err)
	}
}

func TestValidateAllowsMissingOptionalAndRejectsMissingRequiredCompanion(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		want   error
	}{
		{name: "missing optional companion", config: `{"routing":{"rules":[]}}`},
		{
			name:   "missing required companion",
			config: `{"routing":{"rules":[{"domain":["ext:geosite_user.dat:userlist"]}]}}`,
			want:   ErrMissingCompanionAsset,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, test.config)
			writeAsset(t, fixture.download.GeositePath, "geosite")
			writeAsset(t, fixture.download.GeoipPath, "geoip")
			runner := &fakeRunner{}
			if test.want == nil {
				runner.onRun = func(_ context.Context, _ string, _ []string, environment []string) error {
					if len(environment) != 1 {
						t.Fatalf("environment = %#v", environment)
					}
					candidateDir := strings.TrimPrefix(environment[0], "XRAY_LOCATION_ASSET=")
					entries, readErr := os.ReadDir(candidateDir)
					if readErr != nil {
						t.Fatal(readErr)
					}
					var names []string
					for _, entry := range entries {
						names = append(names, entry.Name())
					}
					if !reflect.DeepEqual(names, []string{"geoip.dat", "geosite.dat"}) {
						t.Errorf("optional-companion candidate files = %#v", names)
					}
					return nil
				}
			}
			err := fixture.validator(t, runner).Validate(context.Background(), fixture.download)
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v; want %v", err, test.want)
			}
			if runner.called != (test.want == nil) {
				t.Fatalf("runner called = %t; want %t", runner.called, test.want == nil)
			}
			if len(fixture.files.removed) != 1 {
				t.Fatalf("candidate cleanup calls = %d", len(fixture.files.removed))
			}
		})
	}
}

func TestValidateRedactsFilesystemAndUnsupportedReferenceErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		want   error
	}{
		{
			name:   "unsupported external path",
			config: `{"routing":{"rules":[{"domain":["ext:/secret/private.dat:tag"]}]}}`,
			want:   ErrUnsupportedAsset,
		},
		{
			name:   "asset read failure",
			config: `{"routing":{"rules":[]}}`,
			want:   ErrCandidateSetup,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, test.config)
			if test.name == "asset read failure" {
				if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), fixture.download.GeositePath); err != nil {
					t.Fatal(err)
				}
			} else {
				writeAsset(t, fixture.download.GeositePath, "geosite")
			}
			writeAsset(t, fixture.download.GeoipPath, "geoip")
			err := fixture.validator(t, &fakeRunner{}).Validate(context.Background(), fixture.download)
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v; want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "/secret") || strings.Contains(err.Error(), "private.dat") {
				t.Fatalf("validation error leaked path: %v", err)
			}
			if len(fixture.files.removed) != 0 && fixture.files.removed[0] != "" {
				if _, statErr := os.Stat(fixture.files.removed[0]); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("candidate remains after setup failure: %v", statErr)
				}
			}
		})
	}
}

func TestValidateReportsCleanupFailureWithoutLeakingPath(t *testing.T) {
	fixture := newFixture(t, `{"routing":{"rules":[]}}`)
	writeAsset(t, fixture.download.GeositePath, "geosite")
	writeAsset(t, fixture.download.GeoipPath, "geoip")
	fixture.files.removeErr = errors.New("remove /secret/candidate")
	err := fixture.validator(t, &fakeRunner{}).Validate(context.Background(), fixture.download)
	if !errors.Is(err, ErrCandidateCleanup) || strings.Contains(err.Error(), "/secret") {
		t.Fatalf("Validate() error = %v; want redacted cleanup error", err)
	}
}

func TestValidateCustomGeositeUsesFixedXrayBoundaryAndOverridesOnlyTargetAsset(t *testing.T) {
	for _, name := range []string{"geosite_user.dat", "geosite_custom-1.dat"} {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t, `{"routing":{"rules":[{"domain":["ext:`+name+`:userlist","ext:geoip.dat:private"]}]}}`)
			fixture.config.Geodata.UserGeosite = name
			for name, value := range map[string]string{
				name:        "old custom geosite",
				"geoip.dat": "existing geoip",
			} {
				writeAsset(t, filepath.Join(fixture.companionDir, name), value)
			}
			sourceDir := filepath.Join(fixture.companionDir, ".xray-core-helper-consolidation-pending")
			if err := os.Mkdir(sourceDir, 0700); err != nil {
				t.Fatal(err)
			}
			writeAsset(t, filepath.Join(sourceDir, name), "new custom geosite")
			source := filepath.Join(fixture.config.Xray.AssetDir, ".xray-core-helper-consolidation-pending", name)
			runner := &fakeRunner{onRun: func(_ context.Context, executable string, args, environment []string) error {
				if executable != fixture.config.Xray.Binary ||
					!reflect.DeepEqual(args, []string{"run", "-test", "-config", fixture.config.Xray.Config}) ||
					len(environment) != 1 || !strings.HasPrefix(environment[0], "XRAY_LOCATION_ASSET=") {
					t.Fatalf("Xray command = %#v, %#v, %#v", executable, args, environment)
				}
				candidateDir := strings.TrimPrefix(environment[0], "XRAY_LOCATION_ASSET=")
				for name, want := range map[string]string{
					name:        "new custom geosite",
					"geoip.dat": "existing geoip",
				} {
					content, err := os.ReadFile(filepath.Join(candidateDir, name))
					if err != nil || string(content) != want {
						t.Errorf("candidate %s = %q, %v; want %q", name, content, err, want)
					}
				}
				return nil
			}}
			validator := fixture.validator(t, runner)
			sum := sha256.Sum256([]byte("new custom geosite"))
			if err := validator.ValidateCustomGeosite(context.Background(), source, hex.EncodeToString(sum[:])); err != nil {
				t.Fatalf("ValidateCustomGeosite() error = %v", err)
			}
			if !runner.called {
				t.Fatal("Xray validator was not called")
			}
			live, err := os.ReadFile(filepath.Join(fixture.companionDir, name))
			if err != nil || string(live) != "old custom geosite" {
				t.Fatalf("live custom asset changed during validation: %q, %v", live, err)
			}
			otherHash := sha256.Sum256([]byte("different candidate"))
			if err := validator.ValidateCustomGeosite(context.Background(), source, hex.EncodeToString(otherHash[:])); !errors.Is(err, ErrCandidateChanged) {
				t.Fatalf("ValidateCustomGeosite() hash mismatch = %v; want candidate-change error", err)
			}
			if runner.calls != 1 {
				t.Fatalf("Xray ran %d times after candidate hash mismatch", runner.calls)
			}
		})
	}
}

type testFixture struct {
	root         string
	config       bootstrap.Config
	download     *geodatadownload.Result
	companionDir string
	files        *testFileSystem
}

func newFixture(t *testing.T, configBody string) *testFixture {
	t.Helper()
	root := t.TempDir()
	downloadDir := filepath.Join(root, "download")
	if err := os.Mkdir(downloadDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "xray.jsonc")
	if err := os.WriteFile(configPath, []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	companionDir := filepath.Join(root, "existing-assets")
	if err := os.Mkdir(companionDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := bootstrap.Defaults()
	config.Xray.Config = configPath
	files := &testFileSystem{
		managedRoot: config.Xray.AssetDir,
		backingRoot: companionDir,
	}
	return &testFixture{
		root:   root,
		config: config,
		download: &geodatadownload.Result{
			Directory:   downloadDir,
			GeositePath: filepath.Join(downloadDir, "geosite.dat"),
			GeoipPath:   filepath.Join(downloadDir, "geoip.dat"),
		},
		companionDir: companionDir,
		files:        files,
	}
}

func (fixture *testFixture) validator(t *testing.T, runner CommandRunner) *Validator {
	t.Helper()
	validator, err := New(fixture.config, Options{
		FileSystem: fixture.files,
		Runner:     runner,
		TempRoot:   fixture.root,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return validator
}

func writeAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredAssetLimitPreventsCandidateValidation(t *testing.T) {
	fixture := newFixture(t, `{}`)
	fixture.config.Geodata.MaxAssetSizeMB = 1
	writeAsset(t, fixture.download.GeositePath, "")
	if err := os.Truncate(fixture.download.GeositePath, (1<<20)+1); err != nil {
		t.Fatal(err)
	}
	writeAsset(t, fixture.download.GeoipPath, "candidate geoip")
	runner := &fakeRunner{}
	err := fixture.validator(t, runner).Validate(context.Background(), fixture.download)
	var size *geodatalimits.Exceeded
	if !errors.As(err, &size) || size.LimitBytes != 1<<20 || runner.called {
		t.Fatal("size limit not enforced before Xray", err, runner.called)
	}
}
