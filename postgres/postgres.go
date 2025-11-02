// Package postgres provides PostgreSQL test database support for testdb.
//
// # Choosing the Right API
//
// This package provides two main functions: Setup() and New().
// Here's how to choose:
//
// Use Setup() when:
//   - You're using pgx/pgxpool directly in your application
//   - You don't need custom initialization logic
//   - You want the simplest API (just get a pool and test)
//
// Use New() when:
//   - You're using an ORM (GORM, ent, SQLBoiler)
//   - You're using database/sql interfaces (use SqlDbInitializer)
//   - You're using sqlx for struct scanning
//   - You have custom connection pooling requirements
//   - You need to wrap the connection in your own type
//   - You need custom tracing/logging/instrumentation
//
// # Basic Usage with Setup()
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
//
//	    _, err := pool.Exec(ctx, "INSERT INTO users (email) VALUES ($1)", "test@example.com")
//	    require.NoError(t, err)
//	}
//
// # Built-in Initializers
//
// The package provides two built-in initializers:
//
// PoolInitializer (default) - Creates *pgxpool.Pool for PostgreSQL-specific features:
//
//	pool := postgres.Setup(t)  // Uses PoolInitializer
//	// or
//	db := postgres.New(t, &postgres.PoolInitializer{})
//	pool := db.Entity().(*pgxpool.Pool)
//
// SqlDbInitializer - Creates *sql.DB for database/sql compatibility:
//
//	db := postgres.New(t, &postgres.SqlDbInitializer{})
//	sqlDB := db.Entity().(*sql.DB)
//	// Use standard database/sql operations
//	sqlDB.QueryRow("SELECT * FROM users WHERE id = $1", 1)
//
// # Custom Initializer Examples
//
// When your application uses an ORM like GORM, you need New() with a custom initializer:
//
//	type GormInitializer struct{}
//
//	func (g *GormInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    return gorm.Open(postgres.Open(dsn), &gorm.Config{})
//	}
//
//	func TestWithGORM(t *testing.T) {
//	    db := postgres.New(t, &GormInitializer{})
//	    gormDB := db.Entity().(*gorm.DB)
//	    // Now you can call your application functions that expect *gorm.DB
//	}
//
// When your application wraps connections in a custom type:
//
//	type AppDB struct {
//	    Pool    *pgxpool.Pool
//	    Timeout time.Duration
//	}
//
//	type AppDBInitializer struct{}
//
//	func (a *AppDBInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    pool, err := pgxpool.New(ctx, dsn)
//	    if err != nil {
//	        return nil, err
//	    }
//	    return &AppDB{Pool: pool, Timeout: 30 * time.Second}, nil
//	}
//
//	func TestWithCustomType(t *testing.T) {
//	    db := postgres.New(t, &AppDBInitializer{})
//	    appDB := db.Entity().(*AppDB)
//	    // Now you can call your application functions that expect *AppDB
//	}
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresProvider implements testdb.Provider for PostgreSQL.
// It handles database creation, deletion, and connection management.
type PostgresProvider struct {
	conn        *pgx.Conn
	adminDSN    string          // Store the admin DSN for use in migrations
	adminConfig *pgx.ConnConfig // Cached parsed config (avoid re-parsing on every BuildDSN)
	sslmode     string          // Cached SSL mode (extracted once from adminDSN)
}

// PoolInitializer is the default initializer for PostgreSQL connections.
// It creates a pgxpool.Pool with sensible defaults for testing.
type PoolInitializer struct {
	// ConfigModifier allows customization of the pool configuration after
	// the DSN is parsed but before the pool is created.
	// If nil, sensible defaults for testing are applied.
	ConfigModifier func(*pgxpool.Config)
}

// Initialize sets up the PostgreSQL provider with admin credentials.
// It establishes a connection to the admin database which will be used
// for creating and managing test databases.
func (p *PostgresProvider) Initialize(ctx context.Context, cfg testdb.Config) error {
	const defaultPostgresDSN = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	adminDSN := testdb.ResolveAdminDSN(cfg, defaultPostgresDSN)

	// Store the admin DSN for later use (e.g., migrations)
	p.adminDSN = adminDSN

	config, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return fmt.Errorf("parse admin DSN: %w", err)
	}

	// Cache parsed config to avoid re-parsing in BuildDSN
	p.adminConfig = config.Copy()

	// Extract and cache SSL mode to avoid URL parsing in BuildDSN
	p.sslmode = "disable"
	if strings.Contains(adminDSN, "sslmode=") {
		if u, err := url.Parse(adminDSN); err == nil {
			if mode := u.Query().Get("sslmode"); mode != "" {
				p.sslmode = mode
			}
		}
	} else if config.TLSConfig != nil {
		p.sslmode = "require"
	}

	p.conn, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect to admin database: %w", err)
	}

	return nil
}

// CreateDatabase creates a new PostgreSQL database with the given name.
func (p *PostgresProvider) CreateDatabase(ctx context.Context, name string) error {
	quotedName := pgx.Identifier{name}.Sanitize()
	_, err := p.conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", quotedName))
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// DropDatabase drops a PostgreSQL database if it exists.
// Retries on SQLSTATE 55006 to handle the race where pg_terminate_backend() has sent
// termination signals but connections haven't fully closed yet. This is especially
// important under high concurrency when multiple databases are being dropped simultaneously.
func (p *PostgresProvider) DropDatabase(ctx context.Context, name string) error {
	quotedName := pgx.Identifier{name}.Sanitize()

	// Retry for "database is being accessed by other users" (SQLSTATE 55006)
	var lastErr error
	for attempt := range 3 {
		_, err := p.conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quotedName))
		if err == nil {
			return nil
		}

		// Check for "database is being accessed" error
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55006" {
			lastErr = err
			if attempt < 2 {
				// Exponential backoff: 10ms, 40ms
				sleepDuration := time.Duration(10*(1<<(attempt*2))) * time.Millisecond
				time.Sleep(sleepDuration)
				continue
			}
		}

		// Non-retryable errors...
		return fmt.Errorf("drop database: %w", err)
	}

	return fmt.Errorf("drop database after retries: %w", lastErr)
}

// TerminateConnections forcefully terminates all connections to the specified database.
// This is necessary before dropping a database, as active connections will prevent deletion.
//
// This implementation uses a two-step approach to handle the race condition between
// pool.Close() and pg_stat_activity updates:
// 1. DISALLOW new connections (prevents races)
// 2. TERMINATE existing connections
func (p *PostgresProvider) TerminateConnections(ctx context.Context, name string) error {
	quotedName := pgx.Identifier{name}.Sanitize()

	// Step 1: Prevent new connections from being created
	// This is CRITICAL - it eliminates race conditions where new connections appear
	// during the pg_stat_activity eventual consistency lag window.
	_, err := p.conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s ALLOW_CONNECTIONS FALSE", quotedName))
	if err != nil {
		return fmt.Errorf("disallow connections: %w", err)
	}

	// Step 2: Terminate any existing connections
	// Now that new connections are blocked, we can safely terminate stragglers
	_, err = p.conn.Exec(ctx, `
        SELECT pg_terminate_backend(pg_stat_activity.pid)
        FROM pg_stat_activity
        WHERE pg_stat_activity.datname = $1
        AND pid <> pg_backend_pid();
    `, name)
	if err != nil {
		return fmt.Errorf("terminate connections: %w", err)
	}

	return nil
}

// ResolvedAdminDSN returns the resolved admin DSN being used by this provider.
// This is the actual DSN after resolving user overrides, environment variables, and defaults.
// Useful for migrations and other operations that need the admin connection string.
func (p *PostgresProvider) ResolvedAdminDSN() string {
	return p.adminDSN
}

// BuildDSN constructs a PostgreSQL connection string (DSN) for the specified database.
// It uses the cached admin config to avoid re-parsing on every call.
func (p *PostgresProvider) BuildDSN(dbName string) (string, error) {
	// Use cached config instead of parsing
	if p.adminConfig == nil {
		return "", fmt.Errorf("provider not initialized")
	}

	config := p.adminConfig
	if config.Host == "" || config.Port == 0 || config.User == "" || config.Password == "" {
		return "", fmt.Errorf("incomplete admin DSN: host, port, user and password must be specified")
	}

	// Build DSN string directly - simple string concatenation is faster than fmt.Sprintf
	// for this use case and allocates less memory...
	return "postgres://" + config.User + ":" + config.Password +
		"@" + config.Host + ":" + fmt.Sprint(config.Port) + "/" + dbName +
		"?sslmode=" + p.sslmode, nil
}

// Cleanup performs the necessary cleanup of the provider's resources.
// This includes closing the admin database connection.
func (p *PostgresProvider) Cleanup(ctx context.Context) error {
	if p.conn != nil {
		return p.conn.Close(ctx)
	}
	return nil
}

// runMigrationsIfConfigured runs migrations if the database was configured with a migration directory.
// It calls t.Fatalf if migrations fail, so this function does not return on error.
func runMigrationsIfConfigured(t testing.TB, db *testdb.TestDatabase, callerName string) {
	if db.Config().MigrationDir != "" {
		if err := db.RunMigrations(); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				t.Logf("Warning: failed to close database after migration error: %v", closeErr)
			}
			t.Fatalf("%s: migrations failed: %v", callerName, err)
		}
	}
}

// registerCleanup registers cleanup that closes the connection pool before dropping the database.
func registerCleanup(t testing.TB, db *testdb.TestDatabase) {
	t.Cleanup(func() {
		// Close the pool/connection if it implements io.Closer
		if entity := db.Entity(); entity != nil {
			if closer, ok := entity.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Logf("Warning: failed to close entity: %v", err)
				}
			}
		}

		// Drop database (TerminateConnections handles any remaining connections)
		if err := db.Close(); err != nil {
			t.Errorf("testdb cleanup failed: %v", err)
		}
	})
}

// InitializeTestDatabase creates a pgxpool.Pool for the test database.
func (pi *PoolInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}

	if pi.ConfigModifier != nil {
		pi.ConfigModifier(config)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

// Setup creates a PostgreSQL test database and returns a ready-to-use connection pool.
//
// When to use Setup():
//   - You're using pgx/pgxpool directly (recommended for most PostgreSQL applications)
//   - You want the simplest API - just get a pool and start testing
//   - You don't need custom initialization logic
//
// When to use New() instead:
//   - You need a custom initializer (GORM, sqlx, ent, or custom wrapper)
//   - You need access to the TestDatabase for DSN or configuration
//   - You need to run migrations manually (not during setup)
//
// When to use testdb.New() instead:
//   - You need fine-grained control over cleanup timing
//   - You're implementing a custom database provider
//
// The function:
//  1. Creates an isolated test database
//  2. Runs migrations if configured
//  3. Returns a *pgxpool.Pool ready for testing
//  4. Registers cleanup via t.Cleanup() - no manual cleanup needed
//
// IMPORTANT: Do NOT call pool.Close() or defer any cleanup.
// The function automatically registers cleanup that will run after your test.
//
// Calls t.Fatal() on any error.
//
// Example:
//
//	func TestUsers(t *testing.T) {
//	    pool := postgres.Setup(t,
//	        testdb.WithMigrations("./migrations"),
//	        testdb.WithMigrationTool(testdb.MigrationToolTern))
//	    // Use pool for testing - NO defer pool.Close() needed!
//	    // Cleanup happens automatically via t.Cleanup()
//	}
func Setup(t testing.TB, opts ...testdb.Option) *pgxpool.Pool {
	t.Helper()

	provider := &PostgresProvider{}
	initializer := &PoolInitializer{}

	db, err := testdb.New(t, provider, initializer, opts...)
	if err != nil {
		t.Fatalf("postgres.Setup: %v", err)
	}

	runMigrationsIfConfigured(t, db, "postgres.Setup")

	registerCleanup(t, db)

	return db.Entity().(*pgxpool.Pool)
}

// New creates a PostgreSQL test database with a custom initializer.
//
// When to use New():
//   - You're using GORM, sqlx, ent, or another ORM/database wrapper
//   - You have custom initialization logic (connection pooling, logging, tracing)
//   - You need access to the TestDatabase (for DSN, config, or manual migration control)
//   - You want to type-assert to your own database type
//
// When to use Setup() instead:
//   - You're using pgx/pgxpool directly (simpler API)
//   - You don't need custom initialization
//
// When to use testdb.New() instead:
//   - You need manual cleanup control (not automatic via t.Cleanup)
//   - You're implementing a custom database provider
//
// The function:
//  1. Creates an isolated test database
//  2. Runs migrations if configured
//  3. Returns a *testdb.TestDatabase with custom entity
//  4. Registers cleanup via t.Cleanup() - no manual cleanup needed
//
// IMPORTANT: Do NOT call db.Close() or manually close the entity.
// The function automatically registers cleanup via t.Cleanup() that will:
//  1. Close the entity (pool, GORM db, sqlx db, etc.) if it implements io.Closer
//  2. Drop the test database
//  3. Clean up provider resources
//
// Calls t.Fatal() on any error.
//
// Example with GORM:
//
//	type GormInitializer struct{}
//
//	func (g *GormInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    return gorm.Open(postgres.Open(dsn), &gorm.Config{})
//	}
//
//	func TestAdvanced(t *testing.T) {
//	    db := postgres.New(t, &GormInitializer{},
//	        testdb.WithMigrations("./migrations"),
//	        testdb.WithMigrationTool(testdb.MigrationToolGoose))
//	    gormDB := db.Entity().(*gorm.DB)
//	    // Use gormDB for testing - NO defer needed, cleanup is automatic!
//	}
//
// Example with sqlx:
//
//	type SqlxInitializer struct{}
//
//	func (s *SqlxInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
//	    return sqlx.Connect("postgres", dsn)
//	}
//
//	func TestSqlx(t *testing.T) {
//	    db := postgres.New(t, &SqlxInitializer{})
//	    sqlxDB := db.Entity().(*sqlx.DB)
//	    // Use sqlxDB for testing - NO defer needed, cleanup is automatic!
//	}
func New(t testing.TB, initializer testdb.DBInitializer, opts ...testdb.Option) *testdb.TestDatabase {
	t.Helper()

	if initializer == nil {
		t.Fatalf("postgres.New: initializer cannot be nil\n" +
			"  Use postgres.Setup() for a ready-to-use connection pool\n" +
			"  Use testdb.New() for low-level API with manual initialization")
	}

	provider := &PostgresProvider{}

	db, err := testdb.New(t, provider, initializer, opts...)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}

	runMigrationsIfConfigured(t, db, "postgres.New")

	registerCleanup(t, db)

	return db
}
