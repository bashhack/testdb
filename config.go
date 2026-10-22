package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
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

	// Migrator prepares each new test database, typically by running the
	// project's migrations. nil (the default) leaves the database empty.
	// See WithMigrator.
	Migrator Migrator

	// DBPrefix is prepended to test database names.
	// Useful for identifying test databases in a shared environment.
	//
	// Default: "test"
	// Example database name: "test_1699564231_a1b2c3d4"
	DBPrefix string

	// TemplateName specifies a template database to clone instead of creating a fresh database.
	// Templates are significantly faster than fresh database creation, especially for large schemas
	// or databases with pre-seeded data.
	//
	// If empty (default), a fresh database is created.
	// If specified, the template database must already exist (see postgres.EnsureTemplate).
	//
	// Example: "us_region" or "seeded_schema"
	//
	// Performance impact:
	//   - Fresh database: ~12-15ms (PostgreSQL CREATE DATABASE)
	//   - Template clone: ~3-5ms (PostgreSQL CREATE DATABASE WITH TEMPLATE)
	TemplateName string

	// Verbose enables logging of database operations.
	// When false (default), testdb operates silently.
	// When true, logs database creation, cleanup, and migration completion.
	//
	// Default: false
	Verbose bool

	// KeepOnFailure leaves a test's database in place when the test fails,
	// and logs a connection string for it, so the data can be inspected.
	// See WithKeepOnFailure.
	//
	// Default: false, or true when the TESTDB_KEEP_ON_FAILURE environment
	// variable is set to 1, true, yes or on.
	KeepOnFailure bool
}

// Migrator prepares a freshly created, empty database: it runs migrations,
// loads fixtures, or does whatever the tests need to find in place. It
// receives the DSN of the database to prepare.
//
// Adapters for the common migration tools live in their own modules, so a
// project only depends on the tool it uses:
//   - github.com/bashhack/testdb/migrate/goose
//   - github.com/bashhack/testdb/migrate/tern
//   - github.com/bashhack/testdb/migrate/golangmigrate
//
// For plain SQL without a tool, see postgres.SQLFiles and postgres.LoadSQLFile.
// Template setup functions have the same shape (postgres.SetupFunc is a
// MigratorFunc), so one Migrator serves both per-test databases and templates.
type Migrator interface {
	Migrate(ctx context.Context, dsn string) error
}

// MigratorFunc adapts a function to the Migrator interface.
type MigratorFunc func(ctx context.Context, dsn string) error

// Migrate calls f.
func (f MigratorFunc) Migrate(ctx context.Context, dsn string) error {
	return f(ctx, dsn)
}

// Option is a functional option for configuring test databases.
type Option func(*Config)

// WithMigrator sets how each new test database is prepared before the test
// gets it. Setup and New run the migrator right after creating the database.
//
// Example:
//
//	pool := postgres.Setup(t, testdb.WithMigrator(goose.Dir("./migrations")))
//	pool := postgres.Setup(t, testdb.WithMigrator(postgres.SQLFiles("./schema")))
func WithMigrator(m Migrator) Option {
	return func(c *Config) {
		c.Migrator = m
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

// WithTemplate configures the test database to be created from a template database.
// This is significantly faster than creating a fresh database, especially for large
// schemas or databases with pre-seeded data.
//
// The template database must already exist (see postgres.EnsureTemplate).
// If the template doesn't exist, database creation will fail.
//
// Performance impact:
//   - Fresh database: ~12-15ms
//   - Template clone: ~3-5ms (3-5x faster)
//
// Common use cases:
//   - Large SQL dumps (fixtures) loaded once, cloned per test
//   - Different fixture sets for different test suites (us_region, eu_region)
//   - Testing migrations from specific schema versions
//
// Example:
//
//	db := postgres.New(t, &postgres.PoolInitializer{},
//	    testdb.WithTemplate("us_region"))
func WithTemplate(name string) Option {
	return func(c *Config) {
		c.TemplateName = name
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

// WithKeepOnFailure keeps a test's database when the test fails, instead of
// dropping it, and logs a psql command for it. Passing tests still have their
// databases dropped. Use it to look at the data a failing test left behind.
//
// The TESTDB_KEEP_ON_FAILURE environment variable (1, true, yes or on) turns
// this on for a whole run without changing code:
//
//	TESTDB_KEEP_ON_FAILURE=1 go test ./... -run TestCheckout
//
// Kept databases are yours to drop afterwards; WithDBPrefix makes them easy
// to spot. Leave this off in CI, where nobody is going to look and the
// databases would only pile up.
func WithKeepOnFailure() Option {
	return func(c *Config) {
		c.KeepOnFailure = true
	}
}

// KeepOnFailureEnv is the environment variable that turns on KeepOnFailure
// for a run.
const KeepOnFailureEnv = "TESTDB_KEEP_ON_FAILURE"

// DefaultConfig returns a Config with reasonable defaults.
func DefaultConfig() Config {
	return Config{
		DBPrefix:      "test",
		KeepOnFailure: envBool(KeepOnFailureEnv),
	}
}

// envBool reports whether an environment variable is set to a true-ish value.
func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
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

	// ErrNoMigrator is returned when RunMigrations is called without a Migrator configured.
	ErrNoMigrator = errors.New("no migrator configured")

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
