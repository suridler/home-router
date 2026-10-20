package setup

import (
	"context"
	"errors"
	"home-router/xray-core-helper/internal/xrayconfig"
	"reflect"
	"testing"
)

type discoveryFake struct{ items []Interface }

func (f discoveryFake) Discover(context.Context) ([]Interface, error) { return f.items, nil }

type txFake struct {
	calls *[]string
	fail  string
}

func (t txFake) Validate(context.Context) error {
	*t.calls = append(*t.calls, "validate")
	if t.fail == "validate" {
		return errors.New("failed")
	}
	return nil
}
func (t txFake) Commit(context.Context) error {
	*t.calls = append(*t.calls, "commit")
	if t.fail == "commit" {
		return errors.New("failed")
	}
	return nil
}
func (t txFake) Rollback(context.Context) error { *t.calls = append(*t.calls, "rollback"); return nil }
func (t txFake) Complete(context.Context) error { *t.calls = append(*t.calls, "complete"); return nil }

type storeFake struct {
	calls *[]string
	fail  string
}

func (s storeFake) Prepare(context.Context, Plan) (SetupTransaction, error) {
	*s.calls = append(*s.calls, "prepare")
	return txFake{s.calls, s.fail}, nil
}
func (s storeFake) Recover(context.Context) error { *s.calls = append(*s.calls, "recover"); return nil }

type starterFake struct {
	calls *[]string
	fail  bool
}

func (s starterFake) Start(context.Context) error {
	*s.calls = append(*s.calls, "start")
	if s.fail {
		return errors.New("failed")
	}
	return nil
}
func engineFixture(t *testing.T, items []Interface, fail string) (*Engine, *[]string) {
	t.Helper()
	calls := []string{}
	engine, err := NewEngine(ConfigProbe{State: ConfigEmpty, SetupRequired: true}, discoveryFake{items}, storeFake{&calls, fail}, starterFake{&calls, fail == "start"})
	if err != nil {
		t.Fatal(err)
	}
	return engine, &calls
}
func TestPlanRequiresChoiceOnlyWhenAmbiguous(t *testing.T) {
	one := []Interface{{Name: "lan0", Addresses: []string{"192.0.2.1/24"}}}
	engine, _ := engineFixture(t, one, "")
	plan, err := engine.Plan(context.Background(), "")
	if err != nil || plan.Interface != "lan0" || plan.ListenAddress != "192.0.2.1" || plan.LANNetwork != "192.0.2.0/24" || plan.RoutingEnabled {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	engine, _ = engineFixture(t, append(one, Interface{Name: "lan1", Addresses: []string{"10.0.0.1/24"}}), "")
	if _, err = engine.Plan(context.Background(), ""); !errors.Is(err, ErrInterfaceRequired) {
		t.Fatalf("err=%v", err)
	}
}
func TestSharedEngineAppliesValidatedDirectOnlyPlan(t *testing.T) {
	engine, calls := engineFixture(t, []Interface{{Name: "lan0", Addresses: []string{"192.0.2.1/24"}}}, "")
	plan, _ := engine.Plan(context.Background(), "")
	result, err := engine.Apply(context.Background(), plan)
	if err != nil || result.Status != "configured_direct" || result.RoutingEnabled || !reflect.DeepEqual(*calls, []string{"prepare", "validate", "commit", "start", "complete"}) {
		t.Fatalf("result=%#v calls=%v err=%v", result, *calls, err)
	}
}
func TestSharedEngineRollsBackEveryFailure(t *testing.T) {
	for _, phase := range []string{"validate", "commit", "start"} {
		t.Run(phase, func(t *testing.T) {
			engine, calls := engineFixture(t, []Interface{{Name: "lan0", Addresses: []string{"192.0.2.1/24"}}}, phase)
			plan, _ := engine.Plan(context.Background(), "")
			if _, err := engine.Apply(context.Background(), plan); err == nil {
				t.Fatal("missing failure")
			}
			if (*calls)[len(*calls)-1] != "rollback" {
				t.Fatalf("calls=%v", *calls)
			}
		})
	}
}

type analyzedStore struct{ storeFake }

func (s analyzedStore) AnalyzeGateway(_ context.Context, p Plan) (Plan, error) {
	p.Gateway = &xrayconfig.Gateway{Port: 52345, EgressMark: 255}
	return p, nil
}

type unreadyStarter struct{ calls *[]string }

func (s unreadyStarter) Start(context.Context) error {
	*s.calls = append(*s.calls, "start")
	return nil
}
func (s unreadyStarter) Ready(context.Context) error {
	*s.calls = append(*s.calls, "ready_failed")
	return errors.New("unready")
}
func (s unreadyStarter) Restore(context.Context, bool) error {
	*s.calls = append(*s.calls, "restore_xray")
	return nil
}
func TestGatewayReadinessFailureRestoresConfigAndRuntimeBeforeCompletion(t *testing.T) {
	calls := []string{}
	engine, _ := NewEngine(ConfigProbe{SetupRequired: true}, discoveryFake{[]Interface{{Name: "lan0", Addresses: []string{"10.0.0.1/24"}}}}, analyzedStore{storeFake{calls: &calls}}, unreadyStarter{&calls})
	plan, err := engine.Plan(context.Background(), "lan0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Apply(context.Background(), plan); err == nil {
		t.Fatal("unready Xray completed setup")
	}
	if !reflect.DeepEqual(calls, []string{"prepare", "validate", "commit", "start", "ready_failed", "rollback", "restore_xray"}) {
		t.Fatal(calls)
	}
}
