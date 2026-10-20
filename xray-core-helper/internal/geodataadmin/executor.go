package geodataadmin

import (
	"context"
	"errors"
	"fmt"
	"home-router/xray-core-helper/internal/geodatalimits"
	"net/http"
	"path/filepath"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/geodatadownload"
	"home-router/xray-core-helper/internal/geodataupdate"
)

type Updater interface {
	Update(context.Context, bootstrap.GeodataConfig) error
}

type Executor struct {
	service *Service
	base    bootstrap.GeodataConfig
	updater Updater
}

type ExecutionError struct {
	MaxAssetSizeMB int
	Code           string
	HTTPStatus     int
}

func (failure *ExecutionError) Error() string { return failure.Code }

func NewExecutor(service *Service, trustedBootstrap bootstrap.GeodataConfig, updater Updater) (*Executor, error) {
	if service == nil || !filepath.IsAbs(trustedBootstrap.Builder) ||
		filepath.Clean(trustedBootstrap.Builder) != trustedBootstrap.Builder {
		return nil, errors.New("geodata execution service dependencies are required")
	}
	return &Executor{service: service, base: trustedBootstrap, updater: updater}, nil
}

// Execute serializes one durable operation and never runs an updater for a
// replayed key. A nil updater intentionally provides no execution path.
func (executor *Executor) Execute(ctx context.Context, key string) (Operation, error) {
	if ctx == nil {
		return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
	}
	if executor.updater == nil {
		return Operation{}, executionError("geodata_update_unavailable", http.StatusServiceUnavailable)
	}
	operation, settings, replayed, err := executor.service.BeginUpdateSnapshot(ctx, key)
	if err != nil {
		return Operation{}, admissionError(err)
	}
	if replayed {
		return operation, executor.sizeError(replayError(operation))
	}

	config, err := RuntimeConfig(executor.base, settings.Settings)
	if err != nil {
		if finishErr := executor.finishFailure(ctx, operation.ID, FailureUpdateFailed); finishErr != nil {
			return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
		}
		return Operation{}, executionError(string(FailureUpdateFailed), http.StatusServiceUnavailable)
	}
	if err := executor.updater.Update(ctx, config); err != nil {
		if errors.Is(err, geodataupdate.ErrRollbackFailed) {
			coordinator.Block()
			if finishErr := executor.finishRecoveryRequired(ctx, operation.ID); finishErr != nil {
				return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
			}
			return Operation{}, executionError("geodata_recovery_required", http.StatusServiceUnavailable)
		}
		failureCode := classifyFailure(err)
		if finishErr := executor.finishFailure(ctx, operation.ID, failureCode); finishErr != nil {
			return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
		}
		return Operation{}, executor.sizeError(executionError(string(failureCode), http.StatusServiceUnavailable))
	}
	finishCtx, cancel := completionContext(ctx)
	defer cancel()
	if err := executor.service.FinishSuccess(finishCtx, operation.ID); err != nil {
		return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
	}
	status, err := executor.service.Status(finishCtx)
	if err != nil || status.Operation == nil || status.Operation.ID != operation.ID ||
		status.Operation.State != OperationSucceeded {
		return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
	}
	return *status.Operation, nil
}

func (executor *Executor) finishFailure(ctx context.Context, id string, code FailureCode) error {
	finishCtx, cancel := completionContext(ctx)
	defer cancel()
	return executor.service.FinishFailure(finishCtx, id, code)
}

func (executor *Executor) finishRecoveryRequired(ctx context.Context, id string) error {
	finishCtx, cancel := completionContext(ctx)
	defer cancel()
	return executor.service.FinishRecoveryRequired(finishCtx, id)
}

func completionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

// RuntimeConfig overlays only editable settings; the builder remains the
// trusted static bootstrap value.
func RuntimeConfig(trusted bootstrap.GeodataConfig, settings Settings) (bootstrap.GeodataConfig, error) {
	config := trusted
	config.Origins = append([]string{}, settings.Origins...)
	config.Geosite = bootstrap.GeodataSourceConfig{
		URL: settings.Geosite.URL,
		Checksum: bootstrap.GeodataChecksumConfig{
			URL: settings.Geosite.Checksum.URL, Algorithm: settings.Geosite.Checksum.Algorithm,
		},
	}
	config.Geoip = bootstrap.GeodataSourceConfig{
		URL: settings.Geoip.URL,
		Checksum: bootstrap.GeodataChecksumConfig{
			URL: settings.Geoip.Checksum.URL, Algorithm: settings.Geoip.Checksum.Algorithm,
		},
	}
	if err := config.Validate(); err != nil {
		return bootstrap.GeodataConfig{}, fmt.Errorf("validate persisted geodata settings: %w", err)
	}
	return config, nil
}

func admissionError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidIdempotencyKey):
		return executionError("invalid_idempotency_key", http.StatusBadRequest)
	case errors.Is(err, ErrIdempotencyConflict):
		return executionError("idempotency_conflict", http.StatusConflict)
	case errors.Is(err, ErrOperationInProgress):
		return executionError("geodata_update_in_progress", http.StatusConflict)
	case errors.Is(err, ErrRecoveryRequired):
		return executionError("geodata_recovery_required", http.StatusServiceUnavailable)
	default:
		return executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
	}
}

func replayError(operation Operation) error {
	switch operation.State {
	case OperationSucceeded, OperationRunning:
		return nil
	case OperationRecoveryRequired:
		return executionError("geodata_recovery_required", http.StatusServiceUnavailable)
	case OperationFailed:
		if validFailureCode(FailureCode(operation.ErrorCode)) {
			return executionError(operation.ErrorCode, http.StatusServiceUnavailable)
		}
	}
	return executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
}

func classifyFailure(err error) FailureCode {
	var tooLarge *geodatalimits.Exceeded
	switch {
	case errors.Is(err, geodataupdate.ErrRollbackFailed):
		return FailureRollbackFailed
	case errors.As(err, &tooLarge):
		return FailureAssetTooLarge
	case errors.Is(err, geodataupdate.ErrRestartFailed):
		return FailureRestartFailed
	case errors.Is(err, geodataupdate.ErrValidationFailed):
		return FailureValidationFailed
	case errors.Is(err, geodataupdate.ErrInstallFailed):
		return FailureInstallFailed
	case errors.Is(err, geodataupdate.ErrDownloadFailed):
		if errors.Is(err, geodatadownload.ErrChecksumMismatch) ||
			errors.Is(err, geodatadownload.ErrInvalidChecksum) {
			return FailureChecksumFailed
		}
		return FailureDownloadFailed
	default:
		return FailureUpdateFailed
	}
}

func executionError(code string, status int) error {
	return &ExecutionError{Code: code, HTTPStatus: status}
}

func (executor *Executor) sizeError(err error) error {
	var failure *ExecutionError
	if errors.As(err, &failure) && failure.Code == string(FailureAssetTooLarge) {
		failure.MaxAssetSizeMB = geodatalimits.Bytes(executor.base.MaxAssetSizeMB) / geodatalimits.BytesPerMB
	}
	return err
}
