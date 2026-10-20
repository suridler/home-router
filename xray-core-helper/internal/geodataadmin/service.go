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
	Recovery RecoveryProvider
	Clock    Clock
}

type Service struct {
	store    *Store
	recovery RecoveryProvider
	clock    Clock
}

type RecoveryProjection struct {
	Phase            string `json:"phase"`
	Code             string `json:"code"`
	MutationsBlocked bool   `json:"mutationsBlocked"`
}

type Status struct {
	Settings  VersionedSettings  `json:"settings"`
	Operation *Operation         `json:"operation,omitempty"`
	Recovery  RecoveryProjection `json:"recovery"`
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
)

func NewService(options ServiceOptions) (*Service, error) {
	if options.Store == nil || options.Recovery == nil {
		return nil, errors.New("geodata administration dependencies are required")
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{store: options.Store, recovery: options.Recovery, clock: options.Clock}, nil
}

func (service *Service) Settings(ctx context.Context) (VersionedSettings, error) {
	if err := validContext(ctx); err != nil {
		return VersionedSettings{}, err
	}
	state, err := service.store.readSnapshot(ctx)
	if err != nil {
		return VersionedSettings{}, err
	}
	return state.Settings, nil
}

func (service *Service) Status(ctx context.Context) (Status, error) {
	if err := validContext(ctx); err != nil {
		return Status{}, err
	}
	state, err := service.store.readSnapshot(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Settings: state.Settings, Operation: state.Operation,
		Recovery: projectRecovery(service.recovery.Status()),
	}, nil
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
	if expectedGeneration >= math.MaxInt64 {
		return VersionedSettings{}, ErrGenerationConflict
	}
	tx, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		return VersionedSettings{}, fmt.Errorf("begin geodata settings replacement: %w", err)
	}
	defer tx.Rollback()
	current, err := readSettings(ctx, tx)
	if err != nil {
		return VersionedSettings{}, fmt.Errorf("read geodata settings for replacement: %w", err)
	}
	if current.Generation != expectedGeneration {
		return VersionedSettings{}, ErrGenerationConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE geodata_admin_settings SET
		generation = ?, geosite_url = ?, geosite_checksum_url = ?, geosite_checksum_algorithm = ?,
		geoip_url = ?, geoip_checksum_url = ?, geoip_checksum_algorithm = ?
		WHERE singleton = 1 AND generation = ?`,
		expectedGeneration+1, settings.Geosite.URL, settings.Geosite.Checksum.URL,
		settings.Geosite.Checksum.Algorithm, settings.Geoip.URL,
		settings.Geoip.Checksum.URL, settings.Geoip.Checksum.Algorithm, expectedGeneration)
	if err != nil {
		return VersionedSettings{}, fmt.Errorf("replace geodata settings: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return VersionedSettings{}, fmt.Errorf("confirm geodata settings replacement: %w", err)
	}
	if rows != 1 {
		return VersionedSettings{}, ErrGenerationConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM geodata_admin_origin WHERE settings_singleton = 1"); err != nil {
		return VersionedSettings{}, fmt.Errorf("replace geodata origins: %w", err)
	}
	for index, origin := range settings.Origins {
		if _, err := tx.ExecContext(ctx, "INSERT INTO geodata_admin_origin(origin_order, origin) VALUES (?, ?)", index, origin); err != nil {
			return VersionedSettings{}, fmt.Errorf("replace geodata origins: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return VersionedSettings{}, fmt.Errorf("commit geodata settings replacement: %w", err)
	}
	return VersionedSettings{Generation: expectedGeneration + 1, Settings: cloneSettings(settings)}, nil
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
	settings, err := readSettings(ctx, tx)
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
	settings, err := readSettings(ctx, tx)
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
		FailureInstallFailed, FailureRestartFailed, FailureRollbackFailed, FailureUpdateFailed:
		return true
	default:
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
