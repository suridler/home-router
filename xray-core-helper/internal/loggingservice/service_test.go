package loggingservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/logfileprep"
	"home-router/xray-core-helper/internal/logrotation"
	"home-router/xray-core-helper/internal/transaction"
)

const loggingFixture = `{
  // keep logging comment
  "log": {
    "access": "",
    "error": "",
    "loglevel": "warning",
    "dnsLog": false,
    "unknownLogOption": true
  },
  "unknown": { "retained": true }
}`

type eventLog struct {
	events []string
}

type fakeValidator struct {
	events *eventLog
	err    error
	calls  int
}

func (validator *fakeValidator) Validate(context.Context, string) error {
	validator.events.events = append(validator.events.events, "config-validate")
	validator.calls++
	return validator.err
}

type fakeRestarter struct {
	cancel context.CancelFunc
	events *eventLog
	err    error
	calls  int
}

func (restarter *fakeRestarter) Restart(context.Context) error {
	restarter.events.events = append(restarter.events.events, "restart")
	restarter.calls++
	if restarter.calls == 1 {
		if restarter.cancel != nil {
			restarter.cancel()
		}
		return restarter.err
	}
	return nil
}

type fakePreparer struct {
	last   api.LogConfigurationRequest
	events *eventLog
	err    error
	snap   *fakeSnapshot
	calls  int
}

func (preparer *fakePreparer) Prepare(_ context.Context, request api.LogConfigurationRequest) (logfileprep.Snapshot, error) {
	preparer.last = request
	preparer.events.events = append(preparer.events.events, "prepare")
	preparer.calls++
	if preparer.err != nil {
		return nil, preparer.err
	}
	return preparer.snap, nil
}

type fakeSnapshot struct {
	events      *eventLog
	commitErr   error
	rollbackErr error
}

type fakeRotationPreparer struct {
	events *eventLog
	snap   logrotation.Snapshot
	err    error
}

func (preparer *fakeRotationPreparer) Prepare(context.Context, api.LogConfigurationRequest) (logrotation.Snapshot, error) {
	preparer.events.events = append(preparer.events.events, "rotation-prepare")
	return preparer.snap, preparer.err
}

type fakeRotationSnapshot struct {
	events      *eventLog
	rollbackErr error
}

type noOpRotationPreparer struct{}

func (noOpRotationPreparer) Prepare(context.Context, api.LogConfigurationRequest) (logrotation.Snapshot, error) {
	return noOpRotationSnapshot{}, nil
}

type noOpRotationSnapshot struct{}

func (noOpRotationSnapshot) Commit() error   { return nil }
func (noOpRotationSnapshot) Rollback() error { return nil }

func (snapshot *fakeRotationSnapshot) Commit() error {
	snapshot.events.events = append(snapshot.events.events, "rotation-commit")
	return nil
}

func (snapshot *fakeRotationSnapshot) Rollback() error {
	snapshot.events.events = append(snapshot.events.events, "rotation-rollback")
	return snapshot.rollbackErr
}

func (snapshot *fakeSnapshot) Commit() error {
	snapshot.events.events = append(snapshot.events.events, "commit")
	return snapshot.commitErr
}

func (snapshot *fakeSnapshot) Rollback() error {
	snapshot.events.events = append(snapshot.events.events, "rollback")
	return snapshot.rollbackErr
}

func TestUpdateValidatesBeforePreparationAndRejectsStaleGeneration(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	preparer := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
	service, _, _ := newService(t, loggingFixture, events, preparer, nil, nil)

	invalid := validRequest(0, false)
	invalid.Loglevel = "secret-invalid-level"
	_, err := service.Update(context.Background(), invalid)
	if code, _, _ := transaction.ErrorInfo(err); code != "invalid_loglevel" {
		t.Fatalf("invalid request error code = %q; want invalid_loglevel (%v)", code, err)
	}
	if preparer.calls != 0 || len(events.events) != 0 {
		t.Fatalf("invalid request caused side effects: calls=%d events=%v", preparer.calls, events.events)
	}

	missingGeneration := validRequest(0, false)
	missingGeneration.ExpectedGeneration = nil
	_, err = service.Update(context.Background(), missingGeneration)
	if code, _, _ := transaction.ErrorInfo(err); code != "invalid_request" {
		t.Fatalf("missing generation error code = %q; want invalid_request (%v)", code, err)
	}
	if preparer.calls != 0 || len(events.events) != 0 {
		t.Fatalf("missing generation caused side effects: calls=%d events=%v", preparer.calls, events.events)
	}

	stale := validRequest(8, false)
	_, err = service.Update(context.Background(), stale)
	if !errors.Is(err, transaction.ErrStaleGeneration) {
		t.Fatalf("stale request error = %v; want ErrStaleGeneration", err)
	}
	if preparer.calls != 0 || len(events.events) != 0 {
		t.Fatalf("stale request caused side effects: calls=%d events=%v", preparer.calls, events.events)
	}
}

func TestUpdatePlansBeforePreparationAndRollsBackOnXrayValidationFailure(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		validator  error
		wantEvents []string
	}{
		{
			name:       "malformed managed log object",
			config:     `{"log":{"access":false}}`,
			wantEvents: nil,
		},
		{
			name:       "Xray rejects candidate",
			config:     loggingFixture,
			validator:  errors.New("secret-config-validation-output"),
			wantEvents: []string{"prepare", "config-validate", "rollback"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetMutationGate(t)
			events := &eventLog{}
			preparer := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
			service, validator, configPath := newService(t, test.config, events, preparer, test.validator, nil)
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Update(context.Background(), validRequest(0, false))
			if err == nil {
				t.Fatal("Update() unexpectedly succeeded")
			}
			if got := events.events; !reflect.DeepEqual(got, test.wantEvents) {
				t.Fatalf("phase order = %v; want %v", got, test.wantEvents)
			}
			after, readErr := os.ReadFile(configPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("configuration changed after failure: err=%v", readErr)
			}
			if validator.calls != boolInt(test.validator != nil) {
				t.Fatalf("Xray validator calls = %d", validator.calls)
			}
			if strings.Contains(err.Error(), "secret-config-validation-output") {
				t.Fatalf("error exposed config validation output: %v", err)
			}
		})
	}
}

func TestUpdateRollsBackPreparedFilesWhenRestartFails(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	preparer := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
	service, _, configPath := newService(t, loggingFixture, events, preparer, nil, errors.New("secret-systemd-output"))
	before, _ := os.ReadFile(configPath)

	_, err := service.Update(context.Background(), validRequest(0, true))
	if !errors.Is(err, transaction.ErrRestartFailed) {
		t.Fatalf("Update() error = %v; want ErrRestartFailed", err)
	}
	wantEvents := []string{"prepare", "config-validate", "restart", "restart", "rollback"}
	if !reflect.DeepEqual(events.events, wantEvents) {
		t.Fatalf("phase order = %v; want %v", events.events, wantEvents)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("configuration not restored after failed restart: read error=%v", readErr)
	}
	if strings.Contains(err.Error(), "secret-systemd-output") {
		t.Fatalf("error exposed service-control output: %v", err)
	}
}

func TestUpdatePrepareFailureIsRedactedAndDoesNotRunTransaction(t *testing.T) {
	resetMutationGate(t)
	const secret = "secret-filesystem-detail"
	events := &eventLog{}
	preparer := &fakePreparer{events: events, err: errors.New(secret)}
	service, validator, _ := newService(t, loggingFixture, events, preparer, nil, nil)

	_, err := service.Update(context.Background(), validRequest(0, true))
	if code, message, status := transaction.ErrorInfo(err); code != "log_preparation_failed" ||
		status != 503 || strings.Contains(message, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("prepare failure = %q %q %d (%v)", code, message, status, err)
	}
	if !reflect.DeepEqual(events.events, []string{"prepare"}) || validator.calls != 0 {
		t.Fatalf("prepare failure advanced phases: events=%v validator calls=%d", events.events, validator.calls)
	}
}

func TestUpdateBlocksMutationsWhenPreparationRollbackFails(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	preparer := &fakePreparer{
		events: events,
		snap: &fakeSnapshot{
			events: events, rollbackErr: errors.New("secret-rollback-detail"),
		},
	}
	service, _, _ := newService(t, loggingFixture, events, preparer, errors.New("reject candidate"), nil)

	_, err := service.Update(context.Background(), validRequest(0, false))
	if code, message, status := transaction.ErrorInfo(err); code != "recovery_required" ||
		status != 503 || strings.Contains(message, "secret-rollback-detail") {
		t.Fatalf("rollback failure = %q %q %d (%v)", code, message, status, err)
	}
	if !coordinator.RecoveryBlocked() {
		t.Fatal("preparation rollback failure did not block mutations")
	}
	beforeCalls := preparer.calls
	_, err = service.Update(context.Background(), validRequest(0, false))
	if !errors.Is(err, coordinator.ErrRecoveryBlocked) {
		t.Fatalf("mutation after rollback failure = %v; want recovery gate rejection", err)
	}
	if preparer.calls != beforeCalls {
		t.Fatal("blocked mutation reached preparation")
	}
}

func TestUpdatePrepareRollbackFailureBlocksMutationAdmission(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	preparer := &fakePreparer{
		events: events,
		err:    errors.Join(logfileprep.ErrPreparation, logfileprep.ErrRollbackFailed),
	}
	service, _, _ := newService(t, loggingFixture, events, preparer, nil, nil)

	_, err := service.Update(context.Background(), validRequest(0, false))
	if code, _, _ := transaction.ErrorInfo(err); code != "recovery_required" {
		t.Fatalf("partial prepare rollback error code = %q (%v); want recovery_required", code, err)
	}
	if !coordinator.RecoveryBlocked() {
		t.Fatal("partial prepare rollback failure did not block mutations")
	}
}

func TestUpdateCommitsSuccessfulAndIdempotentPreparations(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	preparer := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
	service, validator, configPath := newService(t, loggingFixture, events, preparer, nil, nil)
	request := validRequest(0, true)

	first, err := service.Update(context.Background(), request)
	if err != nil {
		t.Fatalf("first Update() error = %v", err)
	}
	if first.Generation != 1 {
		t.Fatalf("first generation = %d; want 1", first.Generation)
	}
	wantFirst := []string{"prepare", "config-validate", "restart", "commit"}
	if !reflect.DeepEqual(events.events, wantFirst) {
		t.Fatalf("first phase order = %v; want %v", events.events, wantFirst)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{
		"// keep logging comment", `"unknownLogOption": true`, `"unknown": { "retained": true }`,
		`"/var/log/xray/access.log"`, `"/var/log/xray/error.log"`,
	} {
		if !strings.Contains(string(updated), retained) {
			t.Errorf("updated config lost %q: %s", retained, updated)
		}
	}

	events.events = nil
	replay := validRequest(1, true)
	second, err := service.Update(context.Background(), replay)
	if err != nil {
		t.Fatalf("idempotent Update() error = %v", err)
	}
	if second.Generation != first.Generation {
		t.Fatalf("idempotent generation = %d; want %d", second.Generation, first.Generation)
	}
	if !reflect.DeepEqual(events.events, []string{"prepare", "commit"}) {
		t.Fatalf("no-op phase order = %v; want preparation commit without Xray validation or restart", events.events)
	}
	if validator.calls != 1 {
		t.Fatalf("validator calls after idempotent update = %d; want 1", validator.calls)
	}
}

func TestUpdateSnapshotCommitFailureRollsBackAndBlocks(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	preparer := &fakePreparer{
		events: events,
		snap:   &fakeSnapshot{events: events, commitErr: errors.New("secret-close-detail")},
	}
	service, _, _ := newService(t, loggingFixture, events, preparer, nil, nil)

	_, err := service.Update(context.Background(), validRequest(0, false))
	if code, message, status := transaction.ErrorInfo(err); code != "recovery_required" ||
		status != 503 || strings.Contains(message, "secret-close-detail") || strings.Contains(err.Error(), "secret-close-detail") {
		t.Fatalf("snapshot commit failure = %q %q %d (%v)", code, message, status, err)
	}
	if !reflect.DeepEqual(events.events, []string{"prepare", "config-validate", "commit", "rollback"}) {
		t.Fatalf("phase order = %v", events.events)
	}
	if !coordinator.RecoveryBlocked() {
		t.Fatal("snapshot commit failure did not block mutations")
	}
}

func TestUpdateComposesRotationWithFilePreparation(t *testing.T) {
	for _, test := range []struct {
		name       string
		validation error
		wantEvents []string
	}{
		{
			name: "commit both preparations",
			wantEvents: []string{
				"prepare", "rotation-prepare", "config-validate", "rotation-commit", "commit",
			},
		},
		{
			name:       "rollback both preparations",
			validation: errors.New("reject candidate"),
			wantEvents: []string{
				"prepare", "rotation-prepare", "config-validate", "rotation-rollback", "rollback",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetMutationGate(t)
			events := &eventLog{}
			files := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
			service, _, _ := newService(t, loggingFixture, events, files, test.validation, nil)
			service.rotation = &fakeRotationPreparer{
				events: events, snap: &fakeRotationSnapshot{events: events},
			}

			_, err := service.Update(context.Background(), validRequest(0, false))
			if (err != nil) != (test.validation != nil) {
				t.Fatalf("Update() error = %v", err)
			}
			if !reflect.DeepEqual(events.events, test.wantEvents) {
				t.Fatalf("events = %#v; want %#v", events.events, test.wantEvents)
			}
		})
	}
}

func TestUpdateRollsBackFilesWhenRotationPreparationFails(t *testing.T) {
	resetMutationGate(t)
	events := &eventLog{}
	files := &fakePreparer{events: events, snap: &fakeSnapshot{events: events}}
	service, validator, _ := newService(t, loggingFixture, events, files, nil, nil)
	service.rotation = &fakeRotationPreparer{events: events, err: errors.New("secret systemd detail")}

	_, err := service.Update(context.Background(), validRequest(0, false))
	if code, message, status := transaction.ErrorInfo(err); code != "log_rotation_failed" || status != 503 ||
		strings.Contains(message, "secret") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("rotation failure = %q %q %d (%v)", code, message, status, err)
	}
	if !reflect.DeepEqual(events.events, []string{"prepare", "rotation-prepare", "rollback"}) || validator.calls != 0 {
		t.Fatalf("events=%#v validator calls=%d", events.events, validator.calls)
	}
}

func newService(
	t *testing.T,
	config string,
	events *eventLog,
	preparer *fakePreparer,
	validationErr error,
	restartErr error,
) (*Service, *fakeValidator, string) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(config), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := transaction.OpenStore(context.Background(), filepath.Join(directory, "helper.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	validator := &fakeValidator{events: events, err: validationErr}
	restarter := &fakeRestarter{events: events, err: restartErr}
	engine, err := transaction.NewEngine(configPath, store, validator, restarter)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	service, err := New(engine, preparer, noOpRotationPreparer{})
	if err != nil {
		t.Fatal(err)
	}
	return service, validator, configPath
}

func validRequest(generation uint64, restart bool) api.LogConfigurationMutationRequest {
	dnsLog := true
	return api.LogConfigurationMutationRequest{
		MutationRequest: api.MutationRequest{ExpectedGeneration: &generation, Restart: restart},
		LogConfigurationRequest: api.LogConfigurationRequest{
			Access:   api.LogTarget{Mode: api.LogTargetFile},
			Error:    api.LogTarget{Mode: api.LogTargetFile},
			Loglevel: "info",
			DNSLog:   &dnsLog,
		},
	}
}

func resetMutationGate(t *testing.T) {
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
		cleanup, acquireErr := coordinator.AcquireMaintenance(context.Background())
		if acquireErr == nil {
			_ = cleanup.Unblock()
			cleanup.Release()
		}
	})
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
