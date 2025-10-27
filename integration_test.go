package testdb_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunTernMigrationsIntegration(t *testing.T) {
	if _, err := exec.LookPath("tern"); err != nil {
		t.Skip("tern not installed, skipping test. Run: make tools/install")
	}

	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	ctx := context.Background()
	testPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available, skipping integration test: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("Cannot connect to postgres, skipping integration test: %v", err)
	}
	testPool.Close()

	provider := &postgres.PostgresProvider{}

	db, err := testdb.New(t, provider, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrations("testdata/postgres/migrations_tern"),
		testdb.WithMigrationTool(testdb.MigrationToolTern))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err != nil {
		t.Fatalf("Failed to run Tern migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'users'
		)
	`).Scan(&exists)

	if err != nil {
		t.Fatalf("Failed to check if users table exists: %v", err)
	}

	if !exists {
		t.Error("Expected users table to exist after Tern migration")
	}
}

func TestRunGooseMigrationsIntegration(t *testing.T) {
	if _, err := exec.LookPath("goose"); err != nil {
		t.Skip("goose not installed, skipping test. Run: make tools/install")
	}

	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	ctx := context.Background()
	testPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available, skipping integration test: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("Cannot connect to postgres, skipping integration test: %v", err)
	}
	testPool.Close()

	provider := &postgres.PostgresProvider{}

	db, err := testdb.New(t, provider, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrations("testdata/postgres/migrations_goose"),
		testdb.WithMigrationTool(testdb.MigrationToolGoose))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err != nil {
		t.Fatalf("Failed to run Goose migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'products'
		)
	`).Scan(&exists)

	if err != nil {
		t.Fatalf("Failed to check if products table exists: %v", err)
	}

	if !exists {
		t.Error("Expected products table to exist after Goose migration")
	}
}

func TestRunTernMigrationsWithToolPath(t *testing.T) {
	ternPath, err := exec.LookPath("tern")
	if err != nil {
		t.Skip("tern not installed, skipping test. Run: make tools/install")
	}

	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	ctx := context.Background()
	testPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("Cannot connect to postgres: %v", err)
	}
	testPool.Close()

	provider := &postgres.PostgresProvider{}

	db, err := testdb.New(t, provider, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrations("testdata/postgres/migrations_tern"),
		testdb.WithMigrationTool(testdb.MigrationToolTern),
		testdb.WithMigrationToolPath(ternPath))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err != nil {
		t.Fatalf("Failed to run Tern migrations with tool path: %v", err)
	}

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'users'
		)
	`).Scan(&exists)

	if err != nil {
		t.Fatalf("Failed to check if users table exists: %v", err)
	}

	if !exists {
		t.Error("Expected users table to exist after Tern migration with tool path")
	}
}

func TestRunGooseMigrationsWithToolPath(t *testing.T) {
	goosePath, err := exec.LookPath("goose")
	if err != nil {
		t.Skip("goose not installed, skipping test. Run: make tools/install")
	}

	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	ctx := context.Background()
	testPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("Cannot connect to postgres: %v", err)
	}
	testPool.Close()

	provider := &postgres.PostgresProvider{}

	db, err := testdb.New(t, provider, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrations("testdata/postgres/migrations_goose"),
		testdb.WithMigrationTool(testdb.MigrationToolGoose),
		testdb.WithMigrationToolPath(goosePath))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err != nil {
		t.Fatalf("Failed to run Goose migrations with tool path: %v", err)
	}

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'products'
		)
	`).Scan(&exists)

	if err != nil {
		t.Fatalf("Failed to check if products table exists: %v", err)
	}

	if !exists {
		t.Error("Expected products table to exist after Goose migration with tool path")
	}
}

func TestRunMigrateMigrationsIntegration(t *testing.T) {
	if _, err := exec.LookPath("migrate"); err != nil {
		t.Skip("migrate not installed, skipping test. Run: make tools/install")
	}

	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	ctx := context.Background()
	testPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available, skipping integration test: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("Cannot connect to postgres, skipping integration test: %v", err)
	}
	testPool.Close()

	provider := &postgres.PostgresProvider{}

	db, err := testdb.New(t, provider, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrations("testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err != nil {
		t.Fatalf("Failed to run golang-migrate migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'test_table'
		)
	`).Scan(&exists)

	if err != nil {
		t.Fatalf("Failed to check if test_table exists: %v", err)
	}

	if !exists {
		t.Error("Expected test_table to exist after golang-migrate migration")
	}
}

func TestRunMigrateMigrationsWithToolPath(t *testing.T) {
	migratePath, err := exec.LookPath("migrate")
	if err != nil {
		t.Skip("migrate not installed, skipping test. Run: make tools/install")
	}

	adminDSN := testdb.ResolveAdminDSN(testdb.Config{}, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	ctx := context.Background()
	testPool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Skipf("Postgres not available: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		testPool.Close()
		t.Skipf("Cannot connect to postgres: %v", err)
	}
	testPool.Close()

	provider := &postgres.PostgresProvider{}

	db, err := testdb.New(t, provider, nil,
		testdb.WithAdminDSN(adminDSN),
		testdb.WithMigrations("testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
		testdb.WithMigrationToolPath(migratePath))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err != nil {
		t.Fatalf("Failed to run golang-migrate migrations with tool path: %v", err)
	}

	pool, err := pgxpool.New(ctx, db.DSN())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'test_table'
		)
	`).Scan(&exists)

	if err != nil {
		t.Fatalf("Failed to check if test_table exists: %v", err)
	}

	if !exists {
		t.Error("Expected test_table to exist after golang-migrate migration with tool path")
	}
}
