package geodataadmin

import (
	"context"
	"errors"
	"fmt"
	"home-router/xray-core-helper/internal/api"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"home-router/xray-core-helper/internal/bootstrap"
)

type Updater interface {
	UpdateReportProgress(context.Context, bootstrap.GeodataConfig, func(api.GeodataReport) error) api.GeodataReport
}

type Executor struct {
	service       *Service
	base          bootstrap.GeodataConfig
	updater       Updater
	mu            sync.Mutex
	workerContext context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	closed        bool
}

type ExecutionError struct {
	MaxAssetSizeMB int
	Code           string
	Message        string
	HTTPStatus     int
}

func (failure *ExecutionError) Error() string { return failure.Code }

func NewExecutor(service *Service, trustedBootstrap bootstrap.GeodataConfig, updater Updater) (*Executor, error) {
	if service == nil || !filepath.IsAbs(trustedBootstrap.Builder) ||
		filepath.Clean(trustedBootstrap.Builder) != trustedBootstrap.Builder {
		return nil, errors.New("geodata execution service dependencies are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Executor{service: service, base: trustedBootstrap, updater: updater, workerContext: ctx, cancel: cancel}, nil
}

// Execute durably admits work before returning. Request cancellation cannot stop an
// accepted operation; Close cancels and joins workers before their stores close.
func (executor *Executor) Execute(ctx context.Context, key string) (Operation, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.closed || ctx == nil || executor.updater == nil {
		return Operation{}, executionError("geodata_update_unavailable", http.StatusServiceUnavailable)
	}
	operation, settings, replayed, err := executor.service.BeginUpdateSnapshot(ctx, key)
	if err != nil {
		return Operation{}, admissionError(err)
	}
	if replayed {
		return operation, nil
	}
	executor.workers.Add(1)
	go func() {
		defer executor.workers.Done()
		if _, err := executor.executeAdmitted(executor.workerContext, operation, settings); err != nil {
			log.Printf("geodata operation=%s result=%v", operation.ID, err)
		}
	}()
	return operation, nil
}

func (executor *Executor) Close() error {
	executor.mu.Lock()
	executor.closed = true
	executor.cancel()
	executor.mu.Unlock()
	executor.workers.Wait()
	return nil
}

func (executor *Executor) executeAdmitted(ctx context.Context, operation Operation, settings VersionedSettings) (Operation, error) {
	config, err := RuntimeConfig(executor.base, settings.Settings)
	if err != nil {
		if finishErr := executor.finishFailure(ctx, operation.ID, FailureUpdateFailed); finishErr != nil {
			return Operation{}, executionError("geodata_operation_unavailable", http.StatusServiceUnavailable)
		}
		return Operation{}, executionError(string(FailureUpdateFailed), http.StatusServiceUnavailable)
	}

	report := executor.updater.UpdateReportProgress(ctx, config, func(report api.GeodataReport) error {
		checkpointCtx, cancel := completionContext(ctx)
		defer cancel()
		return executor.service.store.saveReport(checkpointCtx, operation.ID, &report)
	})
	if ctx.Err() != nil {
		report.Issues = append([]api.GeodataIssue{{Process: report.Process, Stage: report.Stage,
			Code: string(FailureInterrupted), Message: "Обновление прервано при остановке helper", At: time.Now().UnixMilli()}}, report.Issues...)
	}
	finishCtx, cancel := completionContext(ctx)
	defer cancel()
	failed := false
	for _, issue := range report.Issues {
		log.Printf("geodata operation=%s process=%s file=%s stage=%s code=%s warning=%t: %s", operation.ID, issue.Process, issue.File, issue.Stage, issue.Code, issue.Warning, issue.Message)
		failed = failed || !issue.Warning
	}
	if report.Activation != nil {
		issue := report.Activation
		log.Printf("gateway operation=%s stage=%s code=%s: %s", operation.ID, issue.Stage, issue.Code, issue.Message)
	}
	if err := executor.service.store.saveReport(finishCtx, operation.ID, &report); err != nil {
		return persistenceFailure(operation, report)
	}
	if failed {
		code := FailureUpdateFailed
		for _, issue := range report.Issues {
			if !issue.Warning {
				code = FailureCode(issue.Code)
				break
			}
		}
		err = executor.service.FinishFailure(finishCtx, operation.ID, code)
	} else {
		err = executor.service.FinishSuccess(finishCtx, operation.ID)
	}
	if err != nil {
		return persistenceFailure(operation, report)
	}
	status, err := executor.service.Status(finishCtx)
	if err != nil || status.Operation == nil {
		return persistenceFailure(operation, report)
	}
	return *status.Operation, operationError(*status.Operation)
}

// Even if SQLite cannot retain the final report, preserve the known file result
// in the HTTP response and journal instead of returning an unexplained 503.
func persistenceFailure(operation Operation, report api.GeodataReport) (Operation, error) {
	issue := api.GeodataIssue{Process: "cycle", Stage: "save_state", Code: "geodata_cycle_save_state_failed",
		Message: "Не удалось сохранить или прочитать итог операции в SQLite", At: time.Now().UnixMilli()}
	report.Issues = append(report.Issues, issue)
	operation.Report, operation.State, operation.ErrorCode = &report, OperationFailed, issue.Code
	log.Printf("geodata operation=%s process=cycle stage=save_state code=%s: %s", operation.ID, issue.Code, issue.Message)
	// A prior activation failure remains primary in operationError, with this
	// persistence failure retained as another visible issue in the same report.
	return operation, operationError(operation)
}

func (executor *Executor) finishFailure(ctx context.Context, id string, code FailureCode) error {
	finishCtx, cancel := completionContext(ctx)
	defer cancel()
	return executor.service.FinishFailure(finishCtx, id, code)
}

func completionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

// RuntimeConfig combines an admitted TOML snapshot with the startup defaults.
// Builder changes are accepted only through manual TOML edits, never API writes.
func RuntimeConfig(trusted bootstrap.GeodataConfig, settings Settings) (bootstrap.GeodataConfig, error) {
	config := trusted
	if settings.MaxAssetSizeMB != 0 {
		config.MaxAssetSizeMB = settings.MaxAssetSizeMB
	}
	if settings.Builder != "" {
		config.Builder = settings.Builder
	}
	if settings.UserGeosite != "" {
		config.UserGeosite = settings.UserGeosite
	}
	if settings.UserGeoip != "" {
		config.UserGeoip = settings.UserGeoip
	}
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

func executionError(code string, status int) error {
	return &ExecutionError{Code: code, HTTPStatus: status}
}

func operationError(op Operation) error {
	var issue *api.GeodataIssue
	if op.Report != nil {
		issue = op.Report.Activation
		if issue == nil {
			for i := range op.Report.Issues {
				if !op.Report.Issues[i].Warning {
					issue = &op.Report.Issues[i]
					break
				}
			}
		}
	}
	if issue != nil {
		return &ExecutionError{Code: issue.Code, HTTPStatus: 503, MaxAssetSizeMB: issue.MaxAssetSizeMB, Message: fmt.Sprintf("%s · %s · %s: %s (операция %s)", issue.Process, issue.File, issue.Stage, issue.Message, op.ID)}
	}
	return replayError(op)
}
