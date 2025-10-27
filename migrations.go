package testdb

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

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
	adminDSN := td.provider.ResolvedAdminDSN()

	config, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return &Error{
			Op:  "runTernMigrations",
			Err: fmt.Errorf("parse admin DSN: %w", err),
		}
	}

	if config.Host == "" || config.Port == 0 || config.User == "" || config.Password == "" {
		return &Error{
			Op:  "runTernMigrations",
			Err: fmt.Errorf("incomplete admin DSN: host, port, user and password must be specified"),
		}
	}

	confPath := filepath.Join(os.TempDir(), fmt.Sprintf("tern_%s.conf", td.name))
	confContent := fmt.Sprintf(`[database]
host = %s
port = %d
database = %s
user = %s
password = %s`,
		config.Host,
		config.Port,
		td.name,
		config.User,
		config.Password)

	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		return &Error{
			Op:  "runTernMigrations",
			Err: fmt.Errorf("write tern config: %w", err),
		}
	}
	defer func() { _ = os.Remove(confPath) }()

	ternPath := "tern"
	if td.config.MigrationToolPath != "" {
		ternPath = td.config.MigrationToolPath
	}

	cmd := exec.Command(ternPath, "migrate",
		"-c", confPath,
		"-m", td.config.MigrationDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &Error{
			Op:  "runTernMigrations",
			Err: fmt.Errorf("tern migrate failed: %w\nOutput: %s", err, output),
		}
	}

	td.logf("testdb: migrations completed for %s", td.name)
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
	goosePath := "goose"
	if td.config.MigrationToolPath != "" {
		goosePath = td.config.MigrationToolPath
	}

	// Goose uses driver names: postgres, mysql, sqlite3
	driver, err := driverFromDSN(td.dsn)
	if err != nil {
		return &Error{
			Op:  "runGooseMigrations",
			Err: err,
		}
	}

	// Format: goose -dir <migration_dir> <driver> <dsn> up
	cmd := exec.Command(goosePath,
		"-dir", td.config.MigrationDir,
		driver,
		td.dsn,
		"up")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &Error{
			Op:  "runGooseMigrations",
			Err: fmt.Errorf("goose up failed: %w\nOutput: %s", err, output),
		}
	}

	td.logf("testdb: migrations completed for %s", td.name)
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
	migratePath := "migrate"
	if td.config.MigrationToolPath != "" {
		migratePath = td.config.MigrationToolPath
	}

	migrationDir := td.config.MigrationDir
	if !filepath.IsAbs(migrationDir) {
		absPath, err := filepath.Abs(migrationDir)
		if err != nil {
			return &Error{
				Op:  "runMigrateMigrations",
				Err: fmt.Errorf("get absolute path: %w", err),
			}
		}
		migrationDir = absPath
	}

	// Build source URL (migrate requires file:// prefix)
	sourceURL := fmt.Sprintf("file://%s", migrationDir)

	// Format: migrate -source <source_url> -database <dsn> up
	cmd := exec.Command(migratePath,
		"-source", sourceURL,
		"-database", td.dsn,
		"up")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &Error{
			Op:  "runMigrateMigrations",
			Err: fmt.Errorf("migrate up failed: %w\nOutput: %s", err, output),
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
