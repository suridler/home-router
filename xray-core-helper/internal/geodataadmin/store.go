package geodataadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/bootstrap"

	"home-router/xray-core-helper/internal/sqliteopen"
)

const operationTable = "geodata_update_operation"

const operationDDL = `CREATE TABLE geodata_update_operation (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	operation_id TEXT NOT NULL UNIQUE CHECK (length(operation_id) = 32),
	idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
	request_fingerprint TEXT NOT NULL CHECK (length(request_fingerprint) = 64),
	settings_generation INTEGER NOT NULL CHECK (settings_generation >= 0),
	state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'recovery_required')),
	result_code TEXT NOT NULL CHECK (length(result_code) <= 64),
	error_code TEXT NOT NULL CHECK (length(error_code) <= 64),
	created_at INTEGER NOT NULL CHECK (created_at >= 0),
	updated_at INTEGER NOT NULL CHECK (updated_at >= created_at),
	finished_at INTEGER CHECK (finished_at IS NULL OR finished_at >= created_at),
	CHECK (
		(state = 'running' AND finished_at IS NULL AND result_code = '' AND error_code = '') OR
		(state = 'succeeded' AND finished_at IS NOT NULL AND result_code = 'geodata_update_succeeded' AND error_code = '') OR
		(state = 'failed' AND finished_at IS NOT NULL AND result_code = '' AND length(error_code) > 0) OR
		(state = 'recovery_required' AND finished_at IS NOT NULL AND result_code = '' AND error_code = 'recovery_required')
	)
)`

var (
	ErrInvalidSettings       = errors.New("invalid geodata settings")
	ErrInvalidIdempotencyKey = errors.New("invalid geodata idempotency key")
	ErrGenerationConflict    = errors.New("geodata settings generation conflict")
	ErrIdempotencyConflict   = errors.New("geodata idempotency key conflicts with another request")
	ErrOperationInProgress   = errors.New("a geodata update is already running")
	ErrRecoveryRequired      = errors.New("geodata recovery is required")
	ErrStaleOperation        = errors.New("geodata operation is stale or not running")
	ErrInvalidOperationCode  = errors.New("invalid geodata operation result code")
)

type Settings struct {
	MaxAssetSizeMB int                  `json:"maxAssetSizeMB,omitempty"`
	Builder        string               `json:"builder,omitempty"`
	UserGeosite    string               `json:"userGeosite,omitempty"`
	UserGeoip      string               `json:"userGeoip,omitempty"`
	Schedule       *api.GeodataSchedule `json:"schedule,omitempty"`
	Origins        []string             `json:"origins"`
	Geosite        SourceSettings       `json:"geosite"`
	Geoip          SourceSettings       `json:"geoip"`
}

type SourceSettings struct {
	URL      string           `json:"url"`
	Checksum ChecksumSettings `json:"checksum"`
}

type ChecksumSettings struct {
	URL       string `json:"url"`
	Algorithm string `json:"algorithm"`
}

type VersionedSettings struct {
	Generation uint64   `json:"generation"`
	Settings   Settings `json:"settings"`
}

type OperationState string

const (
	OperationRunning          OperationState = "running"
	OperationSucceeded        OperationState = "succeeded"
	OperationFailed           OperationState = "failed"
	OperationRecoveryRequired OperationState = "recovery_required"
)

type Operation struct {
	Report             *api.GeodataReport `json:"report,omitempty"`
	ID                 string             `json:"id"`
	SettingsGeneration uint64             `json:"settingsGeneration"`
	State              OperationState     `json:"state"`
	ResultCode         string             `json:"resultCode,omitempty"`
	ErrorCode          string             `json:"errorCode,omitempty"`
	CreatedAt          int64              `json:"createdAt"`
	UpdatedAt          int64              `json:"updatedAt"`
	FinishedAt         *int64             `json:"finishedAt,omitempty"`
}

type snapshot struct {
	Operation *Operation
}

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string, initial bootstrap.GeodataConfig) (*Store, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errors.New("geodata store context is required")
	}
	_, err := SettingsFromBootstrap(initial)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("geodata database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create geodata database directory: %w", err)
	}
	db, err := sqliteopen.Open(ctx, path, sqliteopen.Options{BusyTimeout: 10 * time.Second, WAL: true, ForeignKeys: true})
	if err != nil {
		return nil, fmt.Errorf("open geodata database: %w", err)
	}
	store := &Store{db: db}

	if err := store.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure geodata database: %w", err)
	}
	return store, nil
}

func SettingsFromBootstrap(config bootstrap.GeodataConfig) (Settings, error) {
	settings := Settings{
		MaxAssetSizeMB: config.MaxAssetSizeMB, Builder: config.Builder, UserGeosite: config.UserGeosite, UserGeoip: config.UserGeoip,
		Origins: append([]string{}, config.Origins...),
		Geosite: SourceSettings{
			URL: config.Geosite.URL,
			Checksum: ChecksumSettings{
				URL: config.Geosite.Checksum.URL, Algorithm: config.Geosite.Checksum.Algorithm,
			},
		},
		Geoip: SourceSettings{
			URL: config.Geoip.URL,
			Checksum: ChecksumSettings{
				URL: config.Geoip.Checksum.URL, Algorithm: config.Geoip.Checksum.Algorithm,
			},
		},
	}
	if err := validateSettings(settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func (store *Store) initialize(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin geodata schema initialization: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, strings.Replace(operationDDL, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)); err != nil {
		return err
	}
	var actual string
	if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", operationTable).Scan(&actual); err != nil {
		return err
	}
	if normalizeSQL(actual) != normalizeSQL(operationDDL) {
		return errors.New("unsupported geodata operation schema")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS geodata_update_history (
 operation_id TEXT PRIMARY KEY, report TEXT NOT NULL, state TEXT NOT NULL, finished_at INTEGER
 )`); err != nil {
		return err
	}
	// The daemon owns the common runtime lock: a running record belongs to
	// the previous process. Keep its last checkpoint and make retry available.
	interrupted, interruptedErr := readOperation(ctx, tx)
	if interruptedErr != nil && !errors.Is(interruptedErr, sql.ErrNoRows) {
		return interruptedErr
	}
	if interruptedErr == nil && interrupted.State == OperationRunning {
		report := interrupted.Report
		if report == nil {
			report = &api.GeodataReport{}
		}
		report.Issues = append(report.Issues, api.GeodataIssue{Process: report.Process, Stage: report.Stage, Code: "geodata_interrupted", Message: "Предыдущее выполнение прервано; файлы автоматически не откатывались", At: interrupted.UpdatedAt})
		encoded, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO geodata_update_history(operation_id,report,state,finished_at) VALUES(?,?,'failed',?)
   ON CONFLICT(operation_id) DO UPDATE SET report=excluded.report,state='failed',finished_at=excluded.finished_at`, interrupted.ID, string(encoded), interrupted.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE geodata_update_operation SET state='failed',error_code='geodata_interrupted',finished_at=updated_at WHERE operation_id=? AND state='running'`, interrupted.ID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit geodata schema initialization: %w", err)
	}
	if interruptedErr == nil && interrupted.State == OperationRunning {
		log.Printf("geodata operation=%s code=geodata_interrupted: previous execution interrupted; inspect operation status", interrupted.ID)
	}
	return nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) readSnapshot(ctx context.Context) (snapshot, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot{}, fmt.Errorf("begin geodata snapshot: %w", err)
	}
	defer tx.Rollback()
	operation, err := readOperation(ctx, tx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot{}, fmt.Errorf("read geodata update operation: %w", err)
	}
	var current *Operation
	if err == nil {
		current = &operation
	}
	if err := tx.Commit(); err != nil {
		return snapshot{}, fmt.Errorf("commit geodata snapshot: %w", err)
	}
	return snapshot{Operation: current}, nil
}

func readOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Operation, error) {
	var operation Operation
	var state string
	var finished sql.NullInt64
	err := queryer.QueryRowContext(ctx, `SELECT operation_id, settings_generation, state, result_code,
		error_code, created_at, updated_at, finished_at
		FROM geodata_update_operation WHERE singleton = 1`).Scan(
		&operation.ID, &operation.SettingsGeneration, &state, &operation.ResultCode,
		&operation.ErrorCode, &operation.CreatedAt, &operation.UpdatedAt, &finished,
	)
	if err != nil {
		return Operation{}, err
	}
	operation.State = OperationState(state)
	if finished.Valid {
		operation.FinishedAt = &finished.Int64
	}
	var reportJSON string
	if err := queryer.QueryRowContext(ctx, "SELECT report FROM geodata_update_history WHERE operation_id=?", operation.ID).Scan(&reportJSON); err == nil && reportJSON != "null" {
		if err := json.Unmarshal([]byte(reportJSON), &operation.Report); err != nil {
			return Operation{}, err
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	return operation, nil
}

func validateSettings(settings Settings) error {
	if len(settings.Origins) > 16 {
		return ErrInvalidSettings
	}
	for _, origin := range settings.Origins {
		if len(origin) > 2048 {
			return ErrInvalidSettings
		}
	}
	for _, source := range []SourceSettings{settings.Geosite, settings.Geoip} {
		if len(source.URL) > 2048 || len(source.Checksum.URL) > 2048 {
			return ErrInvalidSettings
		}
	}
	config := bootstrap.GeodataConfig{
		Origins: append([]string{}, settings.Origins...),
		Geosite: bootstrap.GeodataSourceConfig{
			URL: settings.Geosite.URL,
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: settings.Geosite.Checksum.URL, Algorithm: settings.Geosite.Checksum.Algorithm,
			},
		},
		Geoip: bootstrap.GeodataSourceConfig{
			URL: settings.Geoip.URL,
			Checksum: bootstrap.GeodataChecksumConfig{
				URL: settings.Geoip.Checksum.URL, Algorithm: settings.Geoip.Checksum.Algorithm,
			},
		},
	}
	if err := config.Validate(); err != nil {
		return fmt.Errorf("%w: source restrictions failed", ErrInvalidSettings)
	}
	return nil
}

func normalizeSQL(statement string) string {
	return strings.ToLower(strings.Join(strings.Fields(statement), " "))
}

// Reports and history contain execution state only. Legacy settings tables, if
// present, are intentionally ignored rather than imported or silently removed.
func (s *Store) saveReport(ctx context.Context, id string, report *api.GeodataReport) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO geodata_update_history(operation_id,report,state,finished_at) VALUES(?,?,'running',NULL)
 ON CONFLICT(operation_id) DO UPDATE SET report=excluded.report`, id, string(encoded))
	return err
}
