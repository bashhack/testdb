package testdb_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
)

// Example_new demonstrates using testdb.New with a custom initializer
// for full control over database initialization.
func Example_new() {
	t := &testing.T{}

	provider := &postgres.PostgresProvider{}
	initializer := &postgres.PoolInitializer{}

	db, err := testdb.New(t, provider, initializer,
		testdb.WithMigrations("./testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
		testdb.WithDBPrefix("myapp_test"),
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			fmt.Printf("Failed to close database: %v\n", err)
		}
	}()

	// DSN is always available
	fmt.Println("Database created successfully")

	if err := db.RunMigrations(); err != nil {
		fmt.Printf("Migration error: %v\n", err)
		return
	}
	fmt.Println("Migrations completed")

	// Output:
	// Database created successfully
	// Migrations completed
}

// Example_dsnOnly demonstrates using testdb to get just a DSN
// without automatic connection initialization.
func Example_dsnOnly() {
	t := &testing.T{}

	provider := &postgres.PostgresProvider{}

	// Pass nil initializer to skip connection initialization
	db, err := testdb.New(t, provider, nil,
		testdb.WithMigrations("./testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			fmt.Printf("Failed to close database: %v\n", err)
		}
	}()

	// Use the DSN with your own connection logic
	dsn := db.DSN
	_ = dsn // Connect with your preferred client
	fmt.Println("DSN created successfully")

	// Output:
	// DSN created successfully
}

// Example_customInitializer shows implementing a custom initializer
// for integration with ORMs or custom database types.
func Example_customInitializer() {
	t := &testing.T{}

	provider := &postgres.PostgresProvider{}
	initializer := &exampleInitializer{}

	db, err := testdb.New(t, provider, initializer,
		testdb.WithMigrations("./testdata/postgres/migrations_migrate"),
		testdb.WithMigrationTool(testdb.MigrationToolMigrate),
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			fmt.Printf("Failed to close database: %v\n", err)
		}
	}()

	// Type assert to your custom type
	// myDB := db.Entity().(*myapp.DB)
	fmt.Println("Custom initializer setup complete")

	// Output:
	// Custom initializer setup complete
}

// exampleInitializer is a custom initializer for demonstration.
type exampleInitializer struct{}

func (e *exampleInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	// Your custom initialization logic here
	// For example: return gorm.Open(postgres.Open(dsn), &gorm.Config{})
	return nil, nil
}
