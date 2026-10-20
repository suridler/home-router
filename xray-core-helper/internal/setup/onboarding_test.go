package setup

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestExistingXrayNeedsOnboardingAndAdoptionPreservesFile(t *testing.T) {
	ctx := context.Background()
	store, entries, _ := fileStoreFixture(t)
	path := entries[0].SystemPath
	raw := []byte("{\n // existing secret stays local\n \"log\": {}\n}\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	startup, _ := NewStartup(ctx, path, store)
	if !startup.Status().SetupRequired || !startup.Status().RequiresChoice {
		t.Fatal("config bypassed onboarding")
	}
	service := NewStartupService(startup, nil, nil)
	if _, err := service.Adopt(ctx, "stale"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal(err)
	}
	if _, err := service.Adopt(ctx, startup.Status().Fingerprint); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	got, _ := os.ReadFile(path)
	if string(got) != string(raw) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("adoption replaced or changed config")
	}
	reopened, _ := NewStartup(ctx, path, store)
	if reopened.Status().SetupRequired || reopened.Status().RecoveryRequired {
		t.Fatal(reopened.Status())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopened, _ = NewStartup(ctx, path, store)
	if !reopened.Status().RecoveryRequired {
		t.Fatal("missing configured file became fresh setup")
	}
}

func TestNetworkPreparationPreservesExistingConfigAndDoesNotRestartXray(t *testing.T) {
	ctx := context.Background()
	store, entries, _ := fileStoreFixture(t)
	raw := []byte(`{"log":{"loglevel":"warning"}}`)
	if err := os.WriteFile(entries[0].SystemPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	startup, _ := NewStartup(ctx, entries[0].SystemPath, store)
	calls := []string{}
	engine, _ := NewEngine(startup.Status(), discoveryFake{[]Interface{{Name: "lan0", Addresses: []string{"10.0.0.1/24"}}}}, store, starterFake{&calls, false})
	service := NewStartupService(startup, nil, engine)
	plan, err := service.Plan(ctx, "lan0")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PreserveXray {
		t.Fatal("replacement plan for existing config")
	}
	if _, err = service.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(entries[0].SystemPath)
	if string(got) != string(raw) || len(calls) != 0 {
		t.Fatal("config replaced or Xray restarted")
	}
}
