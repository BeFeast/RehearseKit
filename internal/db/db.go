// Package db owns the Postgres connection pool and the embedded schema
// migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is the advisory lock key that serialises concurrent
// `rk migrate` runs (and concurrent test packages) against one database.
const migrationLockID int64 = 0x524b4d4947 // "RKMIG"

// Connect opens a pgx pool for the given DSN and verifies connectivity.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return ConnectMinConns(ctx, dsn, 0)
}

// ConnectMinConns is Connect with the pool's max size raised to at least
// minMax when the DSN (pool_max_conns) or the pgx default (max(4, NumCPU))
// would give fewer. Callers that reserve connections for the duration of
// a job (the worker's per-job locks) use it to keep headroom for the
// ordinary queries those jobs make.
func ConnectMinConns(ctx context.Context, dsn string, minMax int32) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, fmt.Errorf("database URL is empty (set RK_DATABASE_URL)")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if cfg.MaxConns < minMax {
		cfg.MaxConns = minMax
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// Migration is one embedded SQL migration file.
type Migration struct {
	Version string // file name without extension, e.g. "0001_init"
	SQL     string
}

// Migrations returns the embedded migrations in version order.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: strings.TrimSuffix(e.Name(), ".sql"), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrate applies every migration that is not yet recorded in
// schema_migrations. It is idempotent and safe to run concurrently: the whole
// run happens in one transaction under an advisory lock.
//
// The returned slice lists the versions applied by this call.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	migs, err := Migrations()
	if err != nil {
		return nil, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return nil, fmt.Errorf("advisory lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var done []string
	for _, m := range migs {
		if applied[m.Version] {
			continue
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return nil, fmt.Errorf("migration %s: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.Version); err != nil {
			return nil, fmt.Errorf("record %s: %w", m.Version, err)
		}
		done = append(done, m.Version)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return done, nil
}
