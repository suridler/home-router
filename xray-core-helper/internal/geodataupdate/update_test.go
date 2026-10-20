package geodataupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/consolidation"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodatainstall"
)

var errInjected = errors.New("injected failure")

type updateFixture struct {
	events               []string
	fail                 string
	alsoFail             map[string]bool
	failRecovery         bool
	foreignConsolidation bool
	foreignInstaller     bool
	installCleanupOnly   bool
	foreignState         string
	result               *geodatadownload.Result
	updater              *Updater
	restarts             int
}

func newUpdateFixture(t *testing.T, fail string) *updateFixture {
	t.Helper()
	fixture := &updateFixture{fail: fail, alsoFail: make(map[string]bool), result: &geodatadownload.Result{
		GeositePath: "/private/geosite.dat",
		GeoipPath:   "/private/geoip.dat",
	}}
	var err error
	fixture.updater, err = New(Options{
		Downloader:     downloaderFake{fixture},
		Validator:      validatorFake{fixture},
		Installer:      installerFake{fixture},
		Consolidation:  consolidationFake{fixture},
		Restarter:      restarterFake{fixture},
		HealthVerifier: healthFake{fixture},
		RuntimeOverlay: overlayFake{fixture},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *updateFixture) event(name string) error {
	fixture.events = append(fixture.events, name)
	if name == "restart" {
		fixture.restarts++
		if fixture.failRecovery && fixture.restarts == 2 {
			return errors.New("recovery restart failed")
		}
	}
	if fixture.fail == name || fixture.alsoFail[name] {
		return errInjected
	}
	return nil
}

type downloaderFake struct{ fixture *updateFixture }

func (fake downloaderFake) Download(context.Context, bootstrap.GeodataConfig) (*geodatadownload.Result, error) {
	err := fake.fixture.event("download")
	if err != nil {
		return fake.fixture.result, err
	}
	return fake.fixture.result, nil
}

type validatorFake struct{ fixture *updateFixture }

func (fake validatorFake) Validate(context.Context, *geodatadownload.Result) error {
	return fake.fixture.event("validate")
}

type installerFake struct{ fixture *updateFixture }

func (fake installerFake) InstallOwnedWhileLocked(context.Context, geodatainstall.Sources) (geodatainstall.Manifest, geodatainstall.InstallRecovery, error) {
	if fake.fixture.foreignInstaller {
		fake.fixture.events = append(fake.fixture.events, "install-foreign-refusal")
		return geodatainstall.Manifest{}, geodatainstall.NoInstallRecovery, geodatainstall.ErrPendingManifest
	}
	err := fake.fixture.event("install")
	if err != nil {
		if fake.fixture.installCleanupOnly {
			return geodatainstall.Manifest{}, geodatainstall.CleanupOnlyRecovery, err
		}
		if fake.fixture.alsoFail["install-owned"] {
			return geodatainstall.Manifest{}, geodatainstall.ManifestRecovery, err
		}
		return geodatainstall.Manifest{}, geodatainstall.NoInstallRecovery, err
	}
	return geodatainstall.Manifest{}, geodatainstall.ManifestRecovery, nil
}

func (fake installerFake) VerifyWhileLocked(context.Context) error {
	return fake.fixture.event("verify-standard")
}

func (fake installerFake) RollbackWhileLocked(context.Context) error {
	return fake.fixture.event("rollback-standard")
}

func (fake installerFake) CleanupOrphansWhileLocked(context.Context) error {
	return fake.fixture.event("cleanup-standard-orphans")
}

func (fake installerFake) CommitWhileLocked(context.Context) error {
	return fake.fixture.event("commit")
}

type consolidationFake struct{ fixture *updateFixture }

func (fake consolidationFake) PrepareWhileLocked(context.Context) error {
	if fake.fixture.foreignConsolidation {
		fake.fixture.events = append(fake.fixture.events, "prepare-foreign-refusal")
		return consolidation.ErrPending
	}
	return fake.fixture.event("prepare")
}

func (fake consolidationFake) RollbackWhileLocked(context.Context) error {
	return fake.fixture.event("rollback-custom")
}

func (fake consolidationFake) VerifyWhileLocked(context.Context) error {
	return fake.fixture.event("verify-custom")
}

func (fake consolidationFake) FinalizeWhileLocked(context.Context) error {
	return fake.fixture.event("finalize")
}

type restarterFake struct{ fixture *updateFixture }

func (fake restarterFake) RestartWhileLocked(context.Context) error {
	return fake.fixture.event("restart")
}

type healthFake struct{ fixture *updateFixture }

func (fake healthFake) VerifyActive(context.Context) error {
	return fake.fixture.event("health")
}

type overlayFake struct{ fixture *updateFixture }

func (fake overlayFake) ApplyWhileLocked(context.Context) error {
	return fake.fixture.event("overlay")
}

func TestUpdateSuccessOrderAndExactlyOnePlannedRestart(t *testing.T) {
	fixture := newUpdateFixture(t, "")
	if err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "download", "validate", "install", "restart", "health", "overlay",
		"verify-standard", "verify-custom", "finalize", "commit"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %v; want %v", fixture.events, want)
	}
	if fixture.restarts != 1 {
		t.Fatalf("restart count = %d; want one planned restart", fixture.restarts)
	}
}

func TestUpdateRollsBackAtEveryPreFinalizationFailure(t *testing.T) {
	tests := []struct {
		failure  string
		want     []string
		restarts int
	}{
		{"prepare", []string{"prepare"}, 0},
		{"download", []string{"prepare", "download", "rollback-custom"}, 0},
		{"validate", []string{"prepare", "download", "validate", "rollback-custom"}, 0},
		{"install", []string{"prepare", "download", "validate", "install", "rollback-custom"}, 0},
		{"restart", []string{"prepare", "download", "validate", "install", "restart", "rollback-standard", "rollback-custom", "restart"}, 2},
		{"health", []string{"prepare", "download", "validate", "install", "restart", "health", "rollback-standard", "rollback-custom", "restart"}, 2},
		{"overlay", []string{"prepare", "download", "validate", "install", "restart", "health", "overlay", "rollback-standard", "rollback-custom", "restart"}, 2},
		{"verify-standard", []string{"prepare", "download", "validate", "install", "restart", "health", "overlay", "verify-standard", "rollback-standard", "rollback-custom", "restart"}, 2},
		{"verify-custom", []string{"prepare", "download", "validate", "install", "restart", "health", "overlay", "verify-standard", "verify-custom", "rollback-standard", "rollback-custom", "restart"}, 2},
		{"finalize", []string{"prepare", "download", "validate", "install", "restart", "health", "overlay", "verify-standard", "verify-custom", "finalize", "rollback-standard", "rollback-custom", "restart"}, 2},
	}
	for _, test := range tests {
		t.Run(test.failure, func(t *testing.T) {
			fixture := newUpdateFixture(t, test.failure)
			err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
			if !errors.Is(err, errInjected) {
				t.Fatalf("Update() error = %v; want injected failure", err)
			}
			if !reflect.DeepEqual(fixture.events, test.want) {
				t.Fatalf("events = %v; want %v", fixture.events, test.want)
			}
			if fixture.restarts != test.restarts {
				t.Fatalf("restart count = %d; want %d", fixture.restarts, test.restarts)
			}
		})
	}
}

func TestUpdateJoinsRollbackAndRecoveryRestartFailures(t *testing.T) {
	fixture := newUpdateFixture(t, "health")
	fixture.failRecovery = true
	fixture.fail = "health"
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "recovery restart failed") {
		t.Fatalf("Update() error = %v; want primary and recovery failures joined", err)
	}
	if fixture.restarts != 2 {
		t.Fatalf("restart count = %d; want candidate and recovery restart", fixture.restarts)
	}
}

func TestUpdateRollsBackStandardOnlyWhenFailedInstallReportsOwnedRecovery(t *testing.T) {
	fixture := newUpdateFixture(t, "install")
	fixture.alsoFail["install-owned"] = true
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, errInjected) {
		t.Fatalf("Update() error = %v; want install error", err)
	}
	want := []string{"prepare", "download", "validate", "install", "rollback-standard", "rollback-custom"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %v; want owned standard recovery only: %v", fixture.events, want)
	}
}

func TestUpdateCleansOnlyOrphanEvidenceAfterInstallSelfRestored(t *testing.T) {
	fixture := newUpdateFixture(t, "install")
	fixture.installCleanupOnly = true
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, errInjected) {
		t.Fatalf("Update() error = %v; want install failure", err)
	}
	want := []string{"prepare", "download", "validate", "install", "cleanup-standard-orphans", "rollback-custom"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %v; want cleanup-only path without standard rollback: %v", fixture.events, want)
	}
}

func TestUpdateLeavesPreexistingCustomTransactionUntouched(t *testing.T) {
	fixture := newUpdateFixture(t, "")
	fixture.foreignConsolidation = true
	fixture.foreignState = "custom-pending-manifest; frozen=true"
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, consolidation.ErrPending) {
		t.Fatalf("Update() error = %v; want pre-existing consolidation refusal", err)
	}
	if !reflect.DeepEqual(fixture.events, []string{"prepare-foreign-refusal"}) ||
		fixture.restarts != 0 || fixture.foreignState != "custom-pending-manifest; frozen=true" {
		t.Fatalf("pre-existing custom state was touched: events=%v state=%q", fixture.events, fixture.foreignState)
	}
}

func TestUpdateLeavesPreexistingStandardRecoveryUntouched(t *testing.T) {
	fixture := newUpdateFixture(t, "")
	fixture.foreignInstaller = true
	fixture.foreignState = "standard-install-manifest-and-backups"
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, geodatainstall.ErrPendingManifest) {
		t.Fatalf("Update() error = %v; want pre-existing standard transaction refusal", err)
	}
	want := []string{"prepare", "download", "validate", "install-foreign-refusal", "rollback-custom"}
	if !reflect.DeepEqual(fixture.events, want) || fixture.restarts != 0 ||
		fixture.foreignState != "standard-install-manifest-and-backups" {
		t.Fatalf("pre-existing standard state was touched: events=%v state=%q", fixture.events, fixture.foreignState)
	}
}

func TestUpdateContinuesRecoveryAfterRollbackFailure(t *testing.T) {
	fixture := newUpdateFixture(t, "health")
	fixture.alsoFail["rollback-standard"] = true
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "rollback standard geodata") {
		t.Fatalf("Update() error = %v; want primary and rollback failures", err)
	}
	want := []string{"prepare", "download", "validate", "install", "restart", "health",
		"rollback-standard", "rollback-custom", "restart"}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %v; recovery stopped or reordered: want %v", fixture.events, want)
	}
}

func TestUpdateDoesNotRollbackAfterFinalizationWhenInstallerCommitFails(t *testing.T) {
	fixture := newUpdateFixture(t, "commit")
	err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{})
	if !errors.Is(err, errInjected) {
		t.Fatalf("Update() error = %v; want commit error", err)
	}
	if fixture.restarts != 1 || indexEvent(fixture.events, "rollback-standard") ||
		indexEvent(fixture.events, "rollback-custom") {
		t.Fatalf("post-finalization cleanup failure triggered rollback: events=%v", fixture.events)
	}
}

func TestUpdateDoesNotRollbackAfterFinalizationCommittedCleanupFailure(t *testing.T) {
	fixture := newUpdateFixture(t, "")
	fixture.updater.consol = committedFinalizer{fixture: fixture}
	if err := fixture.updater.Update(context.Background(), bootstrap.GeodataConfig{}); err == nil {
		t.Fatal("Update() succeeded after post-commit cleanup error")
	}
	if indexEvent(fixture.events, "rollback-standard") || indexEvent(fixture.events, "rollback-custom") ||
		fixture.restarts != 1 || !indexEvent(fixture.events, "commit") {
		t.Fatalf("post-commit error triggered rollback/restart: events=%v", fixture.events)
	}
}

func TestUpdateJoinsTemporaryCleanupFailureWithPrimaryFailure(t *testing.T) {
	payloads := map[string][]byte{
		"geosite.dat": bytes.Repeat([]byte("g"), 1100),
		"geoip.dat":   bytes.Repeat([]byte("i"), 1200),
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		name := filepath.Base(request.URL.Path)
		if strings.HasSuffix(name, ".sha256") {
			asset := strings.TrimSuffix(name, ".sha256")
			sum := sha256.Sum256(payloads[asset])
			_, _ = fmt.Fprintf(writer, "%x  %s\n", sum, asset)
			return
		}
		_, _ = writer.Write(payloads[name])
	}))
	defer server.Close()
	files := cleanupFailureFS{}
	downloader := geodatadownload.New(geodatadownload.Options{
		Transport:  server.Client().Transport,
		FileSystem: files,
		TempRoot:   t.TempDir(),
	})
	fixture := newUpdateFixture(t, "health")
	fixture.updater.downloader = downloader
	config := bootstrap.GeodataConfig{
		Origins: []string{server.URL},
		Geosite: bootstrap.GeodataSourceConfig{
			URL: server.URL + "/geosite.dat",
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: server.URL + "/geosite.dat.sha256", Algorithm: "sha256",
			},
		},
		Geoip: bootstrap.GeodataSourceConfig{
			URL: server.URL + "/geoip.dat",
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: server.URL + "/geoip.dat.sha256", Algorithm: "sha256",
			},
		},
	}
	err := fixture.updater.Update(context.Background(), config)
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "injected temporary cleanup failure") {
		t.Fatalf("Update() error = %v; want primary and cleanup failures", err)
	}
	if fixture.restarts != 2 || !indexEvent(fixture.events, "rollback-standard") ||
		!indexEvent(fixture.events, "rollback-custom") {
		t.Fatalf("cleanup error changed recovery sequence: events=%v", fixture.events)
	}
}

type cleanupFailureFS struct{}

func (cleanupFailureFS) MkdirTemp(root, pattern string) (string, error) {
	return os.MkdirTemp(root, pattern)
}

func (cleanupFailureFS) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

func (cleanupFailureFS) OpenFile(path string, flags int, mode os.FileMode) (geodatadownload.File, error) {
	return os.OpenFile(path, flags, mode)
}

func (cleanupFailureFS) RemoveAll(string) error {
	return errors.New("injected temporary cleanup failure")
}

type committedFinalizer struct{ fixture *updateFixture }

func (finalizer committedFinalizer) PrepareWhileLocked(context.Context) error {
	return finalizer.fixture.event("prepare")
}

func (finalizer committedFinalizer) RollbackWhileLocked(context.Context) error {
	return finalizer.fixture.event("rollback-custom")
}

func (finalizer committedFinalizer) VerifyWhileLocked(context.Context) error {
	return finalizer.fixture.event("verify-custom")
}

func (finalizer committedFinalizer) FinalizeWhileLocked(context.Context) error {
	if err := finalizer.fixture.event("finalize"); err != nil {
		return err
	}
	return &consolidation.FinalizationCommittedError{Err: errInjected}
}

func indexEvent(events []string, target string) bool {
	for _, event := range events {
		if event == target {
			return true
		}
	}
	return false
}
