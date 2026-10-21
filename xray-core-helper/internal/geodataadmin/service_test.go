package geodataadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/systemsettings"
)

type recoveryFake struct {
	status geodatarecovery.Status
}

func (fake recoveryFake) Status() geodatarecovery.Status { return fake.status }

type mutableRecoveryFake struct {
	status geodatarecovery.Status
}

func (fake *mutableRecoveryFake) Status() geodatarecovery.Status { return fake.status }

type clockFake struct {
	value time.Time
}

func (fake *clockFake) Now() time.Time { return fake.value }

func openFixture(t *testing.T, path string, initial bootstrap.GeodataConfig, recovery geodatarecovery.Status) (*Store, *Service) {
	t.Helper()
	resetCoordinatorForTest(t)
	t.Cleanup(func() { resetCoordinatorForTest(t) })
	store, err := Open(context.Background(), path, initial)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store: store, Settings: testTOML(t, path, initial), Recovery: recoveryFake{status: recovery},
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store, service
}

func cleanRecovery() geodatarecovery.Status {
	return geodatarecovery.Status{
		Code: geodatarecovery.RecoveryNone.String(),
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseClean},
			Plan:  geodatarecovery.Plan{Phase: geodatarecovery.PhaseClean},
		},
	}
}

func configuredSettings() Settings {
	return Settings{
		MaxAssetSizeMB: 256, Builder: "/private/path/to/builder", UserGeosite: bootstrap.DefaultUserGeositeName, UserGeoip: bootstrap.DefaultUserGeoipName, Schedule: &api.GeodataSchedule{Enabled: true, Time: "04:00"},
		Origins: []string{"https://data.example"},
		Geosite: SourceSettings{URL: "https://data.example/files/geosite.dat",
			Checksum: ChecksumSettings{URL: "https://data.example/checksums/geosite.sha256", Algorithm: "sha256"}},
		Geoip: SourceSettings{URL: "https://data.example/files/geoip.dat",
			Checksum: ChecksumSettings{URL: "https://data.example/checksums/geoip.sha256", Algorithm: "sha256"}},
	}
}

func bootstrapSettings(settings Settings) bootstrap.GeodataConfig {
	return bootstrap.GeodataConfig{
		MaxAssetSizeMB: 256, UserGeosite: bootstrap.DefaultUserGeositeName, UserGeoip: bootstrap.DefaultUserGeoipName,
		Origins: settings.Origins,
		Builder: "/private/path/to/builder",
		Geosite: bootstrap.GeodataSourceConfig{
			URL: settings.Geosite.URL,
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: settings.Geosite.Checksum.URL, Algorithm: settings.Geosite.Checksum.Algorithm,
			},
		},
		Geoip: bootstrap.GeodataSourceConfig{
			URL: settings.Geoip.URL,
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: settings.Geoip.Checksum.URL, Algorithm: settings.Geoip.Checksum.Algorithm,
			},
		},
	}
}

func TestOpenSeedsValidatedBootstrapOnceAndPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	firstSettings := configuredSettings()
	store, service := openFixture(t, path, bootstrapSettings(firstSettings), cleanRecovery())
	got, err := service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Settings, firstSettings) {
		t.Fatalf("initial Settings() = %#v; want generation 0 and bootstrap settings", got)
	}
	replacement := configuredSettings()
	replacement.Origins = []string{"https://new-data.example"}
	replacement.Geosite.URL = "https://new-data.example/custom/geosite.dat"
	replacement.Geosite.Checksum.URL = "https://new-data.example/custom/geosite.sha256"
	replacement.Geoip.URL = "https://new-data.example/custom/geoip.dat"
	replacement.Geoip.Checksum.URL = "https://new-data.example/custom/geoip.sha256"
	if _, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), replacement); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	laterBootstrap := configuredSettings()
	laterBootstrap.Origins = []string{"https://restart.example"}
	laterBootstrap.Geosite.URL = "https://restart.example/geosite.dat"
	laterBootstrap.Geosite.Checksum.URL = "https://restart.example/geosite.sha256"
	laterBootstrap.Geoip.URL = "https://restart.example/geoip.dat"
	laterBootstrap.Geoip.Checksum.URL = "https://restart.example/geoip.sha256"
	reopened, service := openFixture(t, path, bootstrapSettings(laterBootstrap), cleanRecovery())
	t.Cleanup(func() { _ = reopened.Close() })
	got, err = service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Settings, replacement) {
		t.Fatalf("reopened Settings() = %#v; persisted API settings were overwritten", got)
	}
}

func TestSettingsValidationAndGenerationConflict(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })

	invalid := configuredSettings()
	invalid.Origins = []string{"https://data.example", "https://DATA.example:443"}
	if _, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), invalid); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("duplicate-origin replacement error = %v; want invalid settings", err)
	}
	invalid = configuredSettings()
	invalid.Geoip.Checksum.URL = ""
	if _, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), invalid); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("incomplete-source replacement error = %v; want invalid settings", err)
	}
	if _, err := service.ReplaceSettings(context.Background(), 1, configuredSettings()); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale replacement error = %v; want generation conflict", err)
	}
	updated, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), configuredSettings())
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != currentGeneration(t, service) {
		t.Fatalf("replacement generation = %d; want 1", updated.Generation)
	}
}

func TestBeginUpdateValidatesKeysReplaysAndRejectsPayloadConflict(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	for _, key := range []string{"", "  ", "has space", strings.Repeat("x", 129)} {
		if _, _, err := service.BeginUpdate(context.Background(), key); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("BeginUpdate(%q) error = %v; want invalid key", key, err)
		}
	}

	first, replayed, err := service.BeginUpdate(context.Background(), "request-1")
	if err != nil || replayed || first.State != OperationRunning {
		t.Fatalf("first BeginUpdate() = %#v, replayed=%t, err=%v", first, replayed, err)
	}
	replay, replayed, err := service.BeginUpdate(context.Background(), "request-1")
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatalf("replay BeginUpdate() = %#v, replayed=%t, err=%v", replay, replayed, err)
	}
	if _, _, err := service.BeginUpdate(context.Background(), "request-2"); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("parallel key error = %v; want in-progress conflict", err)
	}
	changed := configuredSettings()
	changed.MaxAssetSizeMB = 512
	if _, err := service.ReplaceSettings(context.Background(), currentGeneration(t, service), changed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.BeginUpdate(context.Background(), "request-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed-settings replay error = %v; want idempotency conflict", err)
	}
}

func TestConcurrentBeginUpdateAdmitsOnlyOneOperation(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	const requests = 24
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan error, requests)
	created := make(chan Operation, requests)
	for i := 0; i < requests; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			op, _, err := service.BeginUpdate(context.Background(), fmt.Sprintf("parallel-%d", i))
			if err == nil {
				created <- op
			} else {
				results <- err
			}
		}(i)
	}
	close(start)
	wait.Wait()
	close(results)
	close(created)
	var admitted int
	for range created {
		admitted++
	}
	var conflicts int
	for err := range results {
		if errors.Is(err, ErrOperationInProgress) {
			conflicts++
		} else {
			t.Errorf("concurrent BeginUpdate() error = %v", err)
		}
	}
	if admitted != 1 || conflicts != requests-1 {
		t.Fatalf("admitted=%d conflicts=%d; want one admission and %d conflicts", admitted, conflicts, requests-1)
	}
}

func TestRecoveryBlocksNewOperationButStatusRemainsRedacted(t *testing.T) {
	recovery := geodatarecovery.Status{
		Code: "secret /private/path raw-error",
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseInconsistent},
		},
		MutationsBlocked: true,
	}
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), recovery)
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := service.BeginUpdate(context.Background(), "blocked"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("BeginUpdate() error = %v; want recovery-required refusal", err)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret", "raw-error", "request_fingerprint", "idempotency_key"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status contains %q: %s", secret, encoded)
		}
	}
	if status.Recovery.Code != geodatarecovery.ActionRetryInspection || !status.Recovery.MutationsBlocked {
		t.Fatalf("unrecognized recovery status was not safely projected: %#v", status.Recovery)
	}
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM geodata_update_operation").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("recovery refusal persisted %d operations", count)
	}
}

func TestStatusReadsLiveRecoveryProvider(t *testing.T) {
	resetCoordinatorForTest(t)
	t.Cleanup(func() { resetCoordinatorForTest(t) })
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"), bootstrapSettings(configuredSettings()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recovery := &mutableRecoveryFake{status: cleanRecovery()}
	service, err := NewService(ServiceOptions{Store: store, Settings: testTOML(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings())), Recovery: recovery})
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Recovery.Code != geodatarecovery.RecoveryNone.String() || before.Recovery.MutationsBlocked {
		t.Fatalf("initial recovery projection = %#v", before.Recovery)
	}

	recovery.status = geodatarecovery.Status{
		Code: geodatarecovery.ActionRetryInspection, MutationsBlocked: true,
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseInspectionFailed},
		},
	}
	after, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Recovery.Code != geodatarecovery.ActionRetryInspection ||
		after.Recovery.Phase != geodatarecovery.PhaseInspectionFailed.String() ||
		!after.Recovery.MutationsBlocked {
		t.Fatalf("updated recovery projection = %#v; want current provider status", after.Recovery)
	}
	if _, err := service.ReplaceSettings(context.Background(), after.Settings.Generation, configuredSettings()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("settings mutation error = %v; want fail-closed recovery refusal", err)
	}
}

func TestFinishMethodsAreOperationIDCompareAndSet(t *testing.T) {
	tests := []struct {
		name   string
		finish func(*Service, context.Context, string) error
		state  OperationState
		code   string
	}{
		{"success", (*Service).FinishSuccess, OperationSucceeded, "geodata_update_succeeded"},
		{"failure", func(s *Service, ctx context.Context, id string) error {
			return s.FinishFailure(ctx, id, FailureChecksumFailed)
		}, OperationFailed, string(FailureChecksumFailed)},
		{"recovery", (*Service).FinishRecoveryRequired, OperationRecoveryRequired, "recovery_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
				bootstrapSettings(configuredSettings()), cleanRecovery())
			t.Cleanup(func() { _ = store.Close() })
			op, _, err := service.BeginUpdate(context.Background(), "finish")
			if err != nil {
				t.Fatal(err)
			}
			if err := test.finish(service, context.Background(), strings.Repeat("0", 32)); !errors.Is(err, ErrStaleOperation) {
				t.Fatalf("stale finish error = %v; want stale operation", err)
			}
			if err := test.finish(service, context.Background(), op.ID); err != nil {
				t.Fatal(err)
			}
			if err := service.FinishSuccess(context.Background(), op.ID); !errors.Is(err, ErrStaleOperation) {
				t.Fatalf("second finish error = %v; want stale operation", err)
			}
			status, err := service.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.Operation == nil || status.Operation.State != test.state {
				t.Fatalf("finished operation = %#v; want %q", status.Operation, test.state)
			}
			code := status.Operation.ResultCode
			if code == "" {
				code = status.Operation.ErrorCode
			}
			if code != test.code || status.Operation.FinishedAt == nil {
				t.Fatalf("finished operation code/time = %q/%v; want %q and timestamp", code, status.Operation.FinishedAt, test.code)
			}
		})
	}
}

func TestInvalidFinishCodeAndCancellationDoNotMutate(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	op, _, err := service.BeginUpdate(context.Background(), "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FinishFailure(context.Background(), op.ID, FailureCode("raw private error")); !errors.Is(err, ErrInvalidOperationCode) {
		t.Fatalf("unknown finish code error = %v; want invalid-code refusal", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReplaceSettings(ctx, 0, configuredSettings()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled replacement error = %v; want context cancellation", err)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status.Settings.Settings, configuredSettings()) || status.Operation.State != OperationRunning {
		t.Fatalf("canceled/invalid operation changed persisted state: %#v", status)
	}
}

func TestSettingsReplacementRejectsConcurrentManualEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	store, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	before := currentGeneration(t, service)
	cfg, err := bootstrap.Load(filepath.Join(filepath.Dir(path), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Geodata.MaxAssetSizeMB = 512
	raw, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "config.toml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceSettings(context.Background(), before, configuredSettings()); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale save=%v", err)
	}
	current, err := service.Settings(context.Background())
	if err != nil || current.Settings.MaxAssetSizeMB != 512 {
		t.Fatal("manual edit lost", err)
	}
}

func TestSettingsAndStatusExposeSettingsButNotSecrets(t *testing.T) {
	config := bootstrapSettings(configuredSettings())
	config.Builder = "/secret/builder/path"
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"), config, cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fingerprint", "checksum contents", "contentHash", "raw error"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("status contains forbidden %q: %s", forbidden, encoded)
		}
	}
	if status.Settings.Settings.Geosite.URL != configuredSettings().Geosite.URL {
		t.Fatalf("status did not project validated source URL: %#v", status.Settings)
	}
}

func TestSQLiteSchemaCoexistsWithDomainStoreAuditAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	admin, _ := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	domains, err := domainstore.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("domain-store schema audit rejected geodata administration tables: %v", err)
	}
	if err := domains.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	defer reopened.Close()
	got, err := service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Settings, configuredSettings()) {
		t.Fatalf("settings changed across SQLite reopen/audit: %#v", got)
	}
}

func TestOpenRejectsInvalidBootstrapAndRelativeDatabasePath(t *testing.T) {
	bad := bootstrapSettings(configuredSettings())
	bad.Geosite.URL = "https://untrusted.example/geosite.dat"
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "helper.sqlite"), bad); err == nil {
		t.Fatal("Open() accepted bootstrap settings with an unconfigured URL origin")
	}
	if _, err := Open(context.Background(), "relative.sqlite", bootstrap.Defaults().Geodata); err == nil {
		t.Fatal("Open() accepted a relative database path")
	}
}

func TestOperationRecoveryCompletionAndSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper.sqlite")
	store, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	op, _, err := service.BeginUpdate(context.Background(), "recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FinishRecoveryRequired(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, service := openFixture(t, path, bootstrapSettings(configuredSettings()), cleanRecovery())
	defer reopened.Close()
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operation == nil || status.Operation.ID != op.ID || status.Operation.State != OperationRecoveryRequired {
		t.Fatalf("reopened operation = %#v", status.Operation)
	}
}

func TestStaleCompletionCannotOverwriteNewerOperation(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	first, _, err := service.BeginUpdate(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FinishSuccess(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	second, replayed, err := service.BeginUpdate(context.Background(), "second")
	if err != nil || replayed {
		t.Fatalf("second BeginUpdate() = %#v replayed=%t err=%v", second, replayed, err)
	}
	if err := service.FinishFailure(context.Background(), first.ID, FailureUpdateFailed); !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("stale completion error = %v; want stale operation", err)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operation == nil || status.Operation.ID != second.ID || status.Operation.State != OperationRunning {
		t.Fatalf("stale completion overwrote newer operation: %#v", status.Operation)
	}
}

func TestSQLTablesContainStrictTypedColumnsWithoutJSONBlob(t *testing.T) {
	store, _ := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	for _, table := range []string{operationTable} {
		var schema string
		if err := store.db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&schema); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(schema), "json") || !strings.Contains(schema, "CHECK") {
			t.Fatalf("%s schema is not normalized/constraint-checked: %s", table, schema)
		}
	}
	var operationRows int
	if err := store.db.QueryRow("SELECT count(*) FROM geodata_update_operation").Scan(&operationRows); err != nil {
		t.Fatal(err)
	}
	if operationRows != 0 {
		t.Fatalf("initialization created %d update operations", operationRows)
	}
}

func TestOperationRecordDoesNotReturnIdempotencyKeyOrFingerprint(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"),
		bootstrapSettings(configuredSettings()), cleanRecovery())
	t.Cleanup(func() { _ = store.Close() })
	op, _, err := service.BeginUpdate(context.Background(), "request-secret")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "request-secret") || strings.Contains(string(encoded), "fingerprint") {
		t.Fatalf("operation projection leaks internal idempotency data: %s", encoded)
	}
}

func TestSettingsGenerationIdentifiesEffectiveValues(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "helper.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	before := currentGeneration(t, service)
	for i := 0; i < 2; i++ {
		result, err := service.ReplaceSettings(context.Background(), before, configuredSettings())
		if err != nil || result.Generation != before {
			t.Fatalf("unchanged save=%+v %v", result, err)
		}
	}
}

func TestCancelledStoreOpenDoesNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancelled.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if store, err := Open(ctx, path, bootstrap.Defaults().Geodata); err == nil {
		_ = store.Close()
		t.Fatal("Open() succeeded with a canceled context")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Open() created database file: stat error=%v", err)
	}
}

func currentGeneration(t *testing.T, s *Service) uint64 {
	t.Helper()
	settings, err := s.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return settings.Generation
}
func testTOML(t *testing.T, path string, geo bootstrap.GeodataConfig) *systemsettings.Service {
	t.Helper()
	cfg := bootstrap.Defaults()
	if geo.MaxAssetSizeMB == 0 {
		geo.MaxAssetSizeMB = 256
	}
	if geo.UserGeosite == "" {
		geo.UserGeosite = bootstrap.DefaultUserGeositeName
	}
	if geo.UserGeoip == "" {
		geo.UserGeoip = bootstrap.DefaultUserGeoipName
	}
	cfg.Geodata = geo
	name := filepath.Join(filepath.Dir(path), "config.toml")
	if _, err := os.Stat(name); errors.Is(err, os.ErrNotExist) {
		raw, err := toml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	disk, err := bootstrap.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := systemsettings.New(name, disk)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestBuilderPathCanOnlyChangeInTOML(t *testing.T) {
	store, service := openFixture(t, filepath.Join(t.TempDir(), "state.sqlite"), bootstrapSettings(configuredSettings()), cleanRecovery())
	defer store.Close()
	settings, err := service.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attempted := settings.Settings
	attempted.Builder = "/arbitrary/executable"
	if _, err := service.ReplaceSettings(context.Background(), settings.Generation, attempted); !errors.Is(err, ErrInvalidSettings) {
		t.Fatal("API changed executable", err)
	}
	after, err := service.Settings(context.Background())
	if err != nil || after.Settings.Builder != settings.Settings.Builder {
		t.Fatal("settings changed", err)
	}
}
