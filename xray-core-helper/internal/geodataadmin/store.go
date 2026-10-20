package geodataadmin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"home-router/xray-core-helper/internal/bootstrap"

	_ "modernc.org/sqlite"
)

const (
	settingsTable  = "geodata_admin_settings"
	originsTable   = "geodata_admin_origin"
	operationTable = "geodata_update_operation"
)

const settingsDDL = `CREATE TABLE geodata_admin_settings (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	generation INTEGER NOT NULL CHECK (generation >= 0),
	geosite_url TEXT NOT NULL CHECK (length(geosite_url) <= 2048),
	geosite_checksum_url TEXT NOT NULL CHECK (length(geosite_checksum_url) <= 2048),
	geosite_checksum_algorithm TEXT NOT NULL CHECK (geosite_checksum_algorithm = 'sha256'),
	geoip_url TEXT NOT NULL CHECK (length(geoip_url) <= 2048),
	geoip_checksum_url TEXT NOT NULL CHECK (length(geoip_checksum_url) <= 2048),
	geoip_checksum_algorithm TEXT NOT NULL CHECK (geoip_checksum_algorithm = 'sha256')
)`

const originsDDL = `CREATE TABLE geodata_admin_origin (
	origin_order INTEGER PRIMARY KEY CHECK (origin_order BETWEEN 0 AND 15),
	origin TEXT NOT NULL UNIQUE CHECK (length(origin) BETWEEN 1 AND 2048),
	settings_singleton INTEGER NOT NULL DEFAULT 1 CHECK (settings_singleton = 1)
		REFERENCES geodata_admin_settings(singleton) ON DELETE CASCADE
)`

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
	Origins []string       `json:"origins"`
	Geosite SourceSettings `json:"geosite"`
	Geoip   SourceSettings `json:"geoip"`
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
	ID                 string         `json:"id"`
	SettingsGeneration uint64         `json:"settingsGeneration"`
	State              OperationState `json:"state"`
	ResultCode         string         `json:"resultCode,omitempty"`
	ErrorCode          string         `json:"errorCode,omitempty"`
	CreatedAt          int64          `json:"createdAt"`
	UpdatedAt          int64          `json:"updatedAt"`
	FinishedAt         *int64         `json:"finishedAt,omitempty"`
}

type snapshot struct {
	Settings  VersionedSettings
	Operation *Operation
}

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string, initial bootstrap.GeodataConfig) (*Store, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errors.New("geodata store context is required")
	}
	seed, err := SettingsFromBootstrap(initial)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("geodata database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create geodata database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open geodata database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	for _, statement := range []string{
		"PRAGMA busy_timeout = 10000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize geodata database: %w", err)
		}
	}
	if err := store.initialize(ctx, seed); err != nil {
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

func (store *Store) initialize(ctx context.Context, seed Settings) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin geodata schema initialization: %w", err)
	}
	defer tx.Rollback()

	existing := 0
	for _, name := range []string{settingsTable, originsTable, operationTable} {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&count); err != nil {
			return fmt.Errorf("inspect geodata schema: %w", err)
		}
		existing += count
	}
	if existing != 0 && existing != 3 {
		return errors.New("unsupported geodata database schema: incomplete administration tables")
	}
	for _, statement := range []string{settingsDDL, originsDDL, operationDDL} {
		if _, err := tx.ExecContext(ctx, strings.Replace(statement, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)); err != nil {
			return fmt.Errorf("create geodata administration schema: %w", err)
		}
	}
	for name, expected := range map[string]string{
		settingsTable: settingsDDL, originsTable: originsDDL, operationTable: operationDDL,
	} {
		var actual string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&actual); err != nil {
			return fmt.Errorf("read geodata schema: %w", err)
		}
		if normalizeSQL(actual) != normalizeSQL(expected) {
			return fmt.Errorf("unsupported geodata database schema for table %q", name)
		}
	}

	versioned, err := readSettings(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		if err := writeSettings(ctx, tx, 0, seed); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("read persisted geodata settings: %w", err)
	} else if err := validateSettings(versioned.Settings); err != nil {
		return fmt.Errorf("persisted geodata settings are invalid: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit geodata schema initialization: %w", err)
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
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return snapshot{}, fmt.Errorf("read geodata settings: %w", err)
	}
	if err := validateSettings(settings.Settings); err != nil {
		return snapshot{}, fmt.Errorf("persisted geodata settings are invalid: %w", err)
	}
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
	return snapshot{Settings: settings, Operation: current}, nil
}

func readSettings(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (VersionedSettings, error) {
	var generation int64
	settings := Settings{Origins: make([]string, 0)}
	if err := queryer.QueryRowContext(ctx, `SELECT generation, geosite_url, geosite_checksum_url,
		geosite_checksum_algorithm, geoip_url, geoip_checksum_url, geoip_checksum_algorithm
		FROM geodata_admin_settings WHERE singleton = 1`).Scan(
		&generation, &settings.Geosite.URL, &settings.Geosite.Checksum.URL,
		&settings.Geosite.Checksum.Algorithm, &settings.Geoip.URL,
		&settings.Geoip.Checksum.URL, &settings.Geoip.Checksum.Algorithm,
	); err != nil {
		return VersionedSettings{}, err
	}
	if generation < 0 {
		return VersionedSettings{}, errors.New("geodata settings generation is invalid")
	}
	rows, err := queryer.QueryContext(ctx, "SELECT origin FROM geodata_admin_origin ORDER BY origin_order")
	if err != nil {
		return VersionedSettings{}, err
	}
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			_ = rows.Close()
			return VersionedSettings{}, err
		}
		settings.Origins = append(settings.Origins, origin)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return VersionedSettings{}, err
	}
	if err := rows.Close(); err != nil {
		return VersionedSettings{}, err
	}
	return VersionedSettings{Generation: uint64(generation), Settings: settings}, nil
}

func writeSettings(ctx context.Context, tx *sql.Tx, generation uint64, settings Settings) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO geodata_admin_settings(
		singleton, generation, geosite_url, geosite_checksum_url, geosite_checksum_algorithm,
		geoip_url, geoip_checksum_url, geoip_checksum_algorithm
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?)`, generation, settings.Geosite.URL,
		settings.Geosite.Checksum.URL, settings.Geosite.Checksum.Algorithm,
		settings.Geoip.URL, settings.Geoip.Checksum.URL, settings.Geoip.Checksum.Algorithm)
	if err != nil {
		return fmt.Errorf("write geodata settings: %w", err)
	}
	for index, origin := range settings.Origins {
		if _, err := tx.ExecContext(ctx, "INSERT INTO geodata_admin_origin(origin_order, origin) VALUES (?, ?)", index, origin); err != nil {
			return fmt.Errorf("write geodata origin: %w", err)
		}
	}
	return nil
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
