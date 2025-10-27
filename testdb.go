// Package testdb provides isolated test database instances for Go tests.
// It enables true parallel testing by creating a unique database for each test,
// with migration support and optional automatic cleanup.
//
// Supported databases:
//   - PostgreSQL (github.com/bashhack/testdb/postgres)
//
// Basic usage with PostgreSQL (automatic cleanup via postgres.Setup):
//
//	import (
//	    "github.com/bashhack/testdb"
//	    "github.com/bashhack/testdb/postgres"
//	)
//
//	func TestUsers(t *testing.T) {
//	    pool := postgres.Setup(t,
//	        testdb.WithMigrations("./migrations"),
//	        testdb.WithMigrationTool(testdb.MigrationToolTern))
//	    // Use pool for testing...
//	    // Cleanup is automatic via t.Cleanup()
//	}
//
// API Levels:
//
// The library provides three levels of API with different use cases and cleanup behavior:
//
// Level 1 - postgres.Setup() [Recommended for most users]:
//   - Use when: Working with pgx/pgxpool directly
//   - Returns: *pgxpool.Pool (ready to use)
//   - Cleanup: Automatic via t.Cleanup() - DO NOT call Close() manually
//   - Best for: Standard PostgreSQL testing with pgx
//
// Level 2 - postgres.New() [For custom database wrappers]:
//   - Use when: Using GORM, sqlx, ent, or custom initialization
//   - Returns: *testdb.TestDatabase with custom entity
//   - Cleanup: Automatic via t.Cleanup() - DO NOT call db.Close() manually
//   - Best for: Custom ORMs, connection wrappers, or when you need the TestDatabase
//
// Level 3 - testdb.New() [Low-level API]:
//   - Use when: Need manual cleanup control or implementing custom providers
//   - Returns: *testdb.TestDatabase
//   - Cleanup: Manual - YOU MUST call defer db.Close()
//   - Best for: Advanced use cases requiring cleanup timing control
//
// See the postgres.Setup(), postgres.New(), and Close() documentation for details.
package testdb

import (
	"context"
	"testing"
)

// TestDatabase represents an isolated test database instance.
// It manages the complete lifecycle of a test database, including:
//   - Creation and initialization
//   - Migration management
//   - Connection management
//   - Cleanup and resource disposal
type TestDatabase struct {
	// name is the unique identifier for this test database.
	// Example: "test_1699564231_a1b2c3d4"
	name string

	// dsn is the connection string for this specific test database.
	// This is always available and can be used with any database client.
	dsn string

	// config holds the configuration used to create this database.
	config Config

	// cleanup is the function called by Close() to clean up resources.
	cleanup func() error

	// t is the testing context for logging.
	t testingHelper

	// entity holds the initialized database connection/client.
	// Type assert this to your expected type (e.g., *pgxpool.Pool, *sqlx.DB).
	entity any

	// provider is the database-specific implementation.
	provider Provider
}

// Name returns the unique database name for this test database.
func (td *TestDatabase) Name() string {
	return td.name
}

// DSN returns the connection string for this test database.
func (td *TestDatabase) DSN() string {
	return td.dsn
}

// Config returns the configuration used to create this database.
func (td *TestDatabase) Config() Config {
	return td.config
}

// Provider defines database-specific operations that must be implemented
// for each supported database system (PostgreSQL, MySQL, SQLite, MongoDB).
//
// This interface is typically implemented by database-specific packages
// and is not usually used directly by end users.
type Provider interface {
	// Initialize sets up the provider with admin credentials.
	// This establishes a connection to the admin/system database.
	Initialize(ctx context.Context, cfg Config) error

	// CreateDatabase creates a new database with the given name.
	CreateDatabase(ctx context.Context, name string) error

	// DropDatabase drops an existing database.
	DropDatabase(ctx context.Context, name string) error

	// TerminateConnections forcefully closes all connections to a database.
	// This is necessary before dropping a database.
	TerminateConnections(ctx context.Context, name string) error

	// BuildDSN constructs a connection string for the given database name.
	BuildDSN(dbName string) (string, error)

	// ResolvedAdminDSN returns the resolved admin DSN being used by this provider.
	// This is the actual DSN after resolving user overrides, environment variables, and defaults.
	// Useful for migrations and other operations that need admin credentials.
	ResolvedAdminDSN() string

	// Cleanup performs any necessary provider cleanup (e.g., closing admin connections).
	Cleanup(ctx context.Context) error
}

// DBInitializer defines the interface for custom database initialization in tests.
//
// # When You Need a Custom Initializer
//
// Most users should use database-specific convenience functions (e.g., postgres.Setup)
// and don't need to implement this interface. Implement DBInitializer when:
//
// 1. Using an ORM (GORM, ent, SQLBoiler):
//
//	type GormInitializer struct{}
//	func (g *GormInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    return gorm.Open(postgres.Open(dsn), &gorm.Config{})
//	}
//
// 2. Using sqlx for struct scanning:
//
//	type SqlxInitializer struct{}
//	func (s *SqlxInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    return sqlx.Connect("postgres", dsn)
//	}
//
// 3. Wrapping connections in your application's custom type:
//
//	type AppDB struct {
//	    Pool    *pgxpool.Pool
//	    Timeout time.Duration
//	}
//	type AppDBInitializer struct{}
//	func (a *AppDBInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    pool, err := pgxpool.New(ctx, dsn)
//	    if err != nil {
//	        return nil, err
//	    }
//	    return &AppDB{Pool: pool, Timeout: 30 * time.Second}, nil
//	}
//
// 4. Custom connection pooling settings:
//
//	type CustomPoolInitializer struct {
//	    MaxConns int32
//	}
//	func (c *CustomPoolInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    config, _ := pgxpool.ParseConfig(dsn)
//	    config.MaxConns = c.MaxConns
//	    config.MinConns = 2
//	    config.MaxConnLifetime = 1 * time.Hour
//	    return pgxpool.NewWithConfig(ctx, config)
//	}
//
// 5. Adding tracing/logging/instrumentation:
//
//	type TracedInitializer struct{}
//	func (t *TracedInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    config, _ := pgxpool.ParseConfig(dsn)
//	    config.ConnConfig.Tracer = &MyQueryTracer{}
//	    return pgxpool.NewWithConfig(ctx, config)
//	}
//
// # Why Use a Custom Initializer?
//
// The key benefit is that your tests use the SAME database type as your application code.
// If your app functions expect *gorm.DB, your tests should use *gorm.DB too.
// If your app uses a custom AppDB wrapper, your tests should use that wrapper.
// This ensures your tests accurately reflect real-world usage.
type DBInitializer interface {
	// InitializeTestDatabase creates and initializes a database connection/pool.
	// It receives a DSN (Data Source Name) for connecting to the test database
	// and returns an any that should be type-asserted by the caller to
	// their specific database entity type.
	//
	// Parameters:
	//   - ctx: Context for cancellation and timeouts
	//   - dsn: Connection string for the test database
	//
	// Returns:
	//   - any: Database entity (type assert to your specific type)
	//   - error: Any initialization errors
	InitializeTestDatabase(ctx context.Context, dsn string) (any, error)
}

// testingHelper is a minimal interface that both *testing.T and *testing.B satisfy.
// This allows TestDatabase to work with both regular tests and benchmarks.
type testingHelper interface {
	Logf(format string, args ...any)
	Helper()
}

// New creates a test database using the provided provider and optional initializer.
//
// This is the low-level API for creating test databases. Most users should use
// the database-specific convenience functions instead (e.g., postgres.Setup()).
//
// If initializer is nil, no automatic connection initialization is performed.
// The DSN field will still be available for manual connection setup.
//
// Parameters:
//   - t: Testing context for logging and cleanup
//   - provider: Database-specific provider implementation
//   - initializer: Optional custom initializer (can be nil)
//   - opts: Configuration options
//
// Returns:
//   - *TestDatabase: Handle to manage the test database
//   - error: Any errors during creation
//
// Example:
//
//	provider := &postgres.PostgresProvider{}
//	initializer := &postgres.PoolInitializer{}
//	db, err := testdb.New(t, provider, initializer,
//	    testdb.WithMigrations("./migrations"),
//	    testdb.WithMigrationTool(testdb.MigrationToolTern),
//	)
//	if err != nil {
//	    t.Fatal(err)
//	}
//	defer db.Close()
//
//	pool := db.Entity().(*pgxpool.Pool)
func New(t testing.TB, provider Provider, initializer DBInitializer, opts ...Option) (*TestDatabase, error) {
	t.Helper()

	if provider == nil {
		return nil, &Error{
			Op:  "testdb.New",
			Err: ErrNilProvider,
		}
	}

	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := validateConfig(cfg); err != nil {
		return nil, &Error{
			Op:  "testdb.New",
			Err: err,
		}
	}

	ctx := context.Background()
	if err := provider.Initialize(ctx, cfg); err != nil {
		return nil, &Error{
			Op:  "provider.Initialize",
			Err: err,
		}
	}

	dbName, err := generateDatabaseName(cfg.DBPrefix)
	if err != nil {
		return nil, &Error{
			Op:  "generateDatabaseName",
			Err: err,
		}
	}

	if cfg.Verbose {
		t.Logf("testdb: creating database %s", dbName)
	}

	if err := provider.CreateDatabase(ctx, dbName); err != nil {
		return nil, &Error{
			Op:  "provider.CreateDatabase",
			Err: err,
		}
	}

	testDSN, err := provider.BuildDSN(dbName)
	if err != nil {
		_ = provider.DropDatabase(ctx, dbName) // Best effort cleanup
		return nil, &Error{
			Op:  "provider.BuildDSN",
			Err: err,
		}
	}

	td := &TestDatabase{
		name:     dbName,
		config:   cfg,
		dsn:      testDSN,
		t:        t,
		provider: provider,
	}

	td.cleanup = func() error {
		if err := provider.TerminateConnections(ctx, dbName); err != nil {
			return &Error{
				Op:  "provider.TerminateConnections",
				Err: err,
			}
		}

		if err := provider.DropDatabase(ctx, dbName); err != nil {
			return &Error{
				Op:  "provider.DropDatabase",
				Err: err,
			}
		}

		if err := provider.Cleanup(ctx); err != nil {
			return &Error{
				Op:  "provider.Cleanup",
				Err: err,
			}
		}

		if cfg.Verbose {
			t.Logf("testdb: dropped database %s", dbName)
		}
		return nil
	}

	if initializer != nil {
		entity, err := initializer.InitializeTestDatabase(ctx, td.dsn)
		if err != nil {
			_ = td.Close() // Best effort cleanup
			return nil, &Error{
				Op:  "initializer.InitializeTestDatabase",
				Err: err,
			}
		}
		td.entity = entity
	}

	return td, nil
}

// Entity returns the initialized database entity.
// This is only available if a DBInitializer was provided to New().
//
// Type assertions are required since the entity type depends on your DBInitializer.
//
// Common usage (direct assertion - panics on type mismatch):
//
//	pool := db.Entity().(*pgxpool.Pool)
//	gormDB := db.Entity().(*gorm.DB)
//	sqlxDB := db.Entity().(*sqlx.DB)
//
// Safe assertion (checks type before asserting):
//
//	entity := db.Entity()
//	pool, ok := entity.(*pgxpool.Pool)
//	if !ok {
//	    t.Fatalf("expected *pgxpool.Pool, got %T", entity)
//	}
//
// Note: Since you control the DBInitializer, direct assertions are usually safe.
// Panics during test setup help catch initialization bugs early.
func (td *TestDatabase) Entity() any {
	return td.entity
}

// logf logs a message if verbose mode is enabled.
func (td *TestDatabase) logf(format string, args ...any) {
	if td.config.Verbose {
		td.t.Logf(format, args...)
	}
}

// RunMigrations executes database migrations using the configured migration tool.
// The migration directory and tool must both be set via WithMigrations() and
// WithMigrationTool() options.
//
// Supported migration tools:
//   - Tern (github.com/jackc/tern) - PostgreSQL only
//   - Goose (github.com/pressly/goose) - PostgreSQL, MySQL, SQLite
//   - golang-migrate (github.com/golang-migrate/migrate) - PostgreSQL, MySQL, SQLite, MongoDB, and more
//
// The migration tool binary must be available in PATH or specified via
// WithMigrationToolPath() option.
//
// Returns an error if migrations fail. The database is NOT automatically
// cleaned up on migration failure - call Close() manually if needed.
//
// Example:
//
//	db, err := testdb.New(t, provider, initializer,
//	    testdb.WithMigrations("./migrations"),
//	    testdb.WithMigrationTool(testdb.MigrationToolTern),
//	)
//	if err != nil {
//	    t.Fatal(err)
//	}
//	defer db.Close()
//
//	if err := db.RunMigrations(); err != nil {
//	    t.Fatalf("migrations failed: %v", err)
//	}
func (td *TestDatabase) RunMigrations() error {
	if td.config.MigrationDir == "" {
		return &Error{
			Op:  "RunMigrations",
			Err: ErrNoMigrationDir,
		}
	}

	switch td.config.MigrationTool {
	case MigrationToolTern:
		return td.runTernMigrations()
	case MigrationToolGoose:
		return td.runGooseMigrations()
	case MigrationToolMigrate:
		return td.runMigrateMigrations()
	default:
		return &Error{
			Op:  "RunMigrations",
			Err: ErrUnknownMigrationTool,
		}
	}
}

// Close cleans up the test database and associated resources.
//
// This method:
//  1. Terminates all active connections to the database
//  2. Drops the database
//  3. Cleans up provider resources
//
// When to call Close():
//   - Manual cleanup is required when using the low-level testdb.New() API directly
//   - Cleanup is AUTOMATIC when using database-specific Setup/New functions
//     (e.g., postgres.Setup(), postgres.New()) - they register cleanup via t.Cleanup()
//
// Example with low-level API (manual cleanup required):
//
//	db, err := testdb.New(t, provider, initializer)
//	if err != nil {
//	    t.Fatal(err)
//	}
//	defer db.Close()
//
// Or with t.Cleanup:
//
//	db, err := testdb.New(t, provider, initializer)
//	if err != nil {
//	    t.Fatal(err)
//	}
//	t.Cleanup(func() { db.Close() })
//
// Example with high-level API (automatic cleanup):
//
//	pool := postgres.Setup(t)  // Cleanup registered automatically
//	// No need to call Close() - handled by t.Cleanup()
func (td *TestDatabase) Close() error {
	td.t.Helper()

	if td.cleanup == nil {
		return nil // Already closed
	}

	td.logf("testdb: cleaning up database %s", td.name)

	err := td.cleanup()
	td.cleanup = nil // Mark as closed
	return err
}
