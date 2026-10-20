package loggingservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/logfileprep"
	"home-router/xray-core-helper/internal/logpathcheck"
	"home-router/xray-core-helper/internal/logrotation"
	"home-router/xray-core-helper/internal/systemsettings"
	"home-router/xray-core-helper/internal/transaction"
	"home-router/xray-core-helper/internal/xrayconfig"
)

type ConfigTransaction interface {
	MutateWithPreparation(context.Context, uint64, transaction.Planner, bool, transaction.PrepareMutation) (transaction.Metadata, error)
}

type FilePreparer interface {
	Prepare(context.Context, api.LogConfigurationRequest) (logfileprep.Snapshot, error)
}

type RotationPreparer interface {
	Prepare(context.Context, api.LogConfigurationRequest) (logrotation.Snapshot, error)
}

type Settings interface {
	Logging() bootstrap.LoggingConfig
	PrepareLogging(context.Context, bootstrap.LoggingConfig, transaction.Metadata, transaction.Metadata) (transaction.MutationPreparation, error)
}

type Service struct {
	checker  PathChecker
	settings Settings
	paths    func(bootstrap.LoggingConfig) (FilePreparer, error)
	engine   ConfigTransaction
	preparer FilePreparer
	rotation RotationPreparer
}

func New(engine ConfigTransaction, preparer FilePreparer, rotation RotationPreparer) (*Service, error) {
	if engine == nil || preparer == nil || rotation == nil {
		return nil, errors.New("logging mutation dependencies are required")
	}
	return &Service{engine: engine, preparer: preparer, rotation: rotation}, nil
}

func (service *Service) Update(ctx context.Context, request api.LogConfigurationMutationRequest) (transaction.Metadata, error) {
	if ctx == nil || request.ExpectedGeneration == nil {
		return transaction.Metadata{}, transaction.Reject("invalid_request", "expectedGeneration is required", http.StatusBadRequest)
	}
	if err := xrayconfig.ValidateLogConfigurationRequest(request.LogConfigurationRequest); err != nil {
		return transaction.Metadata{}, err
	}
	if service == nil || service.engine == nil || service.preparer == nil || service.rotation == nil {
		return transaction.Metadata{}, transaction.Reject("logging_unavailable", "logging mutation service is unavailable", http.StatusServiceUnavailable)
	}

	// Planning runs after generation/drift checks, under the mutation gate.
	var next bootstrap.LoggingConfig
	var restriction logpathcheck.Result
	var before, after transaction.Metadata
	planner := xrayconfig.PlanLogConfiguration(request.LogConfigurationRequest)
	if service.settings != nil {
		planner = func(source []byte) ([]transaction.ByteEdit, error) {
			var err error
			next, err = xrayconfig.ResolveLogConfiguration(request.LogConfigurationRequest, service.settings.Logging())
			if err != nil {
				return nil, err
			}
			if service.checker != nil {
				restriction, err = service.checker.Check(ctx, next)
				if err != nil {
					return nil, transaction.Reject("log_path_check_failed", "Service log path restrictions could not be checked", http.StatusServiceUnavailable)
				}
				if restriction.Restricted() && !request.ConfirmRestrictedPaths {
					return nil, transaction.Reject("log_path_confirmation_required", "Service restrictions may prevent access to the configured log directory. Confirm to save and apply anyway; permissions will not be expanded. Files inaccessible to helper require manual preparation. A failed Xray restart will not restore the previous logging settings.", http.StatusConflict)
				}
			}
			edits, err := xrayconfig.PlanLogConfigurationValues(request.LogConfigurationRequest, next)(source)
			if err != nil {
				return nil, err
			}
			candidate, err := transaction.ApplyEdits(source, edits)
			if err != nil {
				return nil, err
			}
			hash := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
			before = transaction.Metadata{Generation: *request.ExpectedGeneration, ContentHash: hash(source)}
			after = transaction.Metadata{Generation: before.Generation, ContentHash: hash(candidate)}
			if after.ContentHash != before.ContentHash {
				after.Generation++
			}
			return edits, nil
		}
	}
	mutate := service.engine.MutateWithPreparation
	if request.ConfirmRestrictedPaths && service.checker != nil {
		engine, ok := service.engine.(confirmedTransaction)
		if !ok {
			return transaction.Metadata{}, transaction.Reject("logging_unavailable", "Confirmed logging changes are unavailable", http.StatusServiceUnavailable)
		}
		mutate = func(ctx context.Context, gen uint64, plan transaction.Planner, restart bool, prepare transaction.PrepareMutation) (transaction.Metadata, error) {
			return engine.MutateConfirmedLogging(ctx, gen, plan, restart, prepare, transaction.ConfirmedLoggingOptions{
				Enabled: func() bool { return restriction.Restricted() },
			})
		}
	}
	metadata, err := mutate(
		ctx,
		*request.ExpectedGeneration,
		planner,
		request.Restart,
		func(ctx context.Context) (transaction.MutationPreparation, error) {
			preparer := service.preparer
			var settings transaction.MutationPreparation
			if service.settings != nil {
				var err error
				preparer, err = service.paths(next)
				if err != nil {
					return nil, transaction.Reject("log_preparation_failed", "configured log files could not be prepared", http.StatusServiceUnavailable)
				}
				settings, err = service.settings.PrepareLogging(ctx, next, before, after)
				if err != nil {
					if errors.Is(err, systemsettings.ErrLoggingRecovery) {
						coordinator.Block()
						return nil, recoveryRequired()
					}
					return nil, transaction.Reject("logging_settings_failed", "helper logging settings could not be prepared", http.StatusServiceUnavailable)
				}
			}
			rollbackSettings := func() error {
				if settings != nil {
					return settings.Rollback()
				}
				return nil
			}
			fileRequest := request.LogConfigurationRequest
			// A confirmed helper restriction cannot be bypassed by chmod. Leave
			// those destinations for manual preparation; still prepare allowed ones.
			if request.ConfirmRestrictedPaths {
				if restriction.HelperAccess {
					fileRequest.Access.Mode = api.LogTargetJournal
				}
				if restriction.HelperError {
					fileRequest.Error.Mode = api.LogTargetJournal
				}
			}
			files, err := preparer.Prepare(ctx, fileRequest)
			if err != nil {
				if rollbackSettings() != nil || errors.Is(err, logfileprep.ErrRollbackFailed) {
					coordinator.Block()
					return nil, recoveryRequired()
				}
				if errors.Is(err, logfileprep.ErrPathAccess) {
					return nil, transaction.Reject("log_path_unwritable", "Xray cannot traverse a configured log directory; parent permissions must allow the service identity", http.StatusServiceUnavailable)
				}
				return nil, transaction.Reject(
					"log_preparation_failed",
					"configured log files could not be prepared; check file permissions and service filesystem restrictions",
					http.StatusServiceUnavailable,
				)
			}
			rotation, err := service.rotation.Prepare(ctx, request.LogConfigurationRequest)
			if err != nil {
				if rollbackErr := errors.Join(files.Rollback(), rollbackSettings()); rollbackErr != nil {
					coordinator.Block()
					return nil, recoveryRequired()
				}
				return nil, transaction.Reject(
					"log_rotation_failed",
					"Xray log rotation could not be configured",
					http.StatusServiceUnavailable,
				)
			}
			if rotation == nil {
				if rollbackErr := errors.Join(files.Rollback(), rollbackSettings()); rollbackErr != nil {
					coordinator.Block()
					return nil, recoveryRequired()
				}
				return nil, transaction.Reject("log_rotation_failed", "Xray log rotation could not be configured", http.StatusServiceUnavailable)
			}
			return combinedPreparation{files: files, rotation: rotation, settings: settings}, nil
		},
	)
	if err != nil && (errors.Is(err, transaction.ErrRollbackFailed) || errors.Is(err, transaction.ErrRecoveryRequired)) {
		coordinator.Block()
		return transaction.Metadata{}, recoveryRequired()
	}
	if err != nil {
		return transaction.Metadata{}, redactedTransactionError(err)
	}
	return metadata, nil
}

// SetSettings is called once at daemon wiring, before serving requests.
func (service *Service) SetSettings(settings Settings, paths func(bootstrap.LoggingConfig) (FilePreparer, error)) error {
	if settings == nil || paths == nil {
		return errors.New("logging settings and path preparation are required")
	}
	service.settings, service.paths = settings, paths
	return nil
}

type combinedPreparation struct {
	settings transaction.MutationPreparation
	files    logfileprep.Snapshot
	rotation logrotation.Snapshot
}

func (preparation combinedPreparation) Commit() error {
	if err := errors.Join(preparation.rotation.Commit(), preparation.files.Commit()); err != nil {
		return err
	}
	if preparation.settings != nil {
		return preparation.settings.Commit()
	}
	return nil
}

func (preparation combinedPreparation) Rollback() error {
	err := errors.Join(preparation.rotation.Rollback(), preparation.files.Rollback())
	if preparation.settings != nil {
		err = errors.Join(err, preparation.settings.Rollback())
	}
	return err
}

func recoveryRequired() error {
	return transaction.Reject(
		"recovery_required",
		"logging mutation recovery is required",
		http.StatusServiceUnavailable,
	)
}

func redactedTransactionError(err error) error {
	if errors.Is(err, coordinator.ErrRecoveryBlocked) {
		return &transaction.PublicError{
			Code: "recovery_required", Message: "logging mutations are blocked for recovery",
			HTTPStatus: http.StatusServiceUnavailable, Cause: err,
		}
	}
	code, message, status := transaction.ErrorInfo(err)
	if code == "transaction_failed" {
		code, message, status = "logging_mutation_failed", "logging mutation failed", http.StatusInternalServerError
	}
	return &transaction.PublicError{
		Code: code, Message: message, HTTPStatus: status, Cause: err,
	}
}
