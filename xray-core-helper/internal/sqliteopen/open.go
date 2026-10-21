// Package sqliteopen configures a module-owned SQLite connection. Schemas and
// transaction lifecycles remain with their owners; connections are not shared.
package sqliteopen

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Options struct {
	BusyTimeout time.Duration
	WAL         bool
	ForeignKeys bool
}

func Open(ctx context.Context, path string, options Options) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	initialize := func() error {
		if err := db.PingContext(ctx); err != nil {
			return err
		}
		statements := []string{}
		if options.BusyTimeout > 0 {
			statements = append(statements, fmt.Sprintf("PRAGMA busy_timeout = %d", options.BusyTimeout.Milliseconds()))
		}
		if options.WAL {
			statements = append(statements, "PRAGMA journal_mode = WAL", "PRAGMA synchronous = FULL")
		}
		if options.ForeignKeys {
			statements = append(statements, "PRAGMA foreign_keys = ON")
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}
	if err := initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
