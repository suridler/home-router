package templateapply

import (
	"context"
	"testing"

	"home-router/xray-core-helper/internal/templatebackup"
	"home-router/xray-core-helper/internal/templatecatalog"
)

type gateBackups struct{ generation uint64 }

func (stub gateBackups) CompletedGeneration(context.Context, templatecatalog.ID) (uint64, error) {
	return stub.generation, nil
}
func (stub gateBackups) Snapshot(context.Context, templatecatalog.ID, templatebackup.Side, templatebackup.State) (templatebackup.Record, error) {
	panic("gate invoked applier backup")
}
func (stub gateBackups) Restore(context.Context, templatecatalog.ID, string, uint64) (templatebackup.Record, error) {
	panic("gate restored backup")
}
func (stub gateBackups) RestorePending(context.Context, templatecatalog.ID, string) (templatebackup.Record, error) {
	panic("gate restored pending backup")
}
func (stub gateBackups) SetState(context.Context, templatecatalog.ID, string, templatebackup.State) (templatebackup.Record, error) {
	panic("gate mutated backup")
}

func TestGateRejectsEveryAllowlistedTemplateWithoutInvokingEngine(t *testing.T) {
	gate := &Gate{catalog: templatecatalog.Default(), backups: gateBackups{generation: 4}, engine: &Engine{}}
	for _, entry := range templatecatalog.Default().Entries() {
		_, err := gate.Apply(context.Background(), entry.ID, 4)
		if !IsCode(err, CodeSafeApply) {
			t.Errorf("id=%s err=%v", entry.ID, err)
		}
	}
}
func TestGateChecksAllowlistAndGeneration(t *testing.T) {
	gate := &Gate{catalog: templatecatalog.Default(), backups: gateBackups{generation: 4}, engine: &Engine{}}
	if _, err := gate.Apply(context.Background(), "../other", 4); !IsCode(err, CodeUnknownTemplate) {
		t.Fatalf("unknown err=%v", err)
	}
	if _, err := gate.Apply(context.Background(), templatecatalog.Sysctl, 3); !IsCode(err, CodeGeneration) {
		t.Fatalf("generation err=%v", err)
	}
}
