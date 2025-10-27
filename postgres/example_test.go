package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Example_setup demonstrates basic usage of postgres.Setup for creating
// isolated test databases with automatic cleanup.
func Example_setup() {
	t := &testing.T{}

	// Create isolated test database with migrations
	// Cleanup is automatic via t.Cleanup()
	pool := postgres.Setup(t,
		testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate))

	_, err := pool.Exec(context.Background(),
		"INSERT INTO test_table (name) VALUES ($1)", "test")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("Database setup complete")
	fmt.Println("Data inserted successfully")

	// Output:
	// Database setup complete
	// Data inserted successfully
}

// Example_parallel shows running tests in parallel with isolated databases.
func Example_parallel() {
	t := &testing.T{}

	t.Run("create user", func(t *testing.T) {
		t.Parallel()

		pool := postgres.Setup(t,
			testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
			testdb.WithMigrationTool(testdb.MigrationToolMigrate))

		_, err := pool.Exec(context.Background(),
			"INSERT INTO users (email) VALUES ($1)", "user1@example.com")
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Println("User 1 created")
	})

	t.Run("delete user", func(t *testing.T) {
		t.Parallel()

		pool := postgres.Setup(t,
			testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
			testdb.WithMigrationTool(testdb.MigrationToolMigrate))

		_, err := pool.Exec(context.Background(),
			"INSERT INTO users (email) VALUES ($1)", "user2@example.com")
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		fmt.Println("User 2 created")
	})

	// Note: This example demonstrates the API but cannot be executed as a runnable
	// example because it uses t.Run() which requires a real testing.T instance.
}

// Example_configuration demonstrates configuration options.
func Example_configuration() {
	t := &testing.T{}

	pool := postgres.Setup(t,
		testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
		testdb.WithDBPrefix("myapp_test"),
	)

	if err := pool.Ping(context.Background()); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("Database configured successfully")
	fmt.Println("Custom prefix and migrations applied")

	// Output:
	// Database configured successfully
	// Custom prefix and migrations applied
}

// Example_helperFunction demonstrates the helper function pattern for
// consistent test setup.
func Example_helperFunction() {
	setupDB := func(t *testing.T) *pgxpool.Pool {
		t.Helper()
		return postgres.Setup(t,
			testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
			testdb.WithMigrationTool(testdb.MigrationToolMigrate),
			testdb.WithDBPrefix("myapp_test"),
		)
	}

	t := &testing.T{}
	pool := setupDB(t)

	if err := pool.Ping(context.Background()); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("Helper function setup complete")

	// Output:
	// Helper function setup complete
}

// GormExampleInitializer is a GORM initializer for examples
type GormExampleInitializer struct{}

func (g *GormExampleInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	return gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
}

// Example_gorm demonstrates using postgres.New() with GORM for custom database initialization.
func Example_gorm() {
	t := &testing.T{}

	// Create test database with GORM initializer
	// postgres.New() is used instead of postgres.Setup() when you need custom initialization
	db := postgres.New(t, &GormExampleInitializer{})

	// Get GORM DB instance
	gormDB := db.Entity().(*gorm.DB)

	// Define a model
	type Article struct {
		ID      uint   `gorm:"primaryKey"`
		Title   string `gorm:"not null"`
		Content string
	}

	// Auto-migrate schema
	if err := gormDB.AutoMigrate(&Article{}); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Create a record
	article := Article{Title: "Getting Started", Content: "Introduction to testdb"}
	if err := gormDB.Create(&article).Error; err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Query the record
	var found Article
	if err := gormDB.First(&found, "title = ?", "Getting Started").Error; err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("GORM setup complete")
	fmt.Printf("Article created: %s\n", found.Title)

	// Output:
	// GORM setup complete
	// Article created: Getting Started
}

// Example_setupVsNew demonstrates when to use Setup() vs New().
func Example_setupVsNew() {
	t := &testing.T{}

	// Use Setup() when working with pgx/pgxpool directly
	// Returns *pgxpool.Pool - simplest API
	pool := postgres.Setup(t)

	var result int
	err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Setup() returns pool, result: %d\n", result)

	// Use New() when you need custom initialization (GORM, sqlx, etc.)
	// Returns *testdb.TestDatabase with more flexibility
	db := postgres.New(t, &postgres.PoolInitializer{})
	pool2 := db.Entity().(*pgxpool.Pool)

	var result2 int
	err = pool2.QueryRow(context.Background(), "SELECT 2").Scan(&result2)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("New() returns TestDatabase, result: %d\n", result2)
	fmt.Printf("Database name starts with: %s\n", db.Name()[:5]) // Show prefix only

	// Output:
	// Setup() returns pool, result: 1
	// New() returns TestDatabase, result: 2
	// Database name starts with: test_
}

// Example_sqlDB demonstrates using postgres.New() with SqlDbInitializer for database/sql compatibility.
//
// Use SqlDbInitializer when:
//   - Your application uses database/sql interfaces
//   - You need compatibility with existing sql.DB-based code
//   - You're working with libraries that expect *sql.DB
//
// For PostgreSQL-specific features, use PoolInitializer or Setup() instead.
func Example_sqlDB() {
	t := &testing.T{}

	// Create test database with sql.DB
	db := postgres.New(t, &postgres.SqlDbInitializer{})

	// Get the *sql.DB instance
	sqlDB := db.Entity().(*sql.DB)

	// Create table
	_, err := sqlDB.Exec(`
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT UNIQUE NOT NULL
		)
	`)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Insert data
	_, err = sqlDB.Exec(
		`INSERT INTO users (name, email) VALUES ($1, $2)`,
		"Alice", "alice@example.com",
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Query data
	var name string
	err = sqlDB.QueryRow(
		`SELECT name FROM users WHERE email = $1`,
		"alice@example.com",
	).Scan(&name)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("sql.DB setup complete")
	fmt.Printf("Found user: %s\n", name)

	// Cleanup happens automatically via t.Cleanup()

	// Output:
	// sql.DB setup complete
	// Found user: Alice
}

// Example_basicUsage demonstrates creating an isolated test database.
func Example_basicUsage() {
	t := &testing.T{}

	// Create isolated test database
	pool := postgres.Setup(t)

	// Use the database - it's completely isolated
	// Cleanup is automatic via t.Cleanup()
	var result int
	err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
	if err != nil {
		fmt.Printf("Query failed: %v\n", err)
		return
	}

	fmt.Printf("Query result: %d\n", result)

	// Output:
	// Query result: 1
}

// Example_customPrefix demonstrates using a custom database name prefix.
func Example_customPrefix() {
	t := &testing.T{}

	pool := postgres.Setup(t,
		testdb.WithDBPrefix("myapp"),
	)

	// Database name will be like "myapp_1699564231_a1b2c3d4"
	var dbname string
	err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&dbname)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Verify prefix is used
	if len(dbname) < 6 || dbname[:6] != "myapp_" {
		fmt.Printf("Expected database name to start with 'myapp_', got: %s\n", dbname)
		return
	}

	fmt.Println("Custom prefix applied successfully")
	fmt.Printf("Database name starts with: %s\n", strings.Split(dbname, "_")[0])

	// Output:
	// Custom prefix applied successfully
	// Database name starts with: myapp
}

// Example_multipleOptions demonstrates combining multiple configuration options.
func Example_multipleOptions() {
	t := &testing.T{}

	pool := postgres.Setup(t,
		testdb.WithDBPrefix("test"),
		testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
	)

	// Verify database exists and migrations ran
	var dbname string
	err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&dbname)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Check migration ran
	var exists bool
	err = pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'test_table'
		)
	`).Scan(&exists)

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	if !exists {
		fmt.Println("Expected test_table to exist")
		return
	}

	fmt.Println("Multiple options configured successfully")
	fmt.Println("Database created with custom prefix")
	fmt.Println("Migrations ran and tables exist")

	// Output:
	// Multiple options configured successfully
	// Database created with custom prefix
	// Migrations ran and tables exist
}

// Example_migrationsWithMigrate demonstrates using golang-migrate.
func Example_migrationsWithMigrate() {
	t := &testing.T{}

	pool := postgres.Setup(t,
		testdb.WithMigrations("../testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
	)

	// Verify migration created the table
	var exists bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'test_table'
		)
	`).Scan(&exists)

	if err != nil {
		fmt.Printf("Failed to check table: %v\n", err)
		return
	}

	if !exists {
		fmt.Println("Expected test_table to exist after migrations")
		return
	}

	// Insert data to verify table works
	_, err = pool.Exec(context.Background(),
		"INSERT INTO test_table (name) VALUES ($1)", "test")
	if err != nil {
		fmt.Printf("Failed to insert: %v\n", err)
		return
	}

	fmt.Println("Migration with golang-migrate successful")
	fmt.Println("Table exists and data inserted")

	// Output:
	// Migration with golang-migrate successful
	// Table exists and data inserted
}

// Example_migrationsWithGoose demonstrates using goose.
func Example_migrationsWithGoose() {
	t := &testing.T{}

	pool := postgres.Setup(t,
		testdb.WithMigrations("../testdata/postgres/migrations_goose"),
		testdb.WithMigrationTool(testdb.MigrationToolGoose),
	)

	// Verify migration created the products table
	var exists bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'products'
		)
	`).Scan(&exists)

	if err != nil {
		fmt.Printf("Failed to check table: %v\n", err)
		return
	}

	if !exists {
		fmt.Println("Expected products table to exist after migrations")
		return
	}

	fmt.Println("Migration with goose successful")
	fmt.Println("Products table exists")

	// Output:
	// Migration with goose successful
	// Products table exists
}

// Example_migrationsWithTern demonstrates using tern.
func Example_migrationsWithTern() {
	t := &testing.T{}

	pool := postgres.Setup(t,
		testdb.WithMigrations("../testdata/postgres/migrations_tern"),
		testdb.WithMigrationTool(testdb.MigrationToolTern),
	)

	// Verify migration created the users table
	var exists bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'users'
		)
	`).Scan(&exists)

	if err != nil {
		fmt.Printf("Failed to check table: %v\n", err)
		return
	}

	if !exists {
		fmt.Println("Expected users table to exist after migrations")
		return
	}

	fmt.Println("Migration with tern successful")
	fmt.Println("Users table exists")

	// Output:
	// Migration with tern successful
	// Users table exists
}

// Example_parallelExecution demonstrates running tests in parallel with isolated databases.
func Example_parallelExecution() {
	t := &testing.T{}

	t.Run("test1", func(t *testing.T) {
		t.Parallel()

		pool := postgres.Setup(t)

		// Each test gets its own database
		var dbname string
		err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&dbname)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("Test1 using database: %s\n", dbname)
	})

	t.Run("test2", func(t *testing.T) {
		t.Parallel()

		pool := postgres.Setup(t)

		// This database is different from test1
		var dbname string
		err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&dbname)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("Test2 using database: %s\n", dbname)
	})

	t.Run("test3", func(t *testing.T) {
		t.Parallel()

		pool := postgres.Setup(t)

		// And this is yet another isolated database
		var dbname string
		err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&dbname)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("Test3 using database: %s\n", dbname)
	})

	// Note: This example demonstrates the API but cannot be executed as a runnable
	// example because it uses t.Run() which requires a real testing.T instance.
}
