package geodataadmin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/coordinator"
	"home-router/xray-core-helper/internal/geodatarecovery"
	"home-router/xray-core-helper/internal/scheduler"
	"home-router/xray-core-helper/internal/systemsettings"
)

type RecoveryProvider interface {
	Status() geodatarecovery.Status
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type ServiceOptions struct {
	Store    *Store
	Settings *systemsettings.Service
	Recovery RecoveryProvider
	Clock    Clock
}

type Service struct {
	store    *Store
	settings *systemsettings.Service
	recovery RecoveryProvider
	clock    Clock
}

type RecoveryProjection struct {
	Phase            string `json:"phase"`
	Code             string `json:"code"`
	MutationsBlocked bool   `json:"mutationsBlocked"`
}

type Status struct {
	Settings         VersionedSettings  `json:"settings"`
	Operation        *Operation         `json:"operation,omitempty"`
	Recovery         RecoveryProjection `json:"recovery"`
	LastSuccessfulAt *int64             `json:"lastSuccessfulAt,omitempty"`
	NextScheduledAt  *int64             `json:"nextScheduledAt,omitempty"`
}

type FailureCode string

const (
	FailureAssetTooLarge    FailureCode = "geodata_asset_too_large"
	FailureDownloadFailed   FailureCode = "geodata_download_failed"
	FailureChecksumFailed   FailureCode = "geodata_checksum_failed"
	FailureValidationFailed FailureCode = "geodata_validation_failed"
	FailureInstallFailed    FailureCode = "geodata_install_failed"
	FailureRestartFailed    FailureCode = "geodata_restart_failed"
	FailureRollbackFailed   FailureCode = "geodata_rollback_failed"
	FailureUpdateFailed     FailureCode = "geodata_update_failed"
	FailureInterrupted      FailureCode = "geodata_interrupted"
)

func NewService(options ServiceOptions) (*Service, error) {
	if options.Store == nil || options.Settings == nil || options.Recovery == nil {
		return nil, errors.New("geodata administration dependencies are required")
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{store: options.Store, settings: options.Settings, recovery: options.Recovery, clock: options.Clock}, nil
}

func (service *Service) Settings(ctx context.Context) (VersionedSettings, error) {
	if err := validContext(ctx); err != nil {
		return VersionedSettings{}, err
	}
	return service.currentSettings(ctx)
}

func (service *Service) Status(ctx context.Context) (Status, error) {
	if err := validContext(ctx); err != nil {
		return Status{}, err
	}
	state, err := service.store.readSnapshot(ctx)
	if err != nil {
		return Status{}, err
	}
	settings, err := service.currentSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	result := Status{Settings: settings, Operation: state.Operation, Recovery: projectRecovery(service.recovery.Status())}
	var last sql.NullInt64
	if err := service.store.db.QueryRowContext(ctx, "SELECT MAX(finished_at) FROM geodata_update_history WHERE state='succeeded'").Scan(&last); err != nil {
		return Status{}, err
	}
	if last.Valid {
		result.LastSuccessfulAt = &last.Int64
	}
	if settings.Settings.Schedule != nil && settings.Settings.Schedule.Enabled {
		var hour, minute int
		if _, err := fmt.Sscanf(settings.Settings.Schedule.Time, "%d:%d", &hour, &minute); err == nil {
			next := scheduler.Daily(hour, minute)(service.clock.Now()).UnixMilli()
			result.NextScheduledAt = &next
		}
	}
	return result, nil
}

func (service *Service) ReplaceSettings(ctx context.Context, expectedGeneration uint64, settings Settings) (VersionedSettings, error) {
	if err := validContext(ctx); err != nil {
		return VersionedSettings{}, err
	}
	lease, err := coordinator.Acquire(ctx)
	if errors.Is(err, coordinator.ErrRecoveryBlocked) {
		return VersionedSettings{}, ErrRecoveryRequired
	}
	if err != nil {
		return VersionedSettings{}, fmt.Errorf("acquire geodata settings mutation: %w", err)
	}
	defer lease.Release()
	if recoveryBlocked(service.recovery.Status()) {
		return VersionedSettings{}, ErrRecoveryRequired
	}
	if err := validateSettings(settings); err != nil {
		return VersionedSettings{}, err
	}
	if service.settings == nil {
		return VersionedSettings{}, ErrInvalidSettings
	}
	cfg, generation, err := service.settings.Geodata(ctx)
	if err != nil {
		return VersionedSettings{}, err
	}
	if generation != expectedGeneration {
		return VersionedSettings{}, ErrGenerationConflict
	}
	if settings.Builder != "" && settings.Builder != cfg.Geodata.Builder {
		return VersionedSettings{}, ErrInvalidSettings
	}
	cfg.Geodata, err = RuntimeConfig(cfg.Geodata, settings)
	if err != nil {
		return VersionedSettings{}, ErrInvalidSettings
	}
	if settings.Schedule != nil {
		cfg.Scheduler.GeodataUpdateEnabled = settings.Schedule.Enabled
		cfg.Scheduler.GeodataUpdateTime = settings.Schedule.Time
	}
	_, _, err = service.settings.PutGeodata(ctx, expectedGeneration, cfg.Geodata, cfg.Scheduler)
	if errors.Is(err, systemsettings.ErrSettingsDrift) {
		return VersionedSettings{}, ErrGenerationConflict
	}
	if errors.Is(err, systemsettings.ErrInvalidSettings) {
		return VersionedSettings{}, ErrInvalidSettings
	}
	if err != nil {
		return VersionedSettings{}, err
	}
	return service.currentSettings(ctx)
}

// BeginUpdate admits one durable update request against the current validated
// settings. The returned bool is true only when an existing request was replayed.
func (service *Service) BeginUpdate(ctx context.Context, idempotencyKey string) (Operation, bool, error) {
	operation, _, replayed, err := service.BeginUpdateSnapshot(ctx, idempotencyKey)
	return operation, replayed, err
}

// BeginUpdateSnapshot admits an update and returns the exact persisted settings
// snapshot fingerprinted by the durable operation record.
func (service *Service) BeginUpdateSnapshot(ctx context.Context, idempotencyKey string) (Operation, VersionedSettings, bool, error) {
	if err := validContext(ctx); err != nil {
		return Operation{}, VersionedSettings{}, false, err
	}
	if !validIdempotencyKey(idempotencyKey) {
		return Operation{}, VersionedSettings{}, false, ErrInvalidIdempotencyKey
	}
	if operation, settings, found, err := service.readUpdateReplay(ctx, idempotencyKey); err != nil {
		return Operation{}, VersionedSettings{}, false, err
	} else if found {
		return operation, settings, true, nil
	}
	lease, err := coordinator.Acquire(ctx)
	if errors.Is(err, coordinator.ErrRecoveryBlocked) {
		return Operation{}, VersionedSettings{}, false, ErrRecoveryRequired
	}
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("acquire geodata update admission: %w", err)
	}
	defer lease.Release()
	tx, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("begin geodata update admission: %w", err)
	}
	defer tx.Rollback()
	settings, err := service.currentSettings(ctx)
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("read settings for geodata update: %w", err)
	}
	fingerprint, err := requestFingerprint(settings)
	if err != nil {
		return Operation{}, VersionedSettings{}, false, err
	}
	current, operationErr := readOperation(ctx, tx)
	if operationErr != nil && !errors.Is(operationErr, sql.ErrNoRows) {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("read geodata update operation: %w", operationErr)
	}
	if operationErr == nil {
		var key, previousFingerprint string
		if err := tx.QueryRowContext(ctx, `SELECT idempotency_key, request_fingerprint
			FROM geodata_update_operation WHERE singleton = 1`).Scan(&key, &previousFingerprint); err != nil {
			return Operation{}, VersionedSettings{}, false, fmt.Errorf("read geodata idempotency record: %w", err)
		}
		if key == idempotencyKey {
			if previousFingerprint != fingerprint {
				return Operation{}, VersionedSettings{}, false, ErrIdempotencyConflict
			}
			if err := tx.Commit(); err != nil {
				return Operation{}, VersionedSettings{}, false, fmt.Errorf("commit geodata update replay: %w", err)
			}
			return current, settings, true, nil
		}
		if current.State == OperationRunning {
			return Operation{}, VersionedSettings{}, false, ErrOperationInProgress
		}
	}
	if recoveryBlocked(service.recovery.Status()) {
		return Operation{}, VersionedSettings{}, false, ErrRecoveryRequired
	}

	id, err := newOperationID()
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("create geodata operation identifier: %w", err)
	}
	now, err := service.timestamp()
	if err != nil {
		return Operation{}, VersionedSettings{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO geodata_update_operation(
		singleton, operation_id, idempotency_key, request_fingerprint, settings_generation,
		state, result_code, error_code, created_at, updated_at, finished_at
	) VALUES (1, ?, ?, ?, ?, 'running', '', '', ?, ?, NULL)
	ON CONFLICT(singleton) DO UPDATE SET
		operation_id = excluded.operation_id,
		idempotency_key = excluded.idempotency_key,
		request_fingerprint = excluded.request_fingerprint,
		settings_generation = excluded.settings_generation,
		state = excluded.state,
		result_code = excluded.result_code,
		error_code = excluded.error_code,
		created_at = excluded.created_at,
		updated_at = excluded.updated_at,
		finished_at = NULL
	WHERE geodata_update_operation.state != 'running'`,
		id, idempotencyKey, fingerprint, settings.Generation, now, now)
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("persist geodata update operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("commit geodata update admission: %w", err)
	}
	return Operation{
		ID: id, SettingsGeneration: settings.Generation, State: OperationRunning,
		CreatedAt: now, UpdatedAt: now,
	}, settings, false, nil
}

func (service *Service) readUpdateReplay(ctx context.Context, idempotencyKey string) (Operation, VersionedSettings, bool, error) {
	tx, err := service.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("begin geodata update replay lookup: %w", err)
	}
	defer tx.Rollback()
	settings, err := service.currentSettings(ctx)
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("read settings for geodata update replay: %w", err)
	}
	current, err := readOperation(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, VersionedSettings{}, false, nil
	}
	if err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("read geodata update replay operation: %w", err)
	}
	var key, fingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT idempotency_key, request_fingerprint
		FROM geodata_update_operation WHERE singleton = 1`).Scan(&key, &fingerprint); err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("read geodata replay key: %w", err)
	}
	if key != idempotencyKey {
		if current.State == OperationRunning {
			return Operation{}, VersionedSettings{}, false, ErrOperationInProgress
		}
		return Operation{}, VersionedSettings{}, false, nil
	}
	currentFingerprint, err := requestFingerprint(settings)
	if err != nil {
		return Operation{}, VersionedSettings{}, false, err
	}
	if fingerprint != currentFingerprint {
		return Operation{}, VersionedSettings{}, false, ErrIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, VersionedSettings{}, false, fmt.Errorf("commit geodata update replay lookup: %w", err)
	}
	return current, settings, true, nil
}

func recoveryBlocked(status geodatarecovery.Status) bool {
	return coordinator.RecoveryBlocked() || status.MutationsBlocked ||
		status.Result.Plan.WritesBlocked ||
		status.Result.State.Phase != geodatarecovery.PhaseClean ||
		status.Code != geodatarecovery.RecoveryNone.String()
}

func (service *Service) FinishSuccess(ctx context.Context, operationID string) error {
	return service.finish(ctx, operationID, OperationSucceeded, "geodata_update_succeeded", "")
}

func (service *Service) FinishFailure(ctx context.Context, operationID string, code FailureCode) error {
	if !validFailureCode(code) {
		return ErrInvalidOperationCode
	}
	return service.finish(ctx, operationID, OperationFailed, "", string(code))
}

func (service *Service) FinishRecoveryRequired(ctx context.Context, operationID string) error {
	return service.finish(ctx, operationID, OperationRecoveryRequired, "", "recovery_required")
}

func (service *Service) finish(ctx context.Context, operationID string, state OperationState, resultCode, errorCode string) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	if len(operationID) != 32 {
		return ErrStaleOperation
	}
	now, err := service.timestamp()
	if err != nil {
		return err
	}
	tx, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin geodata operation completion: %w", err)
	}
	defer tx.Rollback()
	var createdAt, updatedAt int64
	err = tx.QueryRowContext(ctx, `SELECT created_at, updated_at FROM geodata_update_operation
		WHERE singleton = 1 AND operation_id = ? AND state = 'running'`, operationID).Scan(&createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStaleOperation
		}
		return fmt.Errorf("read running geodata operation: %w", err)
	}
	if now < updatedAt {
		now = updatedAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE geodata_update_operation SET
		state = ?, result_code = ?, error_code = ?, updated_at = ?, finished_at = ?
		WHERE singleton = 1 AND operation_id = ? AND state = 'running'`,
		string(state), resultCode, errorCode, now, now, operationID)
	if err != nil {
		return fmt.Errorf("finish geodata update operation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm geodata operation completion: %w", err)
	}
	if updated != 1 || updatedAt < createdAt {
		return ErrStaleOperation
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO geodata_update_history(operation_id,report,state,finished_at) VALUES(?,'null',?,?)
 ON CONFLICT(operation_id) DO UPDATE SET state=excluded.state,finished_at=excluded.finished_at`, operationID, string(state), now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit geodata operation completion: %w", err)
	}
	return nil
}

func projectRecovery(status geodatarecovery.Status) RecoveryProjection {
	phase := status.Result.State.Phase.String()
	if phase == "unknown" {
		phase = geodatarecovery.PhaseInspectionFailed.String()
		status.MutationsBlocked = true
	}
	code := status.Code
	if !knownRecoveryCode(code) {
		code = geodatarecovery.ActionRetryInspection
		status.MutationsBlocked = true
	}
	return RecoveryProjection{
		Phase: phase, Code: code,
		MutationsBlocked: status.MutationsBlocked || status.Result.Plan.WritesBlocked,
	}
}

func knownRecoveryCode(code string) bool {
	switch code {
	case geodatarecovery.RecoveryNone.String(),
		geodatarecovery.ActionReviewEvidence, geodatarecovery.ActionReviewDrift,
		geodatarecovery.ActionChooseDirection, geodatarecovery.ActionReviewDomainState,
		geodatarecovery.ActionRetryInspection, geodatarecovery.ActionRetryRecovery:
		return true
	default:
		return false
	}
}

func requestFingerprint(settings VersionedSettings) (string, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("fingerprint geodata update settings: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validIdempotencyKey(key string) bool {
	return api.ValidIdempotencyKey(key)
}

func validFailureCode(code FailureCode) bool {
	switch code {
	case FailureAssetTooLarge, FailureDownloadFailed, FailureChecksumFailed, FailureValidationFailed,
		FailureInstallFailed, FailureRestartFailed, FailureRollbackFailed, FailureUpdateFailed, FailureInterrupted:
		return true
	default:
		for _, process := range []string{"external", "custom", "cycle", "xray"} {
			for _, stage := range []string{"download", "checksum_download", "checksum", "test", "test_installed", "prepare", "resources", "build", "read", "write", "baseline_prepare", "baseline", "lock", "runtime_identity", "save_state", "restore", "restart", "routing", "cleanup"} {
				if string(code) == "geodata_"+process+"_"+stage+"_failed" {
					return true
				}
			}
		}
		return false
	}
}

func cloneSettings(settings Settings) Settings {
	copy := settings
	copy.Origins = append([]string{}, settings.Origins...)
	return copy
}

func (service *Service) timestamp() (int64, error) {
	value := service.clock.Now().UnixMilli()
	if value < 0 || value == math.MaxInt64 {
		return 0, errors.New("geodata operation timestamp is invalid")
	}
	return value, nil
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("geodata administration context is required")
	}
	return ctx.Err()
}

func (s *Service) currentSettings(ctx context.Context) (VersionedSettings, error) {

	cfg, generation, err := s.settings.Geodata(ctx)
	if err != nil {
		return VersionedSettings{}, err
	}
	settings, err := SettingsFromBootstrap(cfg.Geodata)
	settings.Schedule = &api.GeodataSchedule{Enabled: cfg.Scheduler.GeodataUpdateEnabled, Time: cfg.Scheduler.GeodataUpdateTime}
	return VersionedSettings{Generation: generation, Settings: settings}, err
}
