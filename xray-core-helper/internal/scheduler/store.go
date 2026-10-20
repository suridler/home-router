package scheduler

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists daily deadlines in the existing helper database. Missed days
// coalesce into one catch-up invocation; its durable API idempotency key avoids
// duplicate updates if the daemon exits after executing but before advancing.
type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000;
	 CREATE TABLE IF NOT EXISTS helper_schedule (name TEXT PRIMARY KEY, due INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) Persist(job Job, key string) Job {
	job.Pending = func(ctx context.Context, initial time.Time) (time.Time, error) {
		_, err := store.db.ExecContext(ctx, "INSERT OR IGNORE INTO helper_schedule(name,due) VALUES(?,?)", key, initial.Unix())
		if err != nil {
			return time.Time{}, err
		}
		var due int64
		err = store.db.QueryRowContext(ctx, "SELECT due FROM helper_schedule WHERE name=?", key).Scan(&due)
		return time.Unix(due, 0).In(time.Local), err
	}
	job.Complete = func(ctx context.Context, next time.Time) error {
		_, err := store.db.ExecContext(ctx, "UPDATE helper_schedule SET due=? WHERE name=?", next.Unix(), key)
		return err
	}
	return job
}
