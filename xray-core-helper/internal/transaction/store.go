package transaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"home-router/xray-core-helper/internal/sqliteopen"
)

var (
	ErrStaleGeneration = errors.New("expected generation is stale")
	ErrStateDrift      = errors.New("configuration hash differs from committed state")
	ErrTransactionBusy = errors.New("a configuration transaction is already pending")
)

type Metadata struct {
	Generation  uint64
	ContentHash string
}

type Pending struct {
	ID                 string
	Phase              string
	PreviousGeneration uint64
	PreviousHash       string
	NextHash           string
	CandidatePath      string
	BackupPath         string
	RestartRequired    bool
}

type Store struct {
	db *sql.DB
}

func OpenStore(ctx context.Context, path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("transaction database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create transaction database directory: %w", err)
	}
	db, err := sqliteopen.Open(ctx, path, sqliteopen.Options{BusyTimeout: 10 * time.Second, WAL: true, ForeignKeys: true})
	if err != nil {
		return nil, fmt.Errorf("open transaction database: %w", err)
	}
	store := &Store{db: db}

	if err := store.createSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure transaction database: %w", err)
	}
	return store, nil
}

func (store *Store) createSchema(ctx context.Context) error {
	var version int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read transaction schema version: %w", err)
	}
	if version > 1 {
		return fmt.Errorf("transaction database schema version %d is newer than supported version 1", version)
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS config_state (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			generation INTEGER NOT NULL CHECK (generation >= 0),
			content_hash TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS config_transaction (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			transaction_id TEXT NOT NULL UNIQUE,
			phase TEXT NOT NULL CHECK (phase IN ('staged', 'replaced', 'restart_started')),
			previous_generation INTEGER NOT NULL CHECK (previous_generation >= 0),
			previous_hash TEXT NOT NULL,
			next_hash TEXT NOT NULL,
			candidate_path TEXT NOT NULL,
			backup_path TEXT NOT NULL,
			restart_required INTEGER NOT NULL CHECK (restart_required IN (0, 1))
		)`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create transaction schema: %w", err)
		}
	}
	if version == 0 {
		if _, err := store.db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return fmt.Errorf("set transaction schema version: %w", err)
		}
	}
	return nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) Initialize(ctx context.Context, contentHash string) (Metadata, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("begin configuration state initialization: %w", err)
	}
	defer tx.Rollback()
	metadata, err := readMetadata(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO config_state(singleton, generation, content_hash) VALUES (1, 0, ?)", contentHash); err != nil {
			return Metadata{}, fmt.Errorf("initialize configuration state: %w", err)
		}
		metadata = Metadata{ContentHash: contentHash}
	} else if err != nil {
		return Metadata{}, fmt.Errorf("read configuration state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Metadata{}, fmt.Errorf("commit configuration state initialization: %w", err)
	}
	return metadata, nil
}

func (store *Store) Metadata(ctx context.Context) (Metadata, error) {
	metadata, err := readMetadata(ctx, store.db)
	if err != nil {
		return Metadata{}, fmt.Errorf("read configuration metadata: %w", err)
	}
	return metadata, nil
}

func (store *Store) ConfigMetadata(ctx context.Context) (uint64, string, error) {
	metadata, err := store.Metadata(ctx)
	if err != nil {
		return 0, "", err
	}
	return metadata.Generation, metadata.ContentHash, nil
}

func (store *Store) SavePending(ctx context.Context, pending Pending) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction journal write: %w", err)
	}
	defer tx.Rollback()
	metadata, err := readMetadata(ctx, tx)
	if err != nil {
		return fmt.Errorf("read committed configuration state: %w", err)
	}
	if metadata.Generation != pending.PreviousGeneration {
		return ErrStaleGeneration
	}
	if metadata.ContentHash != pending.PreviousHash {
		return ErrStateDrift
	}
	if pending.PreviousGeneration >= math.MaxInt64 {
		return errors.New("configuration generation is exhausted")
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM config_transaction WHERE singleton = 1").Scan(&exists); err != nil {
		return fmt.Errorf("check transaction journal: %w", err)
	}
	if exists != 0 {
		return ErrTransactionBusy
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO config_transaction(
		singleton, transaction_id, phase, previous_generation, previous_hash, next_hash,
		candidate_path, backup_path, restart_required
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)`, pending.ID, pending.Phase, pending.PreviousGeneration,
		pending.PreviousHash, pending.NextHash, pending.CandidatePath, pending.BackupPath,
		boolInt(pending.RestartRequired)); err != nil {
		return fmt.Errorf("write transaction journal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction journal: %w", err)
	}
	return nil
}

func (store *Store) Pending(ctx context.Context) (*Pending, error) {
	var pending Pending
	var restartRequired int
	err := store.db.QueryRowContext(ctx, `SELECT transaction_id, phase, previous_generation,
		previous_hash, next_hash, candidate_path, backup_path, restart_required
		FROM config_transaction WHERE singleton = 1`).Scan(
		&pending.ID, &pending.Phase, &pending.PreviousGeneration, &pending.PreviousHash,
		&pending.NextHash, &pending.CandidatePath, &pending.BackupPath, &restartRequired,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read transaction journal: %w", err)
	}
	pending.RestartRequired = restartRequired != 0
	return &pending, nil
}

func (store *Store) Advance(ctx context.Context, id, expectedPhase, nextPhase string) error {
	if !((expectedPhase == "staged" && nextPhase == "replaced") ||
		(expectedPhase == "replaced" && nextPhase == "restart_started")) {
		return errors.New("invalid configuration transaction phase transition")
	}
	result, err := store.db.ExecContext(ctx, "UPDATE config_transaction SET phase = ? WHERE singleton = 1 AND transaction_id = ? AND phase = ?", nextPhase, id, expectedPhase)
	if err != nil {
		return fmt.Errorf("advance transaction phase: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm transaction phase: %w", err)
	}
	if rows != 1 {
		return ErrTransactionBusy
	}
	return nil
}

func (store *Store) CommitPending(ctx context.Context, id string) (Metadata, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("begin transaction commit: %w", err)
	}
	defer tx.Rollback()
	pending, err := readPending(ctx, tx)
	if err != nil {
		return Metadata{}, fmt.Errorf("read pending transaction: %w", err)
	}
	if pending.ID != id {
		return Metadata{}, ErrTransactionBusy
	}
	expectedPhase := "replaced"
	if pending.RestartRequired {
		expectedPhase = "restart_started"
	}
	if pending.Phase != expectedPhase {
		return Metadata{}, ErrTransactionBusy
	}
	result, err := tx.ExecContext(ctx, `UPDATE config_state SET generation = ?, content_hash = ?
		WHERE singleton = 1 AND generation = ? AND content_hash = ?`,
		pending.PreviousGeneration+1, pending.NextHash, pending.PreviousGeneration, pending.PreviousHash)
	if err != nil {
		return Metadata{}, fmt.Errorf("commit configuration generation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Metadata{}, fmt.Errorf("confirm configuration generation: %w", err)
	}
	if rows != 1 {
		return Metadata{}, ErrStateDrift
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM config_transaction WHERE singleton = 1 AND transaction_id = ?", id); err != nil {
		return Metadata{}, fmt.Errorf("clear committed transaction journal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Metadata{}, fmt.Errorf("commit transaction state: %w", err)
	}
	return Metadata{Generation: pending.PreviousGeneration + 1, ContentHash: pending.NextHash}, nil
}

func (store *Store) ClearPending(ctx context.Context, id string) error {
	result, err := store.db.ExecContext(ctx, "DELETE FROM config_transaction WHERE singleton = 1 AND transaction_id = ?", id)
	if err != nil {
		return fmt.Errorf("clear transaction journal: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm transaction journal removal: %w", err)
	}
	if rows != 1 {
		return ErrTransactionBusy
	}
	return nil
}

func (store *Store) Adopt(ctx context.Context, expectedGeneration uint64, contentHash string) (Metadata, error) {
	if expectedGeneration >= math.MaxInt64 {
		return Metadata{}, ErrStaleGeneration
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("begin configuration adopt: %w", err)
	}
	defer tx.Rollback()
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM config_transaction WHERE singleton = 1").Scan(&pending); err != nil {
		return Metadata{}, fmt.Errorf("check pending transaction before adopt: %w", err)
	}
	if pending != 0 {
		return Metadata{}, ErrTransactionBusy
	}
	result, err := tx.ExecContext(ctx, `UPDATE config_state SET generation = generation + 1, content_hash = ?
		WHERE singleton = 1 AND generation = ?`, contentHash, expectedGeneration)
	if err != nil {
		return Metadata{}, fmt.Errorf("adopt external configuration: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Metadata{}, fmt.Errorf("confirm configuration adopt: %w", err)
	}
	if rows != 1 {
		return Metadata{}, ErrStaleGeneration
	}
	if err := tx.Commit(); err != nil {
		return Metadata{}, fmt.Errorf("commit configuration adopt: %w", err)
	}
	return Metadata{Generation: expectedGeneration + 1, ContentHash: contentHash}, nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readMetadata(ctx context.Context, queryer querier) (Metadata, error) {
	var metadata Metadata
	var generation int64
	if err := queryer.QueryRowContext(ctx, "SELECT generation, content_hash FROM config_state WHERE singleton = 1").Scan(&generation, &metadata.ContentHash); err != nil {
		return Metadata{}, err
	}
	if generation < 0 {
		return Metadata{}, errors.New("configuration generation is invalid")
	}
	metadata.Generation = uint64(generation)
	return metadata, nil
}

func readPending(ctx context.Context, queryer querier) (Pending, error) {
	var pending Pending
	var restartRequired int
	err := queryer.QueryRowContext(ctx, `SELECT transaction_id, phase, previous_generation,
		previous_hash, next_hash, candidate_path, backup_path, restart_required
		FROM config_transaction WHERE singleton = 1`).Scan(
		&pending.ID, &pending.Phase, &pending.PreviousGeneration, &pending.PreviousHash,
		&pending.NextHash, &pending.CandidatePath, &pending.BackupPath, &restartRequired,
	)
	pending.RestartRequired = restartRequired != 0
	return pending, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
