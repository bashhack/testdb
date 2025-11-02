package postgres_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// GormInitializer demonstrates using GORM with postgres.New()
type GormInitializer struct{}

func (g *GormInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	return gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
}

type User struct {
	ID    uint   `gorm:"primaryKey"`
	Email string `gorm:"uniqueIndex;not null"`
	Name  string
}

func TestCreateTable(t *testing.T) {
	pool := postgres.Setup(t)

	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO users (name, email) VALUES ($1, $2)
	`, "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	var name, email string
	err = pool.QueryRow(ctx, "SELECT name, email FROM users WHERE name = $1", "Alice").Scan(&name, &email)
	if err != nil {
		t.Fatalf("failed to query data: %v", err)
	}

	if name != "Alice" || email != "alice@example.com" {
		t.Fatalf("unexpected data: got (%s, %s)", name, email)
	}
}

func TestNewWithGORM(t *testing.T) {
	db := postgres.New(t, &GormInitializer{})

	gormDB := db.Entity().(*gorm.DB)

	if err := gormDB.AutoMigrate(&User{}); err != nil {
		t.Fatalf("failed to migrate schema: %v", err)
	}

	user := User{
		Email: "test@example.com",
		Name:  "Test User",
	}
	result := gormDB.Create(&user)
	if result.Error != nil {
		t.Fatalf("failed to create user: %v", result.Error)
	}
	if user.ID == 0 {
		t.Fatal("user ID should be set after creation")
	}

	var foundUser User
	result = gormDB.First(&foundUser, "email = ?", "test@example.com")
	if result.Error != nil {
		t.Fatalf("failed to find user: %v", result.Error)
	}

	if foundUser.Email != user.Email || foundUser.Name != user.Name {
		t.Fatalf("unexpected user data: got %+v, want %+v", foundUser, user)
	}
}

func TestParallel(t *testing.T) {
	t.Run("test1", func(t *testing.T) {
		t.Parallel()
		pool := postgres.Setup(t)

		ctx := context.Background()

		_, err := pool.Exec(ctx, `
			CREATE TABLE test1_data (
				id SERIAL PRIMARY KEY,
				value TEXT
			)
		`)
		if err != nil {
			t.Fatalf("test1: failed to create table: %v", err)
		}

		_, err = pool.Exec(ctx, "INSERT INTO test1_data (value) VALUES ($1)", "test1-value")
		if err != nil {
			t.Fatalf("test1: failed to insert: %v", err)
		}

		var value string
		err = pool.QueryRow(ctx, "SELECT value FROM test1_data LIMIT 1").Scan(&value)
		if err != nil {
			t.Fatalf("test1: failed to query: %v", err)
		}

		if value != "test1-value" {
			t.Fatalf("test1: expected 'test1-value', got %s", value)
		}
	})

	t.Run("test2", func(t *testing.T) {
		t.Parallel()
		pool := postgres.Setup(t)

		ctx := context.Background()

		_, err := pool.Exec(ctx, `
			CREATE TABLE test2_data (
				id SERIAL PRIMARY KEY,
				value TEXT
			)
		`)
		if err != nil {
			t.Fatalf("test2: failed to create table: %v", err)
		}

		_, err = pool.Exec(ctx, "INSERT INTO test2_data (value) VALUES ($1)", "test2-value")
		if err != nil {
			t.Fatalf("test2: failed to insert: %v", err)
		}

		var value string
		err = pool.QueryRow(ctx, "SELECT value FROM test2_data LIMIT 1").Scan(&value)
		if err != nil {
			t.Fatalf("test2: failed to query: %v", err)
		}

		if value != "test2-value" {
			t.Fatalf("test2: expected 'test2-value', got %s", value)
		}
	})

	t.Run("test3", func(t *testing.T) {
		t.Parallel()
		pool := postgres.Setup(t)

		ctx := context.Background()

		_, err := pool.Exec(ctx, `
			CREATE TABLE test3_data (
				id SERIAL PRIMARY KEY,
				value TEXT
			)
		`)
		if err != nil {
			t.Fatalf("test3: failed to create table: %v", err)
		}

		_, err = pool.Exec(ctx, "INSERT INTO test3_data (value) VALUES ($1)", "test3-value")
		if err != nil {
			t.Fatalf("test3: failed to insert: %v", err)
		}

		var value string
		err = pool.QueryRow(ctx, "SELECT value FROM test3_data LIMIT 1").Scan(&value)
		if err != nil {
			t.Fatalf("test3: failed to query: %v", err)
		}

		if value != "test3-value" {
			t.Fatalf("test3: expected 'test3-value', got %s", value)
		}
	})
}

func TestMultipleConnections(t *testing.T) {
	pool := postgres.Setup(t)

	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		CREATE TABLE concurrent_test (
			id SERIAL PRIMARY KEY,
			value INT
		)
	`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	done := make(chan error, 5)
	for i := range 5 {
		go func(val int) {
			_, err := pool.Exec(ctx, "INSERT INTO concurrent_test (value) VALUES ($1)", val)
			done <- err
		}(i)
	}

	for range 5 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent insert failed: %v", err)
		}
	}

	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM concurrent_test").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count rows: %v", err)
	}

	if count != 5 {
		t.Fatalf("expected 5 rows, got %d", count)
	}
}

func TestAdvancedConfigModifier(t *testing.T) {
	initializer := &postgres.PoolInitializer{
		ConfigModifier: func(cfg *pgxpool.Config) {
			cfg.MaxConns = 8
			cfg.MinConns = 2
			cfg.MaxConnLifetime = 30 * time.Minute

			if cfg.ConnConfig.RuntimeParams == nil {
				cfg.ConnConfig.RuntimeParams = make(map[string]string)
			}
			cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
		},
	}

	db := postgres.New(t, initializer, testdb.WithDBPrefix("custom"))

	pool := db.Entity().(*pgxpool.Pool)

	poolCfg := pool.Config()
	if poolCfg.MaxConns != 8 {
		t.Fatalf("expected MaxConns=8, got %d", poolCfg.MaxConns)
	}
	if poolCfg.MinConns != 2 {
		t.Fatalf("expected MinConns=2, got %d", poolCfg.MinConns)
	}

	var timeout string
	err := pool.QueryRow(context.Background(), "SHOW statement_timeout").Scan(&timeout)
	if err != nil {
		t.Fatalf("failed to check statement_timeout: %v", err)
	}
	if timeout != "10s" {
		t.Fatalf("expected statement_timeout=10s, got %s", timeout)
	}

	var result int
	err = pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
}

func TestDatabaseIsolation(t *testing.T) {
	pool1 := postgres.Setup(t)
	defer pool1.Close()

	ctx := context.Background()

	_, err := pool1.Exec(ctx, `
		CREATE TABLE isolation_test (
			id SERIAL PRIMARY KEY,
			value TEXT
		)
	`)
	if err != nil {
		t.Fatalf("failed to create table in db1: %v", err)
	}

	_, err = pool1.Exec(ctx, "INSERT INTO isolation_test (value) VALUES ($1)", "db1-value")
	if err != nil {
		t.Fatalf("failed to insert in db1: %v", err)
	}

	// Create second database - should not have the table
	pool2 := postgres.Setup(t)
	defer pool2.Close()

	// This should fail because the table doesn't exist in db2
	var value string
	err = pool2.QueryRow(ctx, "SELECT value FROM isolation_test LIMIT 1").Scan(&value)
	if err == nil {
		t.Fatal("expected error querying non-existent table in db2, got nil")
	}

	// But db1 should still work
	err = pool1.QueryRow(ctx, "SELECT value FROM isolation_test LIMIT 1").Scan(&value)
	if err != nil {
		t.Fatalf("db1 query failed: %v", err)
	}
	if value != "db1-value" {
		t.Fatalf("expected 'db1-value', got %s", value)
	}
}

func TestDatabaseNaming(t *testing.T) {
	initializer := &postgres.PoolInitializer{}
	db1 := postgres.New(t, initializer)
	db2 := postgres.New(t, initializer)

	if db1.Name() == db2.Name() {
		t.Fatalf("database names should be unique, both are: %s", db1.Name())
	}

	if !strings.HasPrefix(db1.Name(), "test_") {
		t.Fatalf("expected name to start with 'test_', got: %s", db1.Name())
	}

	db3 := postgres.New(t, initializer, testdb.WithDBPrefix("custom"))
	if !strings.HasPrefix(db3.Name(), "custom_") {
		t.Fatalf("expected name to start with 'custom_', got: %s", db3.Name())
	}
}

func TestTransactions(t *testing.T) {
	pool := postgres.Setup(t)

	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		CREATE TABLE txn_test (
			id SERIAL PRIMARY KEY,
			value INT
		)
	`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	_, err = tx.Exec(ctx, "INSERT INTO txn_test (value) VALUES ($1)", 42)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	err = tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("failed to rollback: %v", err)
	}

	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM txn_test").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows after rollback, got %d", count)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	_, err = tx.Exec(ctx, "INSERT INTO txn_test (value) VALUES ($1)", 99)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	var value int
	err = pool.QueryRow(ctx, "SELECT value FROM txn_test LIMIT 1").Scan(&value)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if value != 99 {
		t.Fatalf("expected value=99, got %d", value)
	}
}

func TestMigrationOptions(t *testing.T) {
	t.Run("WithMigrations_Tern", func(t *testing.T) {
		pool := postgres.Setup(t,
			testdb.WithMigrations("../testdata/postgres/migrations_tern"),
			testdb.WithMigrationTool(testdb.MigrationToolTern))

		var exists bool
		err := pool.QueryRow(context.Background(),
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'users')").Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check table existence: %v", err)
		}
		if !exists {
			t.Fatal("expected users table to exist after migration")
		}
	})

	t.Run("WithMigrations_Goose", func(t *testing.T) {
		pool := postgres.Setup(t,
			testdb.WithMigrations("../testdata/postgres/migrations_goose"),
			testdb.WithMigrationTool("goose"),
		)

		var exists bool
		err := pool.QueryRow(context.Background(),
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'products')").Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check table existence: %v", err)
		}
		if !exists {
			t.Fatal("expected products table to exist after migration")
		}
	})
}

func TestEntityAccess(t *testing.T) {
	db := postgres.New(t, &postgres.PoolInitializer{})

	entity1 := db.Entity()
	entity2 := db.Entity()

	if entity1 == nil || entity2 == nil {
		t.Fatal("entity should not be nil")
	}

	pool1, ok1 := entity1.(*pgxpool.Pool)
	pool2, ok2 := entity2.(*pgxpool.Pool)

	if !ok1 || !ok2 {
		t.Fatal("entity should be *pgxpool.Pool")
	}

	if pool1 != pool2 {
		t.Fatal("entity should return the same pool instance")
	}

	defer pool1.Close()
}

func TestDSNParsing(t *testing.T) {
	tests := map[string]struct {
		adminDSN string
	}{
		"standard dsn": {
			adminDSN: "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable",
		},
		"dsn with query params": {
			adminDSN: "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable&connect_timeout=10",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			pool := postgres.Setup(t, testdb.WithAdminDSN(tc.adminDSN))

			var result int
			err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
			if err != nil {
				t.Fatalf("query failed: %v", err)
			}
		})
	}
}

func TestWithMigrationToolPath(t *testing.T) {
	ternPath, err := exec.LookPath("tern")
	if err != nil {
		t.Skip("tern not found in PATH, skipping test")
	}

	pool := postgres.Setup(t,
		testdb.WithMigrations("../testdata/postgres/migrations_tern"),
		testdb.WithMigrationTool(testdb.MigrationToolTern),
		testdb.WithMigrationToolPath(ternPath))

	var tableName string
	err = pool.QueryRow(context.Background(),
		"SELECT tablename FROM pg_tables WHERE tablename = 'users'").Scan(&tableName)
	if err != nil {
		t.Fatalf("users table should exist after migration: %v", err)
	}
}

func TestSetupVsNewComparison(t *testing.T) {
	t.Run("Setup_returns_pool", func(t *testing.T) {
		// Setup returns *pgxpool.Pool directly - simplest API
		pool := postgres.Setup(t)

		// Use pool directly
		var result int
		err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if result != 1 {
			t.Fatalf("expected 1, got %d", result)
		}
		// Cleanup is automatic
	})

	t.Run("New_returns_TestDatabase", func(t *testing.T) {
		// New returns *testdb.TestDatabase - more flexible
		db := postgres.New(t, &postgres.PoolInitializer{})

		// Access the pool via Entity()
		pool := db.Entity().(*pgxpool.Pool)

		// Use pool
		var result int
		err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}

		// Can also access DSN and Config
		if db.DSN() == "" {
			t.Fatal("DSN should not be empty")
		}
		if db.Name() == "" {
			t.Fatal("database name should not be empty")
		}
		// Cleanup is automatic
	})
}

func TestErrorInvalidDSN(t *testing.T) {
	provider := &postgres.PostgresProvider{}
	initializer := &postgres.PoolInitializer{}

	_, err := testdb.New(t, provider, initializer,
		testdb.WithAdminDSN("not-a-valid-dsn"))
	if err == nil {
		t.Fatal("expected error with invalid admin DSN, got nil")
	}
}

func TestErrorInvalidTestDatabaseDSN(t *testing.T) {
	initializer := &postgres.PoolInitializer{}

	_, err := initializer.InitializeTestDatabase(context.Background(), ":::invalid:::")
	if err == nil {
		t.Fatal("expected error with invalid DSN, got nil")
	}
}

func TestCleanupWithNilConnection(t *testing.T) {
	provider := &postgres.PostgresProvider{}

	err := provider.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup with nil connection should not error: %v", err)
	}
}

func TestErrorConnectionRefused(t *testing.T) {
	provider := &postgres.PostgresProvider{}

	err := provider.Initialize(context.Background(), testdb.Config{
		AdminDSNOverride: "postgres://postgres:postgres@localhost:9999/postgres?sslmode=disable",
	})

	if err == nil {
		t.Fatal("expected error connecting to invalid port, got nil")
	}
}

func TestErrorInvalidPoolCreation(t *testing.T) {
	initializer := &postgres.PoolInitializer{
		ConfigModifier: func(cfg *pgxpool.Config) {
			cfg.MaxConns = 0
		},
	}

	_, err := initializer.InitializeTestDatabase(context.Background(),
		"postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")

	if err == nil {
		t.Fatal("expected error creating pool with invalid config, got nil")
	}

	if !strings.Contains(err.Error(), "create pool") {
		t.Logf("got error: %v", err)
	}
}

func TestErrorPingFailure(t *testing.T) {
	initializer := &postgres.PoolInitializer{}

	_, err := initializer.InitializeTestDatabase(context.Background(),
		"postgres://postgres:postgres@nonexistent-host-xyz:5432/postgres?sslmode=disable")

	if err == nil {
		t.Fatal("expected ping error, got nil")
	}

	if !strings.Contains(err.Error(), "ping database") {
		t.Logf("got error: %v", err)
	}
}

func TestBuildDSNIncompleteDSN(t *testing.T) {
	provider := &postgres.PostgresProvider{}

	// Initialize with incomplete DSN (missing password)
	cfg := testdb.DefaultConfig()
	cfg.AdminDSNOverride = "postgres://user@localhost:5432/db"

	ctx := context.Background()
	err := provider.Initialize(ctx, cfg)
	if err == nil {
		// If initialization succeeds despite incomplete DSN, test BuildDSN
		_, err = provider.BuildDSN("testdb")
		if err == nil {
			t.Fatal("expected error from BuildDSN with incomplete DSN, got nil")
		}
		if !strings.Contains(err.Error(), "incomplete admin DSN") {
			t.Errorf("expected 'incomplete admin DSN' error, got: %v", err)
		}
		if err := provider.Cleanup(ctx); err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		}
	}
	// If Initialize fails, that's also acceptable for incomplete DSN
}

func TestBuildDSNInvalidFormat(t *testing.T) {
	provider := &postgres.PostgresProvider{}

	v := reflect.ValueOf(provider).Elem()
	adminDSNField := v.FieldByName("adminDSN")
	if adminDSNField.IsValid() {
		adminDSNPtr := (*string)(unsafe.Pointer(adminDSNField.UnsafeAddr()))
		*adminDSNPtr = "not-a-valid-dsn-format"

		_, err := provider.BuildDSN("testdb")
		if err == nil {
			t.Fatal("expected error from BuildDSN with invalid DSN format, got nil")
		}
	} else {
		t.Skip("Cannot access adminDSN field")
	}
}

func TestSetupErrorHandling(t *testing.T) {
	spy := &spyTB{TB: t}

	// Recover from the panic that Fatalf causes
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r) // Re-panic if it's not our sentinel
			}
		}

		if !spy.failed {
			t.Error("Expected Setup to call t.Fatalf on error")
		}

		if !strings.Contains(spy.fatalMessage, "postgres.Setup") {
			t.Errorf("Expected error message to contain 'postgres.Setup', got: %s", spy.fatalMessage)
		}

		spy.runCleanups()
	}()

	// Use invalid admin DSN to trigger testdb.New error
	postgres.Setup(spy, testdb.WithAdminDSN("invalid-dsn"))
}

func TestSetupMigrationErrorHandling(t *testing.T) {
	spy := &spyTB{TB: t}

	// Recover from the panic that Fatalf causes
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r) // Re-panic if it's not our sentinel
			}
		}

		if !spy.failed {
			t.Error("Expected Setup to call t.Fatalf on migration error")
		}

		if !strings.Contains(spy.fatalMessage, "migrations failed") {
			t.Errorf("Expected error message to contain 'migrations failed', got: %s", spy.fatalMessage)
		}

		spy.runCleanups()
	}()

	// Use non-existent migration directory to trigger migration error
	postgres.Setup(spy,
		testdb.WithMigrations("/nonexistent/migration/path"),
		testdb.WithMigrationTool(testdb.MigrationToolTern))
}

func TestNewErrorHandling(t *testing.T) {
	spy := &spyTB{TB: t}

	// Recover from the panic that Fatalf causes
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r) // Re-panic if it's not our sentinel
			}
		}

		if !spy.failed {
			t.Error("Expected New to call t.Fatalf on error")
		}

		if !strings.Contains(spy.fatalMessage, "postgres.New") {
			t.Errorf("Expected error message to contain 'postgres.New', got: %s", spy.fatalMessage)
		}

		spy.runCleanups()
	}()

	initializer := &postgres.PoolInitializer{}

	// Use invalid admin DSN to trigger testdb.New error
	postgres.New(spy, initializer, testdb.WithAdminDSN("invalid-dsn"))
}

func TestNewMigrationErrorHandling(t *testing.T) {
	spy := &spyTB{TB: t}

	// Recover from the panic that Fatalf causes
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r) // Re-panic if it's not our sentinel
			}
		}

		if !spy.failed {
			t.Error("Expected New to call t.Fatalf on migration error")
		}

		if !strings.Contains(spy.fatalMessage, "migrations failed") {
			t.Errorf("Expected error message to contain 'migrations failed', got: %s", spy.fatalMessage)
		}

		spy.runCleanups()
	}()

	initializer := &postgres.PoolInitializer{}

	// Use non-existent migration directory to trigger migration error
	postgres.New(spy, initializer,
		testdb.WithMigrations("/nonexistent/migration/path"),
		testdb.WithMigrationTool(testdb.MigrationToolTern))
}

func TestNewNilInitializer(t *testing.T) {
	spy := &spyTB{TB: t}

	// Recover from the panic that Fatalf causes
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r) // Re-panic if it's not our sentinel
			}
		}

		if !spy.failed {
			t.Error("Expected New to call t.Fatalf with nil initializer")
		}

		if !strings.Contains(spy.fatalMessage, "initializer cannot be nil") {
			t.Errorf("Expected error message to contain 'initializer cannot be nil', got: %s", spy.fatalMessage)
		}

		if !strings.Contains(spy.fatalMessage, "postgres.Setup()") {
			t.Errorf("Expected error message to suggest postgres.Setup(), got: %s", spy.fatalMessage)
		}
	}()

	// This should panic with helpful error message
	postgres.New(spy, nil)
}

func TestBuildDSNWithTLSConfig(t *testing.T) {
	certPEM, keyPEM := generateTestCertAndKey()
	if certPEM == nil || keyPEM == nil {
		t.Fatal("Failed to generate test certificate and key")
	}

	tmpDir := t.TempDir()
	certPath := tmpDir + "/cert.pem"
	keyPath := tmpDir + "/key.pem"

	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatalf("Failed to write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("Failed to write key: %v", err)
	}

	provider := &postgres.PostgresProvider{}
	cfg := testdb.DefaultConfig()
	cfg.AdminDSNOverride = "postgres://postgres:postgres@localhost:5432/postgres?sslcert=" + certPath + "&sslkey=" + keyPath

	ctx := context.Background()
	err := provider.Initialize(ctx, cfg)
	if err != nil {
		t.Skipf("Could not initialize provider with TLS (postgres not running?): %v", err)
	}
	defer func() {
		if err := provider.Cleanup(ctx); err != nil {
			t.Errorf("Failed to cleanup provider: %v", err)
		}
	}()

	dsn, err := provider.BuildDSN("testdb")
	if err != nil {
		t.Fatalf("BuildDSN failed: %v", err)
	}

	if !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("Expected sslmode=require in DSN, got: %s", dsn)
	}
}

func TestSetupRegistersCleanup(t *testing.T) {
	spy := &spyTB{TB: t}

	_ = postgres.Setup(spy)

	if len(spy.cleanups) != 1 {
		t.Errorf("Expected 1 cleanup function to be registered, got %d", len(spy.cleanups))
	}

	// Run the cleanup to ensure test database is dropped
	spy.runCleanups()
}

func TestNewRegistersCleanup(t *testing.T) {
	spy := &spyTB{TB: t}

	initializer := &postgres.PoolInitializer{}
	db := postgres.New(spy, initializer)

	if len(spy.cleanups) != 1 {
		t.Errorf("Expected 1 cleanup function to be registered, got %d", len(spy.cleanups))
	}

	pool := db.Entity().(*pgxpool.Pool)
	pool.Close()
	spy.runCleanups()
}

func TestCleanupDropsDatabase(t *testing.T) {
	spy := &spyTB{TB: t}

	pool := postgres.Setup(spy, testdb.WithDBPrefix("cleanup_test"))
	dbName := ""

	config := pool.Config()
	dbName = config.ConnConfig.Database

	if dbName == "" {
		t.Fatal("Could not determine database name from pool config")
	}

	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Database should exist but ping failed: %v", err)
	}

	pool.Close()

	spy.runCleanups()

	// Verify database no longer exists by trying to connect to it
	// We need to create a new provider to check
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		adminDSN = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}

	provider := &postgres.PostgresProvider{}
	if err := provider.Initialize(ctx, testdb.Config{AdminDSNOverride: adminDSN}); err != nil {
		t.Fatalf("Failed to initialize provider: %v", err)
	}
	defer func() {
		if err := provider.Cleanup(ctx); err != nil {
			t.Errorf("Failed to cleanup provider: %v", err)
		}
	}()

	// Try to build a DSN and connect to the dropped database - should fail
	droppedDSN, err := provider.BuildDSN(dbName)
	if err != nil {
		t.Fatalf("Failed to build DSN: %v", err)
	}

	checkPool, err := pgxpool.New(ctx, droppedDSN)
	if err == nil {
		// If we can create a pool, try to ping it
		if pingErr := checkPool.Ping(ctx); pingErr == nil {
			checkPool.Close()
			t.Errorf("Database %s should have been dropped but still exists", dbName)
		} else {
			checkPool.Close()
		}
	}
}

func TestCleanupOnMigrationFailure(t *testing.T) {
	spy := &spyTB{TB: t}

	// Recover from the panic that Fatalf causes
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				panic(r) // Re-panic if it's not our sentinel
			}
		}

		if !spy.failed {
			t.Fatal("Expected Setup to fail on migration error")
		}

		// Extract database name from log messages
		// Verbose logging outputs: "testdb: creating database {name}"
		var dbName string
		for _, msg := range spy.logMessages {
			if strings.Contains(msg, "creating database") {
				parts := strings.Split(msg, "creating database ")
				if len(parts) == 2 {
					dbName = strings.TrimSpace(parts[1])
					break
				}
			}
		}

		if dbName == "" {
			t.Fatal("Could not extract database name from log messages")
		}

		// Now verify the database was actually dropped during cleanup
		ctx := context.Background()
		adminDSN := os.Getenv("TEST_DATABASE_URL")
		if adminDSN == "" {
			adminDSN = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
		}

		provider := &postgres.PostgresProvider{}
		if err := provider.Initialize(ctx, testdb.Config{AdminDSNOverride: adminDSN}); err != nil {
			t.Fatalf("Failed to initialize provider: %v", err)
		}
		defer func() {
			if err := provider.Cleanup(ctx); err != nil {
				t.Errorf("Failed to cleanup provider: %v", err)
			}
		}()

		// Try to connect to the database - should fail because it was cleaned up
		droppedDSN, err := provider.BuildDSN(dbName)
		if err != nil {
			t.Fatalf("Failed to build DSN: %v", err)
		}

		checkPool, err := pgxpool.New(ctx, droppedDSN)
		if err == nil {
			// If we can create a pool, try to ping it
			if pingErr := checkPool.Ping(ctx); pingErr == nil {
				checkPool.Close()
				t.Errorf("Database %s should have been dropped after migration failure but still exists", dbName)
			} else {
				checkPool.Close()
			}
		}
	}()

	// Trigger migration failure with non-existent directory
	// Use WithVerbose to capture database name from logs
	postgres.Setup(spy,
		testdb.WithDBPrefix("migration_cleanup_test"),
		testdb.WithMigrations("/nonexistent/migration/path"),
		testdb.WithMigrationTool(testdb.MigrationToolTern),
		testdb.WithVerbose())
}

func TestIdentifierSanitizationMakesUnsafePrefixesSafe(t *testing.T) {
	// Prefix with hyphen - would fail without pgx.Identifier sanitization
	unsafePrefix := "my-prefix"

	tests := map[string]struct {
		fn func(t *testing.T)
	}{
		"testdb.New": {
			fn: func(t *testing.T) {
				provider := &postgres.PostgresProvider{}
				initializer := &postgres.PoolInitializer{}
				db, err := testdb.New(t, provider, initializer, testdb.WithDBPrefix(unsafePrefix))
				if err != nil {
					t.Fatalf("Expected unsafe prefix to work with sanitization, got error: %v", err)
				}
				defer func() {
					if err := db.Close(); err != nil {
						t.Errorf("Failed to close database: %v", err)
					}
				}()

				// Verify core functionality: connect and query
				pool := db.Entity().(*pgxpool.Pool)
				var result int
				err = pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
				if err != nil {
					t.Fatalf("Failed to query database with sanitized name: %v", err)
				}
				if result != 1 {
					t.Errorf("Expected query result 1, got %d", result)
				}
			},
		},
		"postgres.Setup": {
			fn: func(t *testing.T) {
				pool := postgres.Setup(t, testdb.WithDBPrefix(unsafePrefix))
				defer pool.Close()

				// Verify core functionality: query works
				var result int
				err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
				if err != nil {
					t.Fatalf("Failed to query database with sanitized name: %v", err)
				}
				if result != 1 {
					t.Errorf("Expected query result 1, got %d", result)
				}
			},
		},
		"postgres.New": {
			fn: func(t *testing.T) {
				db := postgres.New(t, &postgres.PoolInitializer{}, testdb.WithDBPrefix(unsafePrefix))
				pool := db.Entity().(*pgxpool.Pool)
				defer pool.Close()

				// Verify core functionality: query works
				var result int
				err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
				if err != nil {
					t.Fatalf("Failed to query database with sanitized name: %v", err)
				}
				if result != 1 {
					t.Errorf("Expected query result 1, got %d", result)
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tc.fn(t)
		})
	}
}

func TestLongPrefixTruncation(t *testing.T) {
	// PostgreSQL identifier limit: 63 bytes (NAMEDATALEN-1)
	// Source: https://www.postgresql.org/docs/current/sql-syntax-lexical.html
	//
	// Behavior: Identifiers longer than 63 bytes are SILENTLY TRUNCATED (no error)
	//
	// Our database name format: prefix_timestamp_random
	// - Prefix: variable length
	// - Timestamp: 19 digits (Unix nanoseconds)
	// - Random: 8 hex characters
	// - Separators: 2 underscores
	// Total: len(prefix) + 29
	//
	// To avoid truncation and potential name collisions, keep prefix under 34 characters.

	// Test 1: Prefix that fits (33 chars + 29 = 62 bytes, under limit)
	t.Run("short prefix does not truncate", func(t *testing.T) {
		shortPrefix := strings.Repeat("s", 33)
		pool := postgres.Setup(t, testdb.WithDBPrefix(shortPrefix))
		defer pool.Close()

		dbName := pool.Config().ConnConfig.Database
		if len(dbName) > 63 {
			t.Errorf("Expected name under 63 bytes, got %d: %s", len(dbName), dbName)
		}

		// Verify database works
		var result int
		err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
		if err != nil {
			t.Fatalf("Query failed: %v", err)
		}
	})

	// Test 2: Prefix that's too long gets rejected with clear error
	t.Run("long prefix rejected by validation", func(t *testing.T) {
		spy := &spyTB{TB: t}

		// Recover from the panic that Fatalf causes
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatalPanic); !ok {
					panic(r) // Re-panic if it's not our sentinel
				}
			}

			if !spy.failed {
				t.Error("Expected Setup to fail with too-long prefix")
			}

			// Should get clear error about prefix being too long
			if !strings.Contains(spy.fatalMessage, "prefix too long") {
				t.Errorf("Expected error about prefix length, got: %s", spy.fatalMessage)
			}

			if !strings.Contains(spy.fatalMessage, "34") {
				t.Errorf("Expected error to mention max length of 34, got: %s", spy.fatalMessage)
			}
		}()

		// 40-char prefix exceeds MaxDBPrefixLength (34)
		tooLongPrefix := strings.Repeat("L", 40)

		postgres.Setup(spy, testdb.WithDBPrefix(tooLongPrefix))
	})
}

func TestCleanupStress(t *testing.T) {
	for range 30 {
		t.Run("iteration", func(t *testing.T) {
			t.Parallel()

			initializer := &postgres.PoolInitializer{
				ConfigModifier: func(cfg *pgxpool.Config) {
					cfg.MaxConns = 5
					cfg.MinConns = 2
					cfg.MaxConnLifetime = time.Hour
					cfg.MaxConnIdleTime = time.Hour
				},
			}

			db := postgres.New(t, initializer)
			pool := db.Entity().(*pgxpool.Pool)

			ctx := context.Background()
			var wg sync.WaitGroup
			for range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					var result string
					_ = pool.QueryRow(ctx, "SELECT pg_sleep(10)").Scan(&result)
					// ...test may have completed (intentional race condition)
				}()
			}

			// Don't wait for queries - let t.Cleanup() race with active connections.
			// This tests the race condition where pool.Close() returns before connections
			// fully close, requiring TerminateConnections() to handle stragglers via the
			// two-step approach: DISALLOW new connections, then TERMINATE existing ones.
			go func() { wg.Wait() }()
		})
	}
}

func TestCleanupStress_SingleValidation(t *testing.T) {
	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var queryErrors []error

	// Register validation that runs AFTER database cleanup (due to LIFO order)
	t.Cleanup(func() {
		// Wait for all query goroutines to complete
		wg.Wait()

		errorsMu.Lock()
		defer errorsMu.Unlock()

		if len(queryErrors) == 0 {
			t.Error("expected queries to be interrupted by cleanup, but got no errors")
			return
		}

		// Check that all errors are expected SQLSTATE codes
		unexpectedCount := 0
		for i, err := range queryErrors {
			if !isExpectedCleanupError(err) {
				t.Errorf("query %d: unexpected error type: %v", i, err)
				unexpectedCount++
			}
		}

		if unexpectedCount == 0 {
			t.Logf("validated %d interruption errors - all match expected SQLSTATE codes", len(queryErrors))
		}
	})

	initializer := &postgres.PoolInitializer{
		ConfigModifier: func(cfg *pgxpool.Config) {
			cfg.MaxConns = 5
			cfg.MinConns = 2
			cfg.MaxConnLifetime = time.Hour
			cfg.MaxConnIdleTime = time.Hour
		},
	}

	db := postgres.New(t, initializer)
	pool := db.Entity().(*pgxpool.Pool)

	// Start long-running queries that will be interrupted by cleanup
	ctx := context.Background()
	for j := range 3 {
		wg.Add(1)
		go func(queryNum int) {
			defer wg.Done()
			var result string
			err := pool.QueryRow(ctx, "SELECT pg_sleep(10)").Scan(&result)
			if err != nil {
				errorsMu.Lock()
				queryErrors = append(queryErrors, err)
				errorsMu.Unlock()
				t.Logf("query %d interrupted: %v", queryNum, err)
			}
		}(j)
	}

	// Test exits here without waiting for queries
	// Cleanup order (LIFO):
	// 1. postgres.New() registered cleanup (closes pool, drops database)
	// 2. Validation cleanup (waits for goroutines, validates errors)
}

func TestCleanupStress_RapidCreateDestroy(t *testing.T) {
	for i := range 30 {
		db := postgres.New(t, &postgres.PoolInitializer{})
		pool := db.Entity().(*pgxpool.Pool)

		var result int
		if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result); err != nil {
			t.Fatalf("Query failed: %v", err)
		}

		if err := db.Close(); err != nil {
			t.Fatalf("Cleanup failed on iteration %d: %v", i, err)
		}
		pool.Close()
	}
}

func TestCleanupStress_ConcurrentDatabases(t *testing.T) {
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(iteration int) {
			defer wg.Done()

			initializer := &postgres.PoolInitializer{
				ConfigModifier: func(cfg *pgxpool.Config) {
					cfg.MaxConns = 5
					cfg.MinConns = 2
				},
			}

			t.Run("concurrent", func(t *testing.T) {
				db := postgres.New(t, initializer)
				pool := db.Entity().(*pgxpool.Pool)

				var result int
				if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result); err != nil {
					t.Errorf("Iteration %d query failed: %v", iteration, err)
				}
			})
		}(i)
	}
	wg.Wait()
}

// isExpectedCleanupError returns true if the error matches expected cleanup errors.
func isExpectedCleanupError(err error) bool {
	if err == nil {
		return false
	}

	// Check for PostgreSQL error codes from cleanup
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "57P01": // admin_shutdown - "terminating connection due to administrator command"
			return true
		case "55000": // object_not_in_prerequisite_state - "database is not currently accepting connections"
			return true
		case "3D000": // invalid_catalog_name - "database does not exist"
			return true
		}
	}

	return false
}

// spyTB is a testing.TB implementation that captures Fatal calls
type spyTB struct {
	testing.TB
	failed       bool
	fatalMessage string
	cleanups     []func()
	logMessages  []string
}

// fatalPanic is a sentinel type for panics from Fatalf
type fatalPanic string

func (s *spyTB) Fatalf(format string, args ...any) {
	s.failed = true
	s.fatalMessage = fmt.Sprintf(format, args...)
	// Panic to stop execution like real t.Fatalf
	panic(fatalPanic(s.fatalMessage))
}

func (s *spyTB) Logf(format string, args ...any) {
	s.logMessages = append(s.logMessages, fmt.Sprintf(format, args...))
}

func (s *spyTB) Helper() {}

func (s *spyTB) Cleanup(f func()) {
	s.cleanups = append(s.cleanups, f)
}

func (s *spyTB) runCleanups() {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
}

func generateTestCertAndKey() ([]byte, []byte) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		fmt.Printf("Failed to generate private key: %v\n", err)
		return nil, nil
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		fmt.Printf("Failed to create certificate: %v\n", err)
		return nil, nil
	}

	certPEM := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	}
	certPEMBytes := pem.EncodeToMemory(certPEM)

	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	privatePEMBytes := pem.EncodeToMemory(privateKeyPEM)

	return certPEMBytes, privatePEMBytes
}
