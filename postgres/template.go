package postgres

import (
	"context"
	"fmt"
	"os"

	"github.com/bashhack/testdb"
	"github.com/jackc/pgx/v5"
)

// CreateTemplate creates a reusable template database that can be cloned
// for fast test database creation.
//
// Templates are useful when:
//   - You have large SQL dumps (fixtures) that are slow to load
//   - You need multiple fixture sets for different test suites (regional data, scenarios)
//   - You want to test migrations from specific schema versions
//
// The setup function receives a DSN and should initialize the template
// (load SQL dumps, run migrations, insert seed data, etc.).
//
// After creation, the template is marked with ALLOW_CONNECTIONS FALSE
// to prevent accidental modification and enable safe cloning.
//
// Example:
//
//	func TestMain(m *testing.M) {
//	    ctx := context.Background()
//
//	    // Create template with US region fixtures
//	    postgres.CreateTemplate(ctx, "us_region",
//	        postgres.LoadSQLFile("fixtures/us_region.sql"))
//
//	    code := m.Run()
//
//	    // Cleanup
//	    postgres.DropTemplate(ctx, "us_region")
//	    os.Exit(code)
//	}
func CreateTemplate(ctx context.Context, name string, setup func(dsn string) error, opts ...testdb.Option) error {
	provider := &PostgresProvider{}

	cfg := testdb.DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := provider.Initialize(ctx, cfg); err != nil {
		return fmt.Errorf("initialize provider: %w", err)
	}
	defer provider.Cleanup(ctx)

	// Create template database
	if err := provider.CreateDatabase(ctx, name); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}

	// Build DSN for setup
	dsn, err := provider.BuildDSN(name)
	if err != nil {
		_ = provider.DropDatabase(ctx, name) // Best effort cleanup
		return fmt.Errorf("build DSN: %w", err)
	}

	// Run setup function (migrations, SQL dumps, seed data, etc.)
	if setup != nil {
		if err := setup(dsn); err != nil {
			_ = provider.DropDatabase(ctx, name) // Best effort cleanup
			return fmt.Errorf("setup template: %w", err)
		}
	}

	// Terminate any lingering connections
	if err := provider.TerminateConnections(ctx, name); err != nil {
		return fmt.Errorf("terminate connections: %w", err)
	}

	// Mark as template and prevent connections
	// This makes the database read-only and enables safe cloning
	quotedName := pgx.Identifier{name}.Sanitize()
	if _, err := provider.conn.Exec(ctx,
		fmt.Sprintf("ALTER DATABASE %s ALLOW_CONNECTIONS FALSE", quotedName)); err != nil {
		return fmt.Errorf("mark as template: %w", err)
	}

	return nil
}

// DropTemplate drops a template database.
// This is useful for cleanup in TestMain or when templates need to be rebuilt.
//
// Example:
//
//	func TestMain(m *testing.M) {
//	    ctx := context.Background()
//
//	    // Create templates
//	    postgres.CreateTemplate(ctx, "fixtures", setup)
//
//	    code := m.Run()
//
//	    // Cleanup
//	    postgres.DropTemplate(ctx, "fixtures")
//	    os.Exit(code)
//	}
func DropTemplate(ctx context.Context, name string, opts ...testdb.Option) error {
	provider := &PostgresProvider{}

	cfg := testdb.DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := provider.Initialize(ctx, cfg); err != nil {
		return fmt.Errorf("initialize provider: %w", err)
	}
	defer provider.Cleanup(ctx)

	// Re-enable connections so we can drop it
	quotedName := pgx.Identifier{name}.Sanitize()
	_, _ = provider.conn.Exec(ctx,
		fmt.Sprintf("ALTER DATABASE %s ALLOW_CONNECTIONS TRUE", quotedName))

	// Terminate connections and drop
	if err := provider.TerminateConnections(ctx, name); err != nil {
		return fmt.Errorf("terminate connections: %w", err)
	}

	if err := provider.DropDatabase(ctx, name); err != nil {
		return fmt.Errorf("drop template: %w", err)
	}

	return nil
}

// TemplateExists checks if a template database exists.
// This is useful for conditional template creation and debugging.
//
// Example:
//
//	if !postgres.TemplateExists(ctx, "us_region") {
//	    postgres.CreateTemplate(ctx, "us_region", setup)
//	}
func TemplateExists(ctx context.Context, name string, opts ...testdb.Option) (bool, error) {
	provider := &PostgresProvider{}

	cfg := testdb.DefaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := provider.Initialize(ctx, cfg); err != nil {
		return false, fmt.Errorf("initialize provider: %w", err)
	}
	defer provider.Cleanup(ctx)

	return provider.TemplateExists(ctx, name)
}

// LoadSQLFile returns a setup function that loads a SQL file into the database.
// This is useful for loading SQL dumps or fixture data.
//
// The SQL file is executed directly via pgx connection.
//
// Example:
//
//	postgres.CreateTemplate(ctx, "fixtures",
//	    postgres.LoadSQLFile("fixtures/seed_data.sql"))
func LoadSQLFile(path string) func(string) error {
	return func(dsn string) error {
		// Read SQL file
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read SQL file %s: %w", path, err)
		}

		// Connect to database
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return fmt.Errorf("connect to database: %w", err)
		}
		defer conn.Close(ctx)

		// Execute SQL
		_, err = conn.Exec(ctx, string(sqlBytes))
		if err != nil {
			return fmt.Errorf("execute SQL from %s: %w", path, err)
		}

		return nil
	}
}

// LoadMigrations returns a setup function that runs database migrations.
// This is useful for creating templates with your schema already applied,
// avoiding the need to run migrations for every test.
//
// Supported migration tools:
//   - testdb.MigrationToolTern (PostgreSQL-only)
//   - testdb.MigrationToolGoose (multi-database)
//   - testdb.MigrationToolMigrate (multi-database)
//
// Example:
//
//	func TestMain(m *testing.M) {
//	    ctx := context.Background()
//
//	    // Create template with migrations - run once
//	    postgres.CreateTemplate(ctx, "with_schema",
//	        postgres.LoadMigrations("./migrations", testdb.MigrationToolGoose))
//
//	    code := m.Run()
//	    postgres.DropTemplate(ctx, "with_schema")
//	    os.Exit(code)
//	}
//
//	func TestUsers(t *testing.T) {
//	    db := postgres.New(t, &postgres.PoolInitializer{},
//	        testdb.WithTemplate("with_schema"))
//	    // Schema already applied - fast clone!
//	}
func LoadMigrations(dir string, tool testdb.MigrationTool) func(string) error {
	return func(dsn string) error {
		switch tool {
		case testdb.MigrationToolGoose:
			return testdb.ExecuteGooseMigrations(dsn, dir, "")
		case testdb.MigrationToolTern:
			return testdb.ExecuteTernMigrations(dsn, dir, "")
		case testdb.MigrationToolMigrate:
			return testdb.ExecuteMigrateMigrations(dsn, dir, "")
		default:
			return fmt.Errorf("unsupported migration tool: %v", tool)
		}
	}
}

// Compose combines multiple setup functions into a single function.
// Setup functions are executed in order, and if any fails, execution stops.
//
// Example:
//
//	postgres.CreateTemplate(ctx, "full",
//	    postgres.Compose(
//	        postgres.LoadMigrations("./migrations", testdb.MigrationToolGoose),
//	        postgres.LoadSQLFile("fixtures/seed.sql"),
//	    ))
func Compose(setupFuncs ...func(string) error) func(string) error {
	return func(dsn string) error {
		for _, fn := range setupFuncs {
			if fn == nil {
				continue
			}
			if err := fn(dsn); err != nil {
				return err
			}
		}
		return nil
	}
}
