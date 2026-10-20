package helperapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/domainstore"
	"home-router/xray-core-helper/internal/geodataadmin"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/geodataupdate"
	"home-router/xray-core-helper/internal/servicecontrol"
)

type daemonRecoveryFake struct{}

func (daemonRecoveryFake) Status() geodatarecovery.Status {
	return daemonCleanRecovery()
}

type daemonMutableRecovery struct {
	status geodatarecovery.Status
}

func (recovery *daemonMutableRecovery) Status() geodatarecovery.Status {
	return recovery.status
}

type daemonOverlayFake struct{}

func (daemonOverlayFake) ApplyWhileLocked(context.Context) error { return nil }

type daemonServiceControlFake struct {
	calls [][2]string
}

type daemonRoutingHealthFake struct {
	calls int
	err   error
}

func (fake *daemonRoutingHealthFake) ListRules(context.Context) ([]byte, error) {
	fake.calls++
	return []byte("fixture-api-output"), fake.err
}

func (fake *daemonServiceControlFake) Execute(_ context.Context, unit servicecontrol.Unit, action servicecontrol.Action) (servicecontrol.Result, error) {
	fake.calls = append(fake.calls, [2]string{string(unit), string(action)})
	return servicecontrol.Result{Unit: unit, Action: action, State: servicecontrol.StateActive}, nil
}

func TestConfiguredDaemonBuildsProductionGeodataExecutor(t *testing.T) {
	config, service, domains := daemonGeodataFixture(t, true)
	services := &daemonServiceControlFake{}
	routingHealth := &daemonRoutingHealthFake{}
	executor, err := newGeodataExecutor(config, service, domains, daemonOverlayFake{}, services, routingHealth)
	if err != nil {
		t.Fatalf("newGeodataExecutor() for complete configuration: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Execute(ctx, "configured-update"); !daemonExecutionError(err, "geodata_operation_unavailable", 503) {
		t.Fatalf("canceled Execute() = %v; want admitted production executor without starting update", err)
	}
	if len(services.calls) != 0 {
		t.Fatalf("service-control calls before update admission = %v; want none", services.calls)
	}
	if routingHealth.calls != 0 {
		t.Fatalf("Xray API calls before update admission = %d; want none", routingHealth.calls)
	}
}

func TestDisabledDaemonExecutorIsUnavailable(t *testing.T) {
	config, service, _ := daemonGeodataFixture(t, false)
	executor, err := newGeodataExecutor(config, service, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGeodataExecutor() for intentionally disabled geodata: %v", err)
	}
	if _, err := executor.Execute(context.Background(), "disabled-update"); !daemonExecutionError(err, "geodata_update_unavailable", 503) {
		t.Fatalf("disabled Execute() error = %v; want geodata_update_unavailable", err)
	}
}

func TestConfiguredDaemonFailsClosedOnMissingUpdateDependencies(t *testing.T) {
	config, service, domains := daemonGeodataFixture(t, true)
	for _, test := range []struct {
		name          string
		services      geodataupdate.ServiceController
		routingHealth geodataupdate.RoutingHealth
	}{
		{name: "service controller", routingHealth: &daemonRoutingHealthFake{}},
		{name: "Xray API health", services: &daemonServiceControlFake{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if executor, err := newGeodataExecutor(config, service, domains,
				daemonOverlayFake{}, test.services, test.routingHealth); err == nil || executor != nil {
				t.Fatalf("newGeodataExecutor() = %v, %v; want startup dependency error and no executor", executor, err)
			}
		})
	}
}

func TestDaemonRecoveryProviderTracksManagerAndCoordinatorState(t *testing.T) {
	resetDaemonCoordinator(t)
	t.Cleanup(func() { resetDaemonCoordinator(t) })
	manager := &daemonMutableRecovery{status: daemonCleanRecovery()}
	provider := liveGeodataRecovery{manager: manager}
	if status := provider.Status(); status.MutationsBlocked || status.Code != geodatarecovery.RecoveryNone.String() {
		t.Fatalf("initial recovery status = %#v; want clean", status)
	}

	coordinator.Block()
	blocked := provider.Status()
	if !blocked.MutationsBlocked || blocked.Code != geodatarecovery.ActionRetryInspection {
		t.Fatalf("coordinator-blocked status = %#v; want fail-closed projection", blocked)
	}
	manager.status = geodatarecovery.Status{
		Code: geodatarecovery.ActionReviewEvidence, MutationsBlocked: true,
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseInconsistent},
		},
	}
	if status := provider.Status(); status.Code != geodatarecovery.ActionReviewEvidence {
		t.Fatalf("manager status = %#v; want latest manager projection", status)
	}

	manager.status = daemonCleanRecovery()
	lease, err := coordinator.AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Unblock(); err != nil {
		lease.Release()
		t.Fatal(err)
	}
	lease.Release()
	if status := provider.Status(); status.MutationsBlocked || status.Code != geodatarecovery.RecoveryNone.String() {
		t.Fatalf("recovered status = %#v; want clean", status)
	}
}

func daemonGeodataFixture(t *testing.T, enabled bool) (bootstrap.Config, *geodataadmin.Service, *domainstore.Store) {
	t.Helper()
	directory := t.TempDir()
	config := bootstrap.Defaults()
	config.Xray.Config = filepath.Join(directory, "xray.json")
	config.Storage.Database = filepath.Join(directory, "helper.sqlite")
	config.Server.Socket = filepath.Join(directory, "helper.sock")
	if err := os.WriteFile(config.Xray.Config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if enabled {
		config.Geodata = bootstrap.GeodataConfig{
			MaxAssetSizeMB: 256,
			UserGeosite:    bootstrap.DefaultUserGeositeName,
			UserGeoip:      bootstrap.DefaultUserGeoipName,
			Builder:        "/usr/local/bin/domain-list-community",
			Origins:        []string{"https://downloads.example"},
			Geosite: bootstrap.GeodataSourceConfig{
				URL: "https://downloads.example/geosite.dat",
				Checksum: bootstrap.GeodataChecksumConfig{
					URL: "https://downloads.example/geosite.dat.sha256", Algorithm: "sha256",
				},
			},
			Geoip: bootstrap.GeodataSourceConfig{
				URL: "https://downloads.example/geoip.dat",
				Checksum: bootstrap.GeodataChecksumConfig{
					URL: "https://downloads.example/geoip.dat.sha256", Algorithm: "sha256",
				},
			},
		}
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}

	domains, err := domainstore.Open(context.Background(), config.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = domains.Close() })
	store, err := geodataadmin.Open(context.Background(), config.Storage.Database, config.Geodata)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := geodataadmin.NewService(geodataadmin.ServiceOptions{
		Store: store, Recovery: daemonRecoveryFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return config, service, domains
}

func daemonExecutionError(err error, code string, status int) bool {
	var executionError *geodataadmin.ExecutionError
	return errors.As(err, &executionError) && executionError.Code == code && executionError.HTTPStatus == status
}

func daemonCleanRecovery() geodatarecovery.Status {
	return geodatarecovery.Status{
		Code: geodatarecovery.RecoveryNone.String(),
		Result: geodatarecovery.Result{
			State: geodatarecovery.State{Phase: geodatarecovery.PhaseClean},
			Plan:  geodatarecovery.Plan{Phase: geodatarecovery.PhaseClean},
		},
	}
}

func resetDaemonCoordinator(t *testing.T) {
	t.Helper()
	if !coordinator.RecoveryBlocked() {
		return
	}
	lease, err := coordinator.AcquireMaintenance(context.Background())
	if err != nil {
		t.Fatalf("acquire recovery lease to reset test gate: %v", err)
	}
	if err := lease.Unblock(); err != nil {
		lease.Release()
		t.Fatalf("unblock test recovery gate: %v", err)
	}
	lease.Release()
}

var _ geodataupdate.RuntimeOverlay = daemonOverlayFake{}
var _ geodataupdate.ServiceController = (*daemonServiceControlFake)(nil)
var _ geodataupdate.RoutingHealth = (*daemonRoutingHealthFake)(nil)
