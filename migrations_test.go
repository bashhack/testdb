package testdb

import (
	"os/exec"
	"testing"
)

func TestRunTernMigrationsInvalidDSN(t *testing.T) {
	// Use mockErrorProvider with invalid DSN for ResolvedAdminDSN
	provider := &mockErrorProvider{adminDSN: "invalid-dsn"}

	db, err := New(t, provider, nil,
		WithMigrations("testdata/postgres/migrations_tern"),
		WithMigrationTool(MigrationToolTern))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when running Tern migrations with invalid DSN")
	}
}

func TestRunTernMigrationsIncompleteDSN(t *testing.T) {
	// DSN missing password
	provider := &mockErrorProvider{adminDSN: "postgres://user@localhost:5432/db"}

	db, err := New(t, provider, nil,
		WithMigrations("testdata/postgres/migrations_tern"),
		WithMigrationTool(MigrationToolTern))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when running Tern migrations with incomplete DSN")
	}
}

func TestRunTernMigrationsInvalidDirectory(t *testing.T) {
	if _, err := exec.LookPath("tern"); err != nil {
		t.Skip("tern not installed, skipping test")
	}

	provider := &mockErrorProvider{
		adminDSN: "postgres://user:pass@localhost:5432/postgres?sslmode=disable",
	}

	db, err := New(t, provider, nil,
		WithMigrations("/nonexistent/migrations"),
		WithMigrationTool(MigrationToolTern))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when running Tern migrations with invalid directory")
	}
}

func TestRunGooseMigrationsInvalidDriver(t *testing.T) {
	provider := &mockProvider{}

	db, err := New(t, provider, nil,
		WithMigrations("testdata/postgres/migrations_goose"),
		WithMigrationTool(MigrationToolGoose))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	// Mock provider returns "mock://" DSN which has no driver mapping
	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when running Goose migrations with unsupported driver")
	}
}

func TestRunGooseMigrationsInvalidDirectory(t *testing.T) {
	if _, err := exec.LookPath("goose"); err != nil {
		t.Skip("goose not installed, skipping test")
	}

	provider := &mockErrorProvider{
		adminDSN: "postgres://user:pass@localhost:5432/postgres?sslmode=disable",
	}

	db, err := New(t, provider, nil,
		WithMigrations("/nonexistent/migrations"),
		WithMigrationTool(MigrationToolGoose))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	// BuildDSN from mockErrorProvider will return postgres://... DSN
	// which will be detected as postgres driver, but directory doesn't exist
	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when running Goose migrations with invalid directory")
	}
}

func TestRunMigrateMigrationsInvalidDirectory(t *testing.T) {
	if _, err := exec.LookPath("migrate"); err != nil {
		t.Skip("migrate not installed, skipping test")
	}

	provider := &mockErrorProvider{
		adminDSN: "postgres://user:pass@localhost:5432/postgres?sslmode=disable",
	}

	db, err := New(t, provider, nil,
		WithMigrations("/nonexistent/migrations"),
		WithMigrationTool(MigrationToolMigrate))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when running golang-migrate migrations with invalid directory")
	}
}
