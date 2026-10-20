package geodataupdate

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/servicecontrol"
)

type serviceControllerFake struct {
	calls  [][2]string
	result servicecontrol.Result
	err    error
}

type routingHealthFake struct {
	output []byte
	err    error
	calls  int
}

func (fake *routingHealthFake) ListRules(context.Context) ([]byte, error) {
	fake.calls++
	return append([]byte(nil), fake.output...), fake.err
}

func (fake *serviceControllerFake) Execute(_ context.Context, unit servicecontrol.Unit, action servicecontrol.Action) (servicecontrol.Result, error) {
	fake.calls = append(fake.calls, [2]string{string(unit), string(action)})
	return fake.result, fake.err
}

func TestServiceControlRestarterRestartsOnlyXrayWhileCoordinatorIsHeld(t *testing.T) {
	services := &serviceControllerFake{}
	restarter, err := NewServiceControlRestarter(services)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := restarter.RestartWhileLocked(ctx); err != nil {
		t.Fatalf("RestartWhileLocked() under the existing coordinator lease = %v", err)
	}
	want := [][2]string{{string(servicecontrol.UnitXray), string(servicecontrol.ActionRestart)}}
	if !reflect.DeepEqual(services.calls, want) {
		t.Fatalf("service-control calls = %v; want %v", services.calls, want)
	}
}

func TestRoutingServiceHealthVerifierUsesXrayAPIAndDiscardsOutput(t *testing.T) {
	const secret = "fixture-api-output"
	routing := &routingHealthFake{output: []byte(secret)}
	services := &serviceControllerFake{}
	verifier, err := NewRoutingServiceHealthVerifier(routing)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyActive(context.Background()); err != nil {
		t.Fatalf("VerifyActive() = %v", err)
	}
	if routing.calls != 1 {
		t.Fatalf("Xray API calls = %d; want 1", routing.calls)
	}
	if len(services.calls) != 0 {
		t.Fatalf("systemd calls during API health check = %v; want none", services.calls)
	}
}

func TestRoutingServiceHealthVerifierPropagatesXrayAPIFailure(t *testing.T) {
	wantErr := errors.New("Xray RoutingService failed")
	routing := &routingHealthFake{output: []byte("fixture-secret-output"), err: wantErr}
	verifier, err := NewRoutingServiceHealthVerifier(routing)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyActive(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("VerifyActive() error = %v; want Xray API error", err)
	}
	if routing.calls != 1 {
		t.Fatalf("Xray API calls = %d; want 1", routing.calls)
	}
}
