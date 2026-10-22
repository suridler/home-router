package readiness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/transaction"
)

type configFake struct {
	status api.StatusResponse
}

func (fake configFake) Status(context.Context) api.StatusResponse { return fake.status }

type databaseFake struct {
	metadataErr error
	pending     *transaction.Pending
	pendingErr  error
}

func (fake databaseFake) Metadata(context.Context) (transaction.Metadata, error) {
	return transaction.Metadata{Generation: 3, ContentHash: "safe-hash"}, fake.metadataErr
}

func (fake databaseFake) Pending(context.Context) (*transaction.Pending, error) {
	return fake.pending, fake.pendingErr
}

type xrayAPIFake struct {
	err error
}

func (fake xrayAPIFake) VerifyActive(context.Context) error { return fake.err }

type recoveryFake struct {
	status geodatarecovery.Status
}

func (fake recoveryFake) Status() geodatarecovery.Status { return fake.status }

func healthyDependencies() (configFake, databaseFake, xrayAPIFake, recoveryFake) {
	return configFake{status: api.StatusResponse{
		ConfigState: "parsed", ConfigReadable: true, ConfigParsed: true,
	}}, databaseFake{}, xrayAPIFake{}, recoveryFake{status: geodatarecovery.Status{
		Code: geodatarecovery.RecoveryNone.String(),
	}}
}

func TestCheckReportsFixedHealthyProjection(t *testing.T) {
	config, database, xrayAPI, recovery := healthyDependencies()
	result := New(config, database, xrayAPI, recovery, healthyTemplateStatus()).Check(context.Background())
	want := api.ReadinessResponse{Status: StatusReady, Checks: []api.ReadinessCheck{
		{Name: "config", Status: StatusReady, Code: "config_ready"},
		{Name: "database", Status: StatusReady, Code: "database_ready"},
		{Name: "xray_api", Status: StatusReady, Code: "xray_api_ready"},
		{Name: "recovery", Status: StatusReady, Code: "recovery_clear"},
		{Name: "templates", Status: StatusReady, Code: "templates_synchronized"},
	}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("Check() = %#v; want %#v", result, want)
	}
}

func TestCheckReportsEachDegradedDependency(t *testing.T) {
	const secret = "vless://private@example.test?token=fixture-secret"
	tests := []struct {
		name, check, code string
		mutate            func(*configFake, *databaseFake, *xrayAPIFake, *recoveryFake)
	}{
		{
			name: "config unavailable", check: "config", code: "config_unavailable",
			mutate: func(config *configFake, _ *databaseFake, _ *xrayAPIFake, _ *recoveryFake) {
				config.status = api.StatusResponse{ConfigState: "invalid", ConfigError: secret}
			},
		},
		{
			name: "config drift", check: "config", code: "config_drift",
			mutate: func(config *configFake, _ *databaseFake, _ *xrayAPIFake, _ *recoveryFake) {
				config.status.ConfigState = "drift"
				config.status.ConfigDrift = true
				config.status.ConfigError = secret
			},
		},
		{
			name: "database unavailable", check: "database", code: "database_unavailable",
			mutate: func(_ *configFake, database *databaseFake, _ *xrayAPIFake, _ *recoveryFake) {
				database.metadataErr = errors.New(secret)
			},
		},
		{
			name: "transaction pending", check: "database", code: "transaction_pending",
			mutate: func(_ *configFake, database *databaseFake, _ *xrayAPIFake, _ *recoveryFake) {
				database.pending = &transaction.Pending{ID: secret, Phase: "staged"}
			},
		},
		{
			name: "Xray API unavailable", check: "xray_api", code: "xray_api_unavailable",
			mutate: func(_ *configFake, _ *databaseFake, xrayAPI *xrayAPIFake, _ *recoveryFake) {
				xrayAPI.err = errors.New(secret)
			},
		},
		{
			name: "recovery required", check: "recovery", code: "recovery_required",
			mutate: func(_ *configFake, _ *databaseFake, _ *xrayAPIFake, recovery *recoveryFake) {
				recovery.status = geodatarecovery.Status{Code: secret, MutationsBlocked: true}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, database, xrayAPI, recovery := healthyDependencies()
			test.mutate(&config, &database, &xrayAPI, &recovery)
			result := New(config, database, xrayAPI, recovery, healthyTemplateStatus()).Check(context.Background())
			if result.Status != StatusDegraded {
				t.Fatalf("status = %q; want degraded", result.Status)
			}
			found := false
			for _, check := range result.Checks {
				if check.Name == test.check {
					found = check.Status == StatusDegraded && check.Code == test.code
				}
				if strings.Contains(check.Code, secret) {
					t.Fatalf("check leaked secret: %#v", check)
				}
			}
			if !found {
				t.Fatalf("checks = %#v; want %s/%s", result.Checks, test.check, test.code)
			}
		})
	}
}

func TestCheckFailsClosedForMissingDependenciesAndNilContext(t *testing.T) {
	result := New(nil, nil, nil, nil).Check(nil)
	if result.Status != StatusDegraded || len(result.Checks) != 5 {
		t.Fatalf("Check() = %#v", result)
	}
	for _, check := range result.Checks {
		if check.Status != StatusDegraded {
			t.Fatalf("missing dependency reported ready: %#v", check)
		}
	}
}
