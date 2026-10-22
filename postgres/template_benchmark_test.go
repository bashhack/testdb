package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BenchmarkFreshDatabase measures the time to create a fresh database from scratch
func BenchmarkFreshDatabase(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		db := postgres.New(b, &postgres.PoolInitializer{})
		_ = db
		// Cleanup happens automatically via b.Cleanup()
	}
}

// BenchmarkTemplateClone measures the time to clone from a template
func BenchmarkTemplateClone(b *testing.B) {
	ctx := context.Background()
	templateName := "bench_template_empty"

	// Setup: Create template once before benchmark
	err := postgres.CreateTemplate(ctx, templateName, nil)
	if err != nil {
		b.Fatalf("Failed to create template: %v", err)
	}
	defer postgres.DropTemplate(ctx, templateName)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		db := postgres.New(b, &postgres.PoolInitializer{},
			testdb.WithTemplate(templateName))
		_ = db
		// Cleanup happens automatically via b.Cleanup()
	}
}

// BenchmarkFreshDatabaseWithSchema measures creating fresh DB + running migrations
func BenchmarkFreshDatabaseWithSchema(b *testing.B) {
	// Create a simple migration file
	tmpDir := b.TempDir()
	migrationFile := filepath.Join(tmpDir, "001_create_users.sql")
	migrationSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX idx_users_email ON users(email);
	`
	if err := os.WriteFile(migrationFile, []byte(migrationSQL), 0644); err != nil {
		b.Fatalf("Failed to write migration: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Create fresh DB
		db := postgres.New(b, &postgres.PoolInitializer{})

		// Run "migration" (load SQL file)
		pool := db.Entity().(*pgxpool.Pool)
		sqlBytes, _ := os.ReadFile(migrationFile)
		_, err := pool.Exec(context.Background(), string(sqlBytes))
		if err != nil {
			b.Fatalf("Failed to run migration: %v", err)
		}
	}
}

// BenchmarkTemplateCloneWithSchema measures cloning from template that has schema
func BenchmarkTemplateCloneWithSchema(b *testing.B) {
	ctx := context.Background()
	templateName := "bench_template_with_schema"

	// Setup: Create template with schema
	tmpDir := b.TempDir()
	schemaFile := filepath.Join(tmpDir, "schema.sql")
	schemaSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX idx_users_email ON users(email);
	`
	if err := os.WriteFile(schemaFile, []byte(schemaSQL), 0644); err != nil {
		b.Fatalf("Failed to write schema: %v", err)
	}

	err := postgres.CreateTemplate(ctx, templateName,
		postgres.LoadSQLFile(schemaFile))
	if err != nil {
		b.Fatalf("Failed to create template: %v", err)
	}
	defer postgres.DropTemplate(ctx, templateName)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		db := postgres.New(b, &postgres.PoolInitializer{},
			testdb.WithTemplate(templateName))
		_ = db
		// Schema already present - no migration needed!
	}
}

// BenchmarkFreshDatabaseWithLargeFixtures simulates loading large SQL dumps
func BenchmarkFreshDatabaseWithLargeFixtures(b *testing.B) {
	// Create a "large" fixture file (simulating regional data)
	tmpDir := b.TempDir()
	fixtureFile := filepath.Join(tmpDir, "fixtures.sql")

	// Generate a moderately complex fixture
	var fixtureSQL string
	fixtureSQL += `
		CREATE TABLE products (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			price DECIMAL(10,2) NOT NULL,
			category TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX idx_products_category ON products(category);
		CREATE INDEX idx_products_price ON products(price);
	`
	// Insert 1000 rows to simulate fixture data
	for i := 0; i < 1000; i++ {
		fixtureSQL += "INSERT INTO products (name, price, category) VALUES ('Product " +
			string(rune(i)) + "', 19.99, 'Category');\n"
	}

	if err := os.WriteFile(fixtureFile, []byte(fixtureSQL), 0644); err != nil {
		b.Fatalf("Failed to write fixture: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		db := postgres.New(b, &postgres.PoolInitializer{})
		pool := db.Entity().(*pgxpool.Pool)

		// Load fixtures
		sqlBytes, _ := os.ReadFile(fixtureFile)
		_, err := pool.Exec(context.Background(), string(sqlBytes))
		if err != nil {
			b.Fatalf("Failed to load fixtures: %v", err)
		}
	}
}

// BenchmarkTemplateCloneWithLargeFixtures measures cloning template with fixtures
func BenchmarkTemplateCloneWithLargeFixtures(b *testing.B) {
	ctx := context.Background()
	templateName := "bench_template_with_fixtures"

	// Setup: Create template with fixtures
	tmpDir := b.TempDir()
	fixtureFile := filepath.Join(tmpDir, "fixtures.sql")

	var fixtureSQL string
	fixtureSQL += `
		CREATE TABLE products (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			price DECIMAL(10,2) NOT NULL,
			category TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX idx_products_category ON products(category);
		CREATE INDEX idx_products_price ON products(price);
	`
	for i := 0; i < 1000; i++ {
		fixtureSQL += "INSERT INTO products (name, price, category) VALUES ('Product " +
			string(rune(i)) + "', 19.99, 'Category');\n"
	}

	if err := os.WriteFile(fixtureFile, []byte(fixtureSQL), 0644); err != nil {
		b.Fatalf("Failed to write fixture: %v", err)
	}

	err := postgres.CreateTemplate(ctx, templateName,
		postgres.LoadSQLFile(fixtureFile))
	if err != nil {
		b.Fatalf("Failed to create template: %v", err)
	}
	defer postgres.DropTemplate(ctx, templateName)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		db := postgres.New(b, &postgres.PoolInitializer{},
			testdb.WithTemplate(templateName))
		_ = db
		// Fixtures already present - instant!
	}
}

// BenchmarkTemplateCreation measures one-time cost of creating a template
func BenchmarkTemplateCreation(b *testing.B) {
	ctx := context.Background()

	// Create schema for template
	tmpDir := b.TempDir()
	schemaFile := filepath.Join(tmpDir, "schema.sql")
	schemaSQL := `
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX idx_users_email ON users(email);
	`
	if err := os.WriteFile(schemaFile, []byte(schemaSQL), 0644); err != nil {
		b.Fatalf("Failed to write schema: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		templateName := "bench_template_creation_" + string(rune(i))

		err := postgres.CreateTemplate(ctx, templateName,
			postgres.LoadSQLFile(schemaFile))
		if err != nil {
			b.Fatalf("Failed to create template: %v", err)
		}

		// Cleanup
		postgres.DropTemplate(ctx, templateName)
	}
}
