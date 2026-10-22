package testdb_test

import (
	"context"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRunMigrationsIntegration drives the low-level path end to end against
// a real server: create a database with testdb.New, run the migrator when
// the test chooses to, and check its work through a fresh connection.
func TestRunMigrationsIntegration(t *testing.T) {
	ctx := context.Background()
	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	adminPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available, skipping integration test: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Skipf("Cannot connect to postgres, skipping integration test: %v", err)
	}
	adminPool.Close()

	db, err := testdb.New(t, &postgres.PostgresProvider{}, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrator(postgres.SQLFiles("testdata/postgres/schema")))
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	// Nothing has run yet
	if n := countTables(t, pool); n != 0 {
		t.Fatalf("expected an empty database before RunMigrations, found %d tables", n)
	}

	if err := db.RunMigrations(ctx); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	for _, table := range []string{"test_table", "users", "products"} {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("expected %s table to exist after migrations", table)
		}
	}
}

func countTables(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'").Scan(&n)
	if err != nil {
		t.Fatalf("count tables: %v", err)
	}
	return n
}
