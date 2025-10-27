package testdb

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

// Config holds the configuration for test database creation and management.
type Config struct {
	// AdminDSNOverride is an optional connection string override for creating/dropping test databases.
	// The user specified here must have privileges to create and drop databases.
	//
	// For PostgreSQL, this is typically the 'postgres' database.
	//
	// If not specified, the library automatically discovers the admin DSN from:
	//   1. TEST_DATABASE_URL environment variable
	//   2. DATABASE_URL environment variable
	//   3. Database-specific defaults (e.g., postgres://postgres:postgres@localhost:5432/postgres)
	//
	// Most users don't need to set this - automatic discovery works in most cases.
	// Use this when you need custom admin credentials or connection settings.
	AdminDSNOverride string

	// MigrationDir is the absolute or relative path to migration files.
	// If set, MigrationTool must also be set (and vice versa).
	//
	// Example: "./migrations" or "/path/to/project/migrations"
	MigrationDir string

	// MigrationTool specifies which migration tool to use.
	// Supported: "tern", "goose", "migrate"
	// If set, MigrationDir must also be set (and vice versa).
	//
	// Example: testdb.MigrationToolTern, testdb.MigrationToolGoose, testdb.MigrationToolMigrate
	MigrationTool MigrationTool

	// MigrationToolPath is the path to the migration tool binary.
	// If empty, the tool is assumed to be in PATH.
	//
	// Example: "/usr/local/bin/tern"
	MigrationToolPath string

	// DBPrefix is prepended to test database names.
	// Useful for identifying test databases in a shared environment.
	//
	// Default: "test"
	// Example database name: "test_1699564231_a1b2c3d4"
	DBPrefix string

	// Verbose enables logging of database operations.
	// When false (default), testdb operates silently.
	// When true, logs database creation, cleanup, and migration completion.
	//
	// Default: false
	Verbose bool
}

// MigrationTool represents supported database migration tools.
type MigrationTool string

const (
	// MigrationToolTern represents the 'tern' migration tool.
	// External dependency: Must be installed separately and available in PATH.
	// See: https://github.com/jackc/tern
	// PostgreSQL only.
	MigrationToolTern MigrationTool = "tern"

	// MigrationToolGoose represents the 'goose' migration tool.
	// External dependency: Must be installed separately and available in PATH.
	// See: https://github.com/pressly/goose
	// Supports PostgreSQL, MySQL, SQLite.
	MigrationToolGoose MigrationTool = "goose"

	// MigrationToolMigrate represents the 'golang-migrate/migrate' migration tool.
	// External dependency: Must be installed separately and available in PATH.
	// See: https://github.com/golang-migrate/migrate
	// Supports PostgreSQL, MySQL, SQLite, MongoDB, and many others.
	MigrationToolMigrate MigrationTool = "migrate"
)

// Option is a functional option for configuring test databases.
type Option func(*Config)

// WithMigrations sets the migration directory.
// The directory should contain your migration files.
// You must also set WithMigrationTool() when using this option.
//
// Example:
//
//	testdb.WithMigrations("./migrations")
//	testdb.WithMigrations("../../db/migrations")
func WithMigrations(dir string) Option {
	return func(c *Config) {
		c.MigrationDir = dir
	}
}

// WithAdminDSN overrides the admin connection string.
// Use this when your database is not on localhost or uses non-default credentials.
//
// Most users don't need this - the library automatically discovers the admin DSN from
// environment variables (TEST_DATABASE_URL, DATABASE_URL) or uses sensible defaults.
//
// Example:
//
//	testdb.WithAdminDSN("postgres://user:pass@db.example.com:5432/postgres")
func WithAdminDSN(dsn string) Option {
	return func(c *Config) {
		c.AdminDSNOverride = dsn
	}
}

// WithMigrationTool sets the migration tool to use.
// You must also set WithMigrations() when using this option.
// Valid values: testdb.MigrationToolTern, testdb.MigrationToolGoose, testdb.MigrationToolMigrate
//
// Example:
//
//	testdb.WithMigrationTool(testdb.MigrationToolGoose)
//	testdb.WithMigrationTool(testdb.MigrationToolMigrate)
func WithMigrationTool(tool MigrationTool) Option {
	return func(c *Config) {
		c.MigrationTool = tool
	}
}

// WithMigrationToolPath sets the path to the migration tool binary.
// Use this if the tool is not in your PATH.
//
// Example:
//
//	testdb.WithMigrationToolPath("/usr/local/bin/goose")
func WithMigrationToolPath(path string) Option {
	return func(c *Config) {
		c.MigrationToolPath = path
	}
}

// WithDBPrefix sets the database name prefix.
// Useful for identifying test databases in a shared environment.
//
// Example:
//
//	testdb.WithDBPrefix("myapp_test")
//	// Results in database names like: myapp_test_1699564231_a1b2c3d4
func WithDBPrefix(prefix string) Option {
	return func(c *Config) {
		c.DBPrefix = prefix
	}
}

// WithVerbose enables verbose logging of database operations.
// By default, testdb operates silently. Enable this for debugging.
//
// Example:
//
//	testdb.WithVerbose()
func WithVerbose() Option {
	return func(c *Config) {
		c.Verbose = true
	}
}

// DefaultConfig returns a Config with reasonable defaults.
func DefaultConfig() Config {
	return Config{
		DBPrefix: "test",
	}
}

// discoverAdminDSN attempts to discover the admin DSN from environment variables.
// It checks in order:
//  1. TEST_DATABASE_URL
//  2. DATABASE_URL
//  3. Returns empty string if neither is set
//
// This is an internal helper called by ResolveAdminDSN.
func discoverAdminDSN() string {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return ""
}

// ResolveAdminDSN resolves the admin DSN using a consistent priority order.
// This helper consolidates the DSN resolution logic to avoid duplication across
// database-specific providers (PostgreSQL, MySQL, SQLite, etc.).
//
// The library automatically discovers the admin DSN so users don't need to
// provide it unless they have custom connection requirements.
//
// Resolution order:
//  1. cfg.AdminDSNOverride (explicit user override via WithAdminDSN)
//  2. TEST_DATABASE_URL environment variable
//  3. DATABASE_URL environment variable
//  4. defaultDSN (database-specific default)
//
// Example usage in provider initialization:
//
//	adminDSN := testdb.ResolveAdminDSN(cfg, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
func ResolveAdminDSN(cfg Config, defaultDSN string) string {
	if cfg.AdminDSNOverride != "" {
		return cfg.AdminDSNOverride
	}
	if discovered := discoverAdminDSN(); discovered != "" {
		return discovered
	}
	return defaultDSN
}

// generateDatabaseName creates a unique database name with the given prefix.
// Format: {prefix}_{timestamp}_{random}
//
// Example: test_1699564231_a1b2c3d4
func generateDatabaseName(prefix string) (string, error) {
	if prefix == "" {
		prefix = "test"
	}

	// Use nanosecond timestamp for uniqueness
	timestamp := time.Now().UnixNano()

	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		return "", fmt.Errorf("failed to generate random suffix: %w", err)
	}
	randSuffix := hex.EncodeToString(randBytes)

	return fmt.Sprintf("%s_%d_%s", prefix, timestamp, randSuffix), nil
}

const (
	// MaxDBPrefixLength is the maximum recommended length for database name prefixes.
	//
	// This limit is intentionally based on the most restrictive database to ensure
	// consistent behavior across all supported databases:
	//   - PostgreSQL: 63 bytes (most restrictive)
	//   - MySQL: 64 characters
	//   - SQLite: effectively unlimited
	//
	// Database name format: prefix_timestamp_random (prefix + 29 chars)
	// To avoid truncation: prefix + 29 <= 63, therefore prefix <= 34
	//
	// Design decision: I'm using the most restrictive limit (PostgreSQL's 63-byte limit)
	// for all databases rather than implementing database-specific validation. This
	// provides a consistent, safe experience and simplifies the API. A 34-character
	// prefix is sufficient for all practical use cases.
	MaxDBPrefixLength = 34
)

var (
	// ErrNilProvider is returned when a nil provider is passed to New().
	ErrNilProvider = errors.New("provider cannot be nil")

	// ErrNoMigrationDir is returned when RunMigrations is called without a migration directory.
	ErrNoMigrationDir = errors.New("migration directory not set")

	// ErrUnknownMigrationTool is returned when an unknown migration tool is configured.
	ErrUnknownMigrationTool = errors.New("unknown migration tool")

	// ErrMigrationToolWithoutDir is returned when a migration tool is specified without a directory.
	ErrMigrationToolWithoutDir = errors.New("migration tool specified but migration directory not set")

	// ErrMigrationDirWithoutTool is returned when a migration directory is specified without a tool.
	ErrMigrationDirWithoutTool = errors.New("migration directory specified but migration tool not set")

	// ErrPrefixTooLong is returned when the database prefix would cause identifier truncation.
	ErrPrefixTooLong = errors.New("database prefix too long: would exceed database identifier limit")
)

// Error represents a testdb error with operation context.
type Error struct {
	// Op is the operation that failed (e.g., "provider.Initialize").
	Op string

	// Err is the underlying error.
	Err error
}

func (e *Error) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("testdb: %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("testdb: %v", e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// validateConfig validates the configuration for consistency.
func validateConfig(cfg Config) error {
	// If migration directory is set, migration tool must be set
	if cfg.MigrationDir != "" && cfg.MigrationTool == "" {
		return ErrMigrationDirWithoutTool
	}

	// If migration tool is set, migration directory must be set
	if cfg.MigrationTool != "" && cfg.MigrationDir == "" {
		return ErrMigrationToolWithoutDir
	}

	// Validate prefix length to prevent database identifier truncation.
	// Database name format: prefix_timestamp_random (prefix + 29 chars)
	// Limit based on most restrictive database (PostgreSQL: 63 bytes, MySQL: 64 chars).
	// This intentionally applies to all databases (including SQLite which has no limit)
	// to provide consistent behavior and a simple API.
	if len(cfg.DBPrefix) > MaxDBPrefixLength {
		return fmt.Errorf("%w (max %d characters, got %d)",
			ErrPrefixTooLong, MaxDBPrefixLength, len(cfg.DBPrefix))
	}

	return nil
}
