package db_test

import (
	"context"
	"testing"

	"github.com/BeFeast/RehearseKit/internal/db"
	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := dbtest.Pool(t) // applies migrations once
	ctx := context.Background()

	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second migrate applied %v, want nothing", again)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	migs, _ := db.Migrations()
	if n != len(migs) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(migs))
	}
	for _, tbl := range []string{"users", "sessions", "jobs", "stems", "job_events", "mix_states", "gpu_leases"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, tbl).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s missing", tbl)
		}
	}
}

func TestMigrateEnforcesChecks(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO jobs (id, project_name, input_type, quality, status, expires_at)
		VALUES (gen_random_uuid(), 'x', 'upload', 'fast', 'bogus', now())`)
	if err == nil {
		t.Fatal("insert with invalid status succeeded")
	}
}
