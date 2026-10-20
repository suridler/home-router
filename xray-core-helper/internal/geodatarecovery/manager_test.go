package geodatarecovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/consolidation"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodatainstall"
	"home-router/xray-core-helper/internal/httpapi"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type recoveryFixture struct {
	standard *fakeStandardRecovery
	custom   *fakeCustomRecovery
	domains  *fakeRecoveryDomains
	manager  *Manager
	calls    []string
}

func newRecoveryFixture(t *testing.T, standard geodatainstall.Inspection, custom consolidation.Inspection, domains domainstore.Snapshot) *recoveryFixture {
	t.Helper()
	clearRecoveryGate(t)
	fixture := &recoveryFixture{
		standard: &fakeStandardRecovery{value: standard},
		domains:  &fakeRecoveryDomains{value: domains},
	}
	fixture.custom = &fakeCustomRecovery{value: custom, calls: &fixture.calls, domains: fixture.domains}
	fixture.standard.calls = &fixture.calls
	inspector, err := New(Options{
		Standard: fixture.standard, Custom: fixture.custom, Domains: fixture.domains,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager, err = NewManager(ManagerOptions{
		Inspector: inspector, Standard: fixture.standard, Custom: fixture.custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func clearRecoveryGate(t *testing.T) {
	t.Helper()
	coordinator.Block()
	lease, err := coordinator.AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Unblock(); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	t.Cleanup(func() {
		coordinator.Block()
		lease, err := coordinator.AcquireMaintenance(context.Background())
		if err == nil {
			_ = lease.Unblock()
			lease.Release()
		}
	})
}

func TestManagerMapsEveryRecoveryEnumToExactPrimitives(t *testing.T) {
	candidate := customManifest(consolidation.LiveGeositeCandidate, true, false)
	finalized := customManifest(consolidation.LiveGeositeCandidate, true, true)
	active := domainstore.Snapshot{Desired: []string{"wanted.example"}, ConsolidationActive: true}
	finalizedDomains := domainstore.Snapshot{Desired: []string{"wanted.example"}, Baseline: []string{"wanted.example"}}
	partialStandard := standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetMissing, false, false)
	cleanupStandard := geodatainstall.Inspection{Phase: geodatainstall.InspectionCleanupOnly}
	tests := []struct {
		name     string
		action   Recovery
		standard geodatainstall.Inspection
		custom   consolidation.Inspection
		domains  domainstore.Snapshot
		want     []string
	}{
		{"none", RecoveryNone, geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			consolidation.Inspection{Phase: consolidation.InspectionClean}, domainstore.Snapshot{}, nil},
		{"clear pre-manifest freeze", RecoveryClearCustomFreeze, geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			consolidation.Inspection{Phase: consolidation.InspectionPartial},
			domainstore.Snapshot{ConsolidationActive: true}, []string{"custom.clear-freeze"}},
		{"rollback standard", RecoveryRollbackStandard, partialStandard,
			consolidation.Inspection{Phase: consolidation.InspectionClean}, domainstore.Snapshot{}, []string{"standard.rollback"}},
		{"cleanup standard", RecoveryCleanupStandard, cleanupStandard,
			consolidation.Inspection{Phase: consolidation.InspectionClean}, domainstore.Snapshot{}, []string{"standard.cleanup"}},
		{"rollback mixed custom pair", RecoveryRollbackCustom, geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			customManifest(consolidation.LiveAssetsMixed, true, false), active, []string{"custom.rollback"}},
		{"rollback combined mixed pair", RecoveryRollbackBoth, partialStandard,
			customManifest(consolidation.LiveAssetsMixed, true, false), active, []string{"standard.rollback", "custom.rollback"}},
		{"rollback custom", RecoveryRollbackCustom, geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			candidate, active, []string{"custom.rollback"}},
		{"cleanup custom partial", RecoveryCleanupCustom, geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			consolidation.Inspection{Phase: consolidation.InspectionPartial}, domainstore.Snapshot{}, []string{"custom.cleanup-partial"}},
		{"cleanup custom finalized", RecoveryCleanupCustom, geodatainstall.Inspection{Phase: geodatainstall.InspectionClean},
			finalized, finalizedDomains, []string{"custom.finalize"}},
		{"rollback both", RecoveryRollbackBoth, partialStandard, candidate, active,
			[]string{"standard.rollback", "custom.rollback"}},
		{"cleanup both", RecoveryCleanupBoth, cleanupStandard, finalized, finalizedDomains,
			[]string{"standard.cleanup", "custom.finalize"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, test.standard, test.custom, test.domains)
			if test.action == RecoveryNone {
				if got := fixture.manager.Recover(context.Background()); got.Code != RecoveryNone.String() ||
					coordinator.RecoveryBlocked() {
					t.Fatalf("clean recovery status = %#v blocked=%t", got, coordinator.RecoveryBlocked())
				}
				return
			}
			status := fixture.manager.Recover(context.Background())
			if status.Code != RecoveryNone.String() || status.MutationsBlocked ||
				coordinator.RecoveryBlocked() {
				t.Fatalf("recovered status = %#v blocked=%t", status, coordinator.RecoveryBlocked())
			}
			if !reflect.DeepEqual(fixture.calls, test.want) {
				t.Fatalf("actions = %v; want %v", fixture.calls, test.want)
			}
		})
	}
}

func TestManagerAmbiguousPlanExecutesNothingAndKeepsGateBlocked(t *testing.T) {
	fixture := newRecoveryFixture(t,
		standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetInstalled, false, false),
		customManifest(consolidation.LiveGeositeCandidate, true, false),
		domainstore.Snapshot{Desired: []string{"wanted.example"}, ConsolidationActive: true})
	status := fixture.manager.Recover(context.Background())
	if status.Code != ActionChooseDirection || !status.MutationsBlocked ||
		status.Result.Plan.AutomaticRecovery || len(fixture.calls) != 0 ||
		!coordinator.RecoveryBlocked() {
		t.Fatalf("ambiguous recovery status = %#v; calls=%v blocked=%t",
			status, fixture.calls, coordinator.RecoveryBlocked())
	}
	if _, err := coordinator.Acquire(context.Background()); !errors.Is(err, coordinator.ErrRecoveryBlocked) {
		t.Fatalf("mutation acquisition error = %v", err)
	}
}

func TestManagerFailureAndReinspectionStayBlockedWithRedactedStatus(t *testing.T) {
	t.Run("execution failure", func(t *testing.T) {
		fixture := newRecoveryFixture(t,
			standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetMissing, false, false),
			consolidation.Inspection{Phase: consolidation.InspectionClean}, domainstore.Snapshot{})
		fixture.standard.actionErr = errors.New("/private/path secret-hash")
		status := fixture.manager.Recover(context.Background())
		assertBlockedRedacted(t, status, ActionRetryRecovery)
	})
	t.Run("reinspection failure", func(t *testing.T) {
		fixture := newRecoveryFixture(t,
			standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetMissing, false, false),
			consolidation.Inspection{Phase: consolidation.InspectionClean}, domainstore.Snapshot{})
		fixture.standard.failAfterFirst = true
		status := fixture.manager.Recover(context.Background())
		assertBlockedRedacted(t, status, ActionRetryInspection)
	})
}

func TestManagerRepeatedRecoverIsIdempotent(t *testing.T) {
	fixture := newRecoveryFixture(t,
		standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetMissing, false, false),
		consolidation.Inspection{Phase: consolidation.InspectionClean}, domainstore.Snapshot{})
	for range 2 {
		status := fixture.manager.Recover(context.Background())
		if status.Code != RecoveryNone.String() || coordinator.RecoveryBlocked() {
			t.Fatalf("Recover() = %#v blocked=%t", status, coordinator.RecoveryBlocked())
		}
	}
	if !reflect.DeepEqual(fixture.calls, []string{"standard.rollback"}) {
		t.Fatalf("recovery actions repeated: %v", fixture.calls)
	}
}

func TestOperatorRequiredStartupCanServeReadOnlyAndRejectMutations(t *testing.T) {
	fixture := newRecoveryFixture(t,
		standardManifest(geodatainstall.LiveAssetInstalled, geodatainstall.LiveAssetInstalled, false, false),
		customManifest(consolidation.LiveGeositeCandidate, true, false),
		domainstore.Snapshot{Desired: []string{"wanted.example"}, ConsolidationActive: true})
	if status := fixture.manager.Recover(context.Background()); status.Code != ActionChooseDirection {
		t.Fatalf("startup recovery status = %#v", status)
	}
	server := httptest.NewServer(httpapi.NewServer("test", xrayconfig.NewReader(xrayconfig.Settings{}),
		blockedConfigService{}, nil).Handler)
	defer server.Close()
	read, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatalf("read-only startup listener failed: %v", err)
	}
	_ = read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("health response = %d; want 200", read.StatusCode)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/xray/outbounds/vless",
		strings.NewReader(`{"expectedGeneration":1,"tag":"x","uri":"vless://id@example.com:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("mutation request failed: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("mutation response = %d; want 503", response.StatusCode)
	}
}

func assertBlockedRedacted(t *testing.T, status Status, code string) {
	t.Helper()
	if status.Code != code || !status.MutationsBlocked || !coordinator.RecoveryBlocked() {
		t.Fatalf("blocked status = %#v; gate=%t", status, coordinator.RecoveryBlocked())
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/path", "secret-hash", "raw-source-error"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status leaked %q: %s", secret, encoded)
		}
	}
}

type fakeStandardRecovery struct {
	value          geodatainstall.Inspection
	calls          *[]string
	actionErr      error
	failAfterFirst bool
	inspectCalls   int
}

func (fake *fakeStandardRecovery) Inspect(context.Context) (geodatainstall.Inspection, error) {
	fake.inspectCalls++
	if fake.failAfterFirst && fake.inspectCalls > 1 {
		return geodatainstall.Inspection{}, errors.New("raw-source-error")
	}
	return fake.value, nil
}

func (fake *fakeStandardRecovery) RollbackWhileLocked(context.Context) error {
	*fake.calls = append(*fake.calls, "standard.rollback")
	if fake.actionErr != nil {
		return fake.actionErr
	}
	fake.value = geodatainstall.Inspection{Phase: geodatainstall.InspectionClean}
	return nil
}

func (fake *fakeStandardRecovery) CleanupOrphansWhileLocked(context.Context) error {
	*fake.calls = append(*fake.calls, "standard.cleanup")
	if fake.actionErr != nil {
		return fake.actionErr
	}
	fake.value = geodatainstall.Inspection{Phase: geodatainstall.InspectionClean}
	return nil
}

type fakeCustomRecovery struct {
	value   consolidation.Inspection
	calls   *[]string
	domains *fakeRecoveryDomains
}

func (fake *fakeCustomRecovery) Inspect(context.Context) (consolidation.Inspection, error) {
	return fake.value, nil
}

func (fake *fakeCustomRecovery) record(name string) {
	*fake.calls = append(*fake.calls, name)
	fake.value = consolidation.Inspection{Phase: consolidation.InspectionClean}
	fake.domains.value.ConsolidationActive = false
}

func (fake *fakeCustomRecovery) RollbackWhileLocked(context.Context) error {
	fake.record("custom.rollback")
	return nil
}

func (fake *fakeCustomRecovery) FinalizeWhileLocked(context.Context) error {
	fake.record("custom.finalize")
	return nil
}

func (fake *fakeCustomRecovery) CleanupPartialWhileLocked(context.Context) error {
	fake.record("custom.cleanup-partial")
	return nil
}

func (fake *fakeCustomRecovery) RecoverIncompletePrepareWhileLocked(context.Context) error {
	fake.record("custom.clear-freeze")
	return nil
}

type fakeRecoveryDomains struct {
	value domainstore.Snapshot
}

type blockedConfigService struct{}

func (blockedConfigService) Adopt(context.Context, uint64) (transaction.Metadata, bool, error) {
	lease, err := coordinator.Acquire(context.Background())
	if err == nil {
		lease.Release()
	}
	return transaction.Metadata{}, false, err
}

func (blockedConfigService) Mutate(context.Context, uint64, transaction.Planner, bool) (transaction.Metadata, error) {
	lease, err := coordinator.Acquire(context.Background())
	if err == nil {
		lease.Release()
	}
	return transaction.Metadata{}, err
}

func (fake *fakeRecoveryDomains) Snapshot(context.Context) (domainstore.Snapshot, error) {
	return fake.value, nil
}
