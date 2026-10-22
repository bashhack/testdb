package testdb

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

// ExecuteTernMigrations runs tern migrations against a database.
// This is exported so it can be used by postgres.LoadMigrations for template creation.
//
// Tern requires parsing the DSN to extract connection details and database name.
func ExecuteTernMigrations(dsn, migrationDir, ternPath string) error {
	if ternPath == "" {
		ternPath = "tern"
	}

	// Parse DSN to get connection details
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}

	if config.Host == "" || config.Port == 0 || config.User == "" || config.Password == "" {
		return fmt.Errorf("incomplete DSN: host, port, user and password must be specified")
	}

	// Create temporary tern config file
	confPath := filepath.Join(os.TempDir(), fmt.Sprintf("tern_%s.conf", config.Database))
	confContent := fmt.Sprintf(`[database]
host = %s
port = %d
database = %s
user = %s
password = %s`,
		config.Host,
		config.Port,
		config.Database,
		config.User,
		config.Password)

	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		return fmt.Errorf("write tern config: %w", err)
	}
	defer func() { _ = os.Remove(confPath) }()

	// Execute tern migrate
	cmd := exec.Command(ternPath, "migrate",
		"-c", confPath,
		"-m", migrationDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tern migrate failed: %w\nOutput: %s", err, output)
	}

	return nil
}

// runTernMigrations executes migrations using the Tern migration tool.
// Tern is PostgreSQL-specific and supports advanced features like configurable migrations.
//
// See: https://github.com/jackc/tern
//
// This function:
//  1. Parses the admin DSN to extract connection details
//  2. Creates a temporary tern configuration file
//  3. Executes the tern CLI with appropriate arguments
//  4. Captures and returns any migration errors
//  5. Cleans up temporary files
func (td *TestDatabase) runTernMigrations() error {
	ternPath := td.config.MigrationToolPath
	if ternPath == "" {
		ternPath = "tern"
	}

	err := ExecuteTernMigrations(td.dsn, td.config.MigrationDir, ternPath)
	if err != nil {
		return &Error{
			Op:  "runTernMigrations",
			Err: err,
		}
	}

	td.logf("testdb: migrations completed for %s", td.name)
	return nil
}

// ExecuteGooseMigrations runs goose migrations against a database.
// This is exported so it can be used by postgres.LoadMigrations for template creation.
func ExecuteGooseMigrations(dsn, migrationDir, goosePath string) error {
	if goosePath == "" {
		goosePath = "goose"
	}

	// Goose uses driver names: postgres, mysql, sqlite3
	driver, err := driverFromDSN(dsn)
	if err != nil {
		return err
	}

	// Format: goose -dir <migration_dir> <driver> <dsn> up
	cmd := exec.Command(goosePath,
		"-dir", migrationDir,
		driver,
		dsn,
		"up")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("goose up failed: %w\nOutput: %s", err, output)
	}

	return nil
}

// runGooseMigrations executes migrations using the Goose migration tool.
// Goose supports PostgreSQL, MySQL, and SQLite.
//
// See: https://github.com/pressly/goose
//
// This function:
//  1. Determines the database driver from the DSN
//  2. Executes the goose CLI with appropriate arguments
//  3. Captures and returns any migration errors
func (td *TestDatabase) runGooseMigrations() error {
	goosePath := td.config.MigrationToolPath
	if goosePath == "" {
		goosePath = "goose"
	}

	err := ExecuteGooseMigrations(td.dsn, td.config.MigrationDir, goosePath)
	if err != nil {
		return &Error{
			Op:  "runGooseMigrations",
			Err: err,
		}
	}

	td.logf("testdb: migrations completed for %s", td.name)
	return nil
}

// ExecuteMigrateMigrations runs golang-migrate migrations against a database.
// This is exported so it can be used by postgres.LoadMigrations for template creation.
func ExecuteMigrateMigrations(dsn, migrationDir, migratePath string) error {
	if migratePath == "" {
		migratePath = "migrate"
	}

	// Convert to absolute path if needed
	if !filepath.IsAbs(migrationDir) {
		absPath, err := filepath.Abs(migrationDir)
		if err != nil {
			return fmt.Errorf("get absolute path: %w", err)
		}
		migrationDir = absPath
	}

	// Build source URL (migrate requires file:// prefix)
	sourceURL := fmt.Sprintf("file://%s", migrationDir)

	// Format: migrate -source <source_url> -database <dsn> up
	cmd := exec.Command(migratePath,
		"-source", sourceURL,
		"-database", dsn,
		"up")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("migrate up failed: %w\nOutput: %s", err, output)
	}

	return nil
}

// runMigrateMigrations executes migrations using the golang-migrate migration tool.
// golang-migrate supports PostgreSQL, MySQL, SQLite, MongoDB, and many other databases.
//
// See: https://github.com/golang-migrate/migrate
//
// This function:
//  1. Constructs the source path URL (file:// prefix)
//  2. Executes the migrate CLI with the DSN and source path
//  3. Captures and returns any migration errors
func (td *TestDatabase) runMigrateMigrations() error {
	migratePath := td.config.MigrationToolPath
	if migratePath == "" {
		migratePath = "migrate"
	}

	err := ExecuteMigrateMigrations(td.dsn, td.config.MigrationDir, migratePath)
	if err != nil {
		return &Error{
			Op:  "runMigrateMigrations",
			Err: err,
		}
	}

	td.logf("testdb: migrations completed for %s", td.name)
	return nil
}

// driverFromDSN determines the goose driver name from a DSN.
// Returns "postgres", "mysql", or "sqlite3" based on the DSN format.
func driverFromDSN(dsn string) (string, error) {
	switch {
	case len(dsn) >= 9 && dsn[:9] == "postgres:":
		return "postgres", nil
	case len(dsn) >= 11 && dsn[:11] == "postgresql:":
		return "postgres", nil
	case len(dsn) >= 6 && dsn[:6] == "mysql:":
		return "mysql", nil
	case len(dsn) >= 7 && dsn[:7] == "sqlite3":
		return "sqlite3", nil
	case len(dsn) >= 6 && dsn[:6] == "sqlite":
		return "sqlite3", nil
	default:
		return "", fmt.Errorf("unable to determine database driver from DSN: %s", dsn)
	}
}
