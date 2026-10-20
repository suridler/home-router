package templateapply

import (
	"context"

	"home-router/xray-core-helper/internal/templatecatalog"
)

// Gate is the transport-facing boundary. It deliberately cannot invoke the
// internal engine until Phase F supplies durable confirmed-apply ownership.
type Gate struct {
	catalog templatecatalog.Catalog
	backups BackupStore
	engine  *Engine
}

func NewGate(backups BackupStore, engine *Engine) (*Gate, error) {
	if backups == nil || engine == nil {
		return nil, &Error{Code: CodeRuntime}
	}
	return &Gate{catalog: templatecatalog.Default(), backups: backups, engine: engine}, nil
}

func (gate *Gate) Apply(ctx context.Context, id templatecatalog.ID, expectedGeneration uint64) (Result, error) {
	if _, err := gate.catalog.Lookup(id); err != nil {
		return Result{}, &Error{Code: CodeUnknownTemplate}
	}
	generation, err := gate.backups.CompletedGeneration(ctx, id)
	if err != nil {
		return Result{}, &Error{Code: CodeBackup}
	}
	if generation != expectedGeneration {
		return Result{}, &Error{Code: CodeGeneration}
	}
	return Result{}, &Error{Code: CodeSafeApply}
}
