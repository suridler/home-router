package domainstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"home-router/xray-core-helper/internal/domain"
	"home-router/xray-core-helper/internal/sqliteopen"
)

const schemaVersion = 2
const previousSchemaVersion = 1

const (
	schemaTable  = "proxy_domain_schema"
	stateTable   = "proxy_domain_state"
	controlTable = "proxy_domain_control"
)

const schemaDDL = `CREATE TABLE proxy_domain_schema (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	version INTEGER NOT NULL CHECK (version = 2)
)`

const previousSchemaDDL = `CREATE TABLE proxy_domain_schema (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	version INTEGER NOT NULL CHECK (version = 1)
)`

const stateDDL = `CREATE TABLE proxy_domain_state (
	domain TEXT PRIMARY KEY NOT NULL CHECK (length(domain) BETWEEN 1 AND 253),
	desired INTEGER NOT NULL CHECK (desired IN (0, 1)),
	baseline INTEGER NOT NULL CHECK (baseline IN (0, 1)),
	CHECK (desired = 1 OR baseline = 1)
)`

const controlDDL = `CREATE TABLE proxy_domain_control (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	consolidation_active INTEGER NOT NULL CHECK (consolidation_active IN (0, 1))
)`

var (
	ErrPreparedDomainsMismatch = errors.New("prepared domain snapshot does not match desired domains")
	ErrConsolidationNotActive  = errors.New("domain consolidation is not active")
)

type Store struct {
	db *sql.DB
}

type Snapshot struct {
	Desired             []string `json:"desired"`
	Baseline            []string `json:"baseline"`
	ConsolidationActive bool     `json:"consolidationActive"`
}

func Open(ctx context.Context, path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("domain database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create domain database directory: %w", err)
	}
	db, err := sqliteopen.Open(ctx, path, sqliteopen.Options{})
	if err != nil {
		return nil, fmt.Errorf("open domain database: %w", err)
	}
	store := &Store{db: db}
	if err := store.initializeSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, pendingBaselineDDL); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := os.Chmod(path, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure domain database: %w", err)
	}
	return store, nil
}

func (store *Store) initializeSchema(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin domain schema validation: %w", err)
	}
	defer tx.Rollback()

	objects, err := schemaObjects(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect domain database schema: %w", err)
	}
	hasSchema := false
	hasState := false
	hasControl := false
	for _, name := range objects {
		switch name {
		case schemaTable:
			hasSchema = true
		case stateTable:
			hasState = true
		case controlTable:
			hasControl = true
		}
	}

	if !hasSchema && !hasState && !hasControl {
		if _, err := tx.ExecContext(ctx, schemaDDL); err != nil {
			return fmt.Errorf("create domain schema version table: %w", err)
		}
		if _, err := tx.ExecContext(ctx, stateDDL); err != nil {
			return fmt.Errorf("create domain state table: %w", err)
		}
		if _, err := tx.ExecContext(ctx, controlDDL); err != nil {
			return fmt.Errorf("create domain control table: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO proxy_domain_schema(singleton, version) VALUES (1, ?)", schemaVersion); err != nil {
			return fmt.Errorf("record domain schema version: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO proxy_domain_control(singleton, consolidation_active) VALUES (1, 0)"); err != nil {
			return fmt.Errorf("initialize domain consolidation state: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit domain schema creation: %w", err)
		}
		return nil
	}
	if !hasSchema || !hasState {
		return errors.New("unsupported domain database schema: incomplete proxy-domain tables")
	}
	if err := validateTableSchema(ctx, tx, stateTable, stateDDL); err != nil {
		return err
	}
	if hasControl {
		if err := validateTableSchema(ctx, tx, schemaTable, schemaDDL); err != nil {
			return err
		}
		if err := validateTableSchema(ctx, tx, controlTable, controlDDL); err != nil {
			return err
		}
		var singleton, version int
		if err := tx.QueryRowContext(ctx, "SELECT singleton, version FROM proxy_domain_schema").Scan(&singleton, &version); err != nil {
			return fmt.Errorf("read domain schema version: %w", err)
		}
		if singleton != 1 || version != schemaVersion {
			return fmt.Errorf("unsupported domain database schema version %d", version)
		}
		if err := validateSchemaVersionRows(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit domain schema validation: %w", err)
		}
		return nil
	}
	if err := validateTableSchema(ctx, tx, schemaTable, previousSchemaDDL); err != nil {
		return err
	}
	var singleton, version int
	if err := tx.QueryRowContext(ctx, "SELECT singleton, version FROM proxy_domain_schema").Scan(&singleton, &version); err != nil {
		return fmt.Errorf("read domain schema version: %w", err)
	}
	if singleton != 1 || version != previousSchemaVersion {
		return fmt.Errorf("unsupported domain database schema version %d", version)
	}
	if err := validateSchemaVersionRows(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, controlDDL); err != nil {
		return fmt.Errorf("create domain control table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO proxy_domain_control(singleton, consolidation_active) VALUES (1, 0)"); err != nil {
		return fmt.Errorf("initialize domain consolidation state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE proxy_domain_schema"); err != nil {
		return fmt.Errorf("replace domain schema version table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("create domain schema version table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO proxy_domain_schema(singleton, version) VALUES (1, ?)", schemaVersion); err != nil {
		return fmt.Errorf("record domain schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit domain schema validation: %w", err)
	}
	return nil
}

func validateSchemaVersionRows(ctx context.Context, queryer schemaQuerier) error {
	var rows int
	if err := queryer.QueryRowContext(ctx, "SELECT count(*) FROM proxy_domain_schema").Scan(&rows); err != nil {
		return fmt.Errorf("validate domain schema version rows: %w", err)
	}
	if rows != 1 {
		return errors.New("unsupported domain database schema: expected one version row")
	}
	return nil
}

type schemaQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func schemaObjects(ctx context.Context, queryer schemaQuerier) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' AND type IN ('table', 'index', 'view', 'trigger')
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		objects = append(objects, name)
	}
	return objects, rows.Err()
}

func validateTableSchema(ctx context.Context, queryer schemaQuerier, name, expected string) error {
	var actual string
	if err := queryer.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&actual); err != nil {
		return fmt.Errorf("read domain table schema: %w", err)
	}
	if normalizeSQL(actual) != normalizeSQL(expected) {
		return fmt.Errorf("unsupported domain database schema for table %q", name)
	}
	return nil
}

func normalizeSQL(statement string) string {
	return strings.ToLower(strings.Join(strings.Fields(statement), " "))
}

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) AddDesired(ctx context.Context, value string) error {
	name, _, err := domain.NormalizeResource(value)
	if err != nil {
		return err
	}
	return store.updateDesired(ctx, name, true)
}

func (store *Store) DeleteDesired(ctx context.Context, value string) error {
	name, _, err := domain.NormalizeResource(value)
	if err != nil {
		return err
	}
	return store.updateDesired(ctx, name, false)
}

func (store *Store) updateDesired(ctx context.Context, name string, desired bool) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin desired-domain mutation: %w", err)
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT consolidation_active FROM proxy_domain_control WHERE singleton = 1").Scan(&active); err != nil {
		return fmt.Errorf("read domain consolidation state: %w", err)
	}
	if active == 1 {
		return fmt.Errorf("mutate desired domains: %w", domain.ErrConsolidationActive)
	}
	if desired {
		_, err = tx.ExecContext(ctx, `INSERT INTO proxy_domain_state(domain, desired, baseline) VALUES (?, 1, 0)
			ON CONFLICT(domain) DO UPDATE SET desired = 1`, name)
	} else {
		if _, err = tx.ExecContext(ctx, "DELETE FROM proxy_domain_state WHERE domain = ? AND baseline = 0", name); err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE proxy_domain_state SET desired = 0 WHERE domain = ? AND baseline = 1 AND desired = 1", name)
		}
	}
	if err != nil {
		return fmt.Errorf("update desired domain: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit desired-domain mutation: %w", err)
	}
	return nil
}

func (store *Store) SetConsolidationActive(ctx context.Context, active bool) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin domain consolidation update: %w", err)
	}
	defer tx.Rollback()
	value := 0
	if active {
		value = 1
	}
	if _, err := tx.ExecContext(ctx, "UPDATE proxy_domain_control SET consolidation_active = ? WHERE singleton = 1", value); err != nil {
		return fmt.Errorf("update domain consolidation state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit domain consolidation update: %w", err)
	}
	return nil
}

// BeginConsolidation freezes desired-domain mutations and returns the desired
// set from the same SQLite transaction.
func (store *Store) BeginConsolidation(ctx context.Context) (Snapshot, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin domain consolidation: %w", err)
	}
	defer tx.Rollback()

	var active int
	if err := tx.QueryRowContext(ctx, "SELECT consolidation_active FROM proxy_domain_control WHERE singleton = 1").Scan(&active); err != nil {
		return Snapshot{}, fmt.Errorf("read domain consolidation state: %w", err)
	}
	if active == 1 {
		return Snapshot{}, fmt.Errorf("begin domain consolidation: %w", domain.ErrConsolidationActive)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE proxy_domain_control SET consolidation_active = 1 WHERE singleton = 1"); err != nil {
		return Snapshot{}, fmt.Errorf("freeze desired-domain mutations: %w", err)
	}
	snapshot, err := snapshotTx(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit domain consolidation: %w", err)
	}
	return snapshot, nil
}

// FinalizeConsolidation atomically commits the prepared desired set as baseline
// and releases the consolidation freeze only when the set is still unchanged.
func (store *Store) FinalizeConsolidation(ctx context.Context, prepared []string) error {
	names, ips, err := domain.SplitResources(prepared)
	normalized := append(names, ips...)
	sort.Strings(normalized)
	if err != nil || !equalDomains(normalized, prepared) {
		return ErrPreparedDomainsMismatch
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin domain consolidation finalization: %w", err)
	}
	defer tx.Rollback()

	var active int
	if err := tx.QueryRowContext(ctx, "SELECT consolidation_active FROM proxy_domain_control WHERE singleton = 1").Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConsolidationNotActive
		}
		return fmt.Errorf("read domain consolidation state: %w", err)
	}
	if active != 1 {
		return ErrConsolidationNotActive
	}

	snapshot, err := snapshotTx(ctx, tx)
	if err != nil {
		return err
	}
	if !equalDomains(snapshot.Desired, prepared) {
		return ErrPreparedDomainsMismatch
	}
	// The CHECK constraint forbids zero/zero rows, so drop baseline-only
	// entries before assigning baseline = desired.
	if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_domain_state WHERE desired = 0 AND baseline = 1"); err != nil {
		return fmt.Errorf("remove superseded baseline domains: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE proxy_domain_state SET baseline = desired"); err != nil {
		return fmt.Errorf("finalize domain baseline: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_domain_state WHERE desired = 0 AND baseline = 0"); err != nil {
		return fmt.Errorf("remove obsolete domain rows: %w", err)
	}
	result, err := tx.ExecContext(ctx, "UPDATE proxy_domain_control SET consolidation_active = 0 WHERE singleton = 1 AND consolidation_active = 1")
	if err != nil {
		return fmt.Errorf("clear domain consolidation state: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("verify domain consolidation state: %w", err)
	}
	if updated != 1 {
		return ErrConsolidationNotActive
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit domain consolidation finalization: %w", err)
	}
	return nil
}

func (store *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin domain snapshot: %w", err)
	}
	defer tx.Rollback()

	snapshot, err := snapshotTx(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit domain snapshot: %w", err)
	}
	return snapshot, nil
}

func equalDomains(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func snapshotTx(ctx context.Context, tx *sql.Tx) (Snapshot, error) {
	snapshot := Snapshot{Desired: make([]string, 0), Baseline: make([]string, 0)}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT consolidation_active FROM proxy_domain_control WHERE singleton = 1").Scan(&active); err != nil {
		return Snapshot{}, fmt.Errorf("read domain consolidation state: %w", err)
	}
	snapshot.ConsolidationActive = active == 1
	rows, err := tx.QueryContext(ctx, "SELECT domain, desired, baseline FROM proxy_domain_state ORDER BY domain")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read domain snapshot: %w", err)
	}
	for rows.Next() {
		var name string
		var desired, baseline int
		if err := rows.Scan(&name, &desired, &baseline); err != nil {
			_ = rows.Close()
			return Snapshot{}, fmt.Errorf("read domain snapshot row: %w", err)
		}
		if desired == 1 {
			snapshot.Desired = append(snapshot.Desired, name)
		}
		if baseline == 1 {
			snapshot.Baseline = append(snapshot.Baseline, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Snapshot{}, fmt.Errorf("read domain snapshot rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, fmt.Errorf("close domain snapshot rows: %w", err)
	}
	return snapshot, nil
}
