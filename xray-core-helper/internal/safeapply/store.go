// Package safeapply owns durable confirmed-apply state.
package safeapply

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"home-router/xray-core-helper/internal/templatecatalog"
)

type Phase string

const (
	PhasePrepared       Phase = "prepared"
	PhaseApplied        Phase = "applied"
	PhaseConfirming     Phase = "confirming"
	PhaseRollingBack    Phase = "rolling_back"
	PhaseAwaitingReboot Phase = "awaiting_reboot"
)

type State string

const (
	StatePending         State = "pending"
	StateConfirmed       State = "confirmed"
	StateRolledBack      State = "rolled_back"
	StateRebootCancelled State = "reboot_cancelled"
)

var (
	ErrBusy     = errors.New("safe apply already pending")
	ErrNotFound = errors.New("safe apply not found")
	ErrStale    = errors.New("safe apply transaction is stale")
	ErrPhase    = errors.New("safe apply phase conflict")
)

type Transaction struct {
	ID            string             `json:"transactionId"`
	TemplateID    templatecatalog.ID `json:"templateId"`
	BackupID      string             `json:"backupId"`
	CandidateHash string             `json:"candidateHash"`
	Deadline      time.Time          `json:"deadline"`
	Phase         Phase              `json:"phase"`
	State         State              `json:"state"`
	Generation    uint64             `json:"generation"`
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("safe apply database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create safe apply database directory: %w", err)
	}
	// The grouped journal contains original native file bytes. Create privately
	// before SQLite opens WAL files; never follow a symlink or open a FIFO.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("private safe apply database unavailable")
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("invalid safe apply database")
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open safe apply database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, now: time.Now}
	for _, statement := range []string{"PRAGMA busy_timeout = 10000", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = FULL", `CREATE TABLE IF NOT EXISTS safe_apply_transaction (
		transaction_id TEXT PRIMARY KEY, template_id TEXT NOT NULL, backup_id TEXT NOT NULL,
		candidate_hash TEXT NOT NULL, deadline_ns INTEGER NOT NULL, phase TEXT NOT NULL,
		state TEXT NOT NULL, generation INTEGER NOT NULL CHECK(generation >= 0)
	)`, `CREATE UNIQUE INDEX IF NOT EXISTS safe_apply_one_pending ON safe_apply_transaction(state) WHERE state = 'pending'`,
		`CREATE TABLE IF NOT EXISTS safe_apply_reboot (transaction_id TEXT PRIMARY KEY, boot_id TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS safe_apply_baseline (
		transaction_id TEXT NOT NULL, template_id TEXT NOT NULL, present INTEGER NOT NULL CHECK(present IN (0,1)),
		content BLOB NOT NULL, mode INTEGER NOT NULL, uid INTEGER NOT NULL, gid INTEGER NOT NULL,
		PRIMARY KEY(transaction_id, template_id)
		)`, `CREATE TABLE IF NOT EXISTS safe_apply_restore (
		transaction_id TEXT NOT NULL, template_id TEXT NOT NULL,
		PRIMARY KEY(transaction_id, template_id))`} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize safe apply database: %w", err)
		}
	}
	return store, nil
}
func (store *Store) Close() error { return store.db.Close() }

func (store *Store) Begin(ctx context.Context, id templatecatalog.ID, backupID, candidateHash string, generation uint64, deadline time.Time) (Transaction, error) {
	if _, err := templatecatalog.Default().Lookup(id); err != nil || backupID == "" || len(candidateHash) != 64 || !deadline.After(store.now()) {
		return Transaction{}, ErrPhase
	}
	transactionID, err := randomID()
	if err != nil {
		return Transaction{}, err
	}
	transaction := Transaction{ID: transactionID, TemplateID: id, BackupID: backupID, CandidateHash: candidateHash, Deadline: deadline.UTC(), Phase: PhasePrepared, State: StatePending, Generation: generation}
	_, err = store.db.ExecContext(ctx, `INSERT INTO safe_apply_transaction(transaction_id,template_id,backup_id,candidate_hash,deadline_ns,phase,state,generation) VALUES(?,?,?,?,?,?,?,?)`, transaction.ID, transaction.TemplateID, transaction.BackupID, transaction.CandidateHash, transaction.Deadline.UnixNano(), transaction.Phase, transaction.State, transaction.Generation)
	if err != nil {
		if pending, _ := store.Pending(ctx); pending != nil {
			return Transaction{}, ErrBusy
		}
		return Transaction{}, fmt.Errorf("record safe apply: %w", err)
	}
	return transaction, nil
}

func (store *Store) Pending(ctx context.Context) (*Transaction, error) {
	row := store.db.QueryRowContext(ctx, `SELECT transaction_id,template_id,backup_id,candidate_hash,deadline_ns,phase,state,generation FROM safe_apply_transaction WHERE state='pending'`)
	transaction, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending safe apply: %w", err)
	}
	return &transaction, nil
}

func (store *Store) Advance(ctx context.Context, id string, from, to Phase) (Transaction, error) {
	valid := (from == PhasePrepared && to == PhaseApplied) || (from == PhaseApplied && to == PhaseConfirming) || ((from == PhasePrepared || from == PhaseApplied || from == PhaseConfirming) && to == PhaseRollingBack)
	if !valid {
		return Transaction{}, ErrPhase
	}
	result, err := store.db.ExecContext(ctx, `UPDATE safe_apply_transaction SET phase=? WHERE transaction_id=? AND state='pending' AND phase=?`, to, id, from)
	if err != nil {
		return Transaction{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Transaction{}, ErrPhase
	}
	return store.byID(ctx, id)
}

func (store *Store) Resolve(ctx context.Context, id string, state State) (Transaction, bool, error) {
	if state != StateConfirmed && state != StateRolledBack && state != StateRebootCancelled {
		return Transaction{}, false, ErrPhase
	}
	pending, err := store.Pending(ctx)
	if err != nil {
		return Transaction{}, false, err
	}
	if pending != nil && pending.ID != id {
		return Transaction{}, false, ErrStale
	}
	if pending == nil {
		existing, err := store.byID(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return Transaction{}, false, err
		}
		if err != nil {
			return Transaction{}, false, err
		}
		if existing.State == state {
			return existing, false, nil
		}
		return Transaction{}, false, ErrStale
	}
	result, err := store.db.ExecContext(ctx, `UPDATE safe_apply_transaction SET state=? WHERE transaction_id=? AND state='pending'`, state, id)
	if err != nil {
		return Transaction{}, false, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Transaction{}, false, ErrStale
	}
	resolved, err := store.byID(ctx, id)
	return resolved, true, err
}

func (store *Store) byID(ctx context.Context, id string) (Transaction, error) {
	transaction, err := scan(store.db.QueryRowContext(ctx, `SELECT transaction_id,template_id,backup_id,candidate_hash,deadline_ns,phase,state,generation FROM safe_apply_transaction WHERE transaction_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, ErrNotFound
	}
	return transaction, err
}

// awaitReboot atomically records restored-file completion and the originating
// boot. Keeping the transaction pending preserves the existing mutation gate.
func (store *Store) awaitReboot(ctx context.Context, id, bootID string) (Transaction, error) {
	if !validBootID(bootID) {
		return Transaction{}, ErrPhase
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Transaction{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE safe_apply_transaction SET phase=? WHERE transaction_id=? AND state='pending' AND phase=?`, PhaseAwaitingReboot, id, PhaseRollingBack)
	if err != nil {
		return Transaction{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return Transaction{}, ErrPhase
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO safe_apply_reboot(transaction_id,boot_id) VALUES(?,?)`, id, bootID); err != nil {
		return Transaction{}, err
	}
	if err = tx.Commit(); err != nil {
		return Transaction{}, err
	}
	return store.byID(ctx, id)
}

func (store *Store) rebootBootID(ctx context.Context, id string) (string, error) {
	var bootID string
	if err := store.db.QueryRowContext(ctx, `SELECT boot_id FROM safe_apply_reboot WHERE transaction_id=?`, id).Scan(&bootID); err != nil {
		return "", err
	}
	if !validBootID(bootID) {
		return "", ErrPhase
	}
	return bootID, nil
}

// requireRestore invalidates a previous reboot-ready assertion when a later
// boot finds changed files. Keep evidence and require another full restoration.
func (store *Store) requireRestore(ctx context.Context, id string) (Transaction, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Transaction{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE safe_apply_transaction SET phase=? WHERE transaction_id=? AND state='pending' AND phase=?`, PhaseRollingBack, id, PhaseAwaitingReboot)
	if err != nil {
		return Transaction{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return Transaction{}, ErrPhase
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM safe_apply_reboot WHERE transaction_id=?`, id); err != nil {
		return Transaction{}, err
	}
	if err = tx.Commit(); err != nil {
		return Transaction{}, err
	}
	return store.byID(ctx, id)
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Transaction, error) {
	var transaction Transaction
	var deadline int64
	err := row.Scan(&transaction.ID, &transaction.TemplateID, &transaction.BackupID, &transaction.CandidateHash, &deadline, &transaction.Phase, &transaction.State, &transaction.Generation)
	transaction.Deadline = time.Unix(0, deadline).UTC()
	return transaction, err
}
func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
