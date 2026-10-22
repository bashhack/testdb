package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const benchSchemaSQL = `
	CREATE TABLE users (
		id SERIAL PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		created_at TIMESTAMP DEFAULT NOW()
	);
	CREATE INDEX idx_users_email ON users(email);
`

// benchFixtureSQL builds a moderately large fixture: a products table with
// indexes and 1000 rows, simulating regional seed data.
func benchFixtureSQL() string {
	var sql strings.Builder
	sql.WriteString(`
		CREATE TABLE products (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			price DECIMAL(10,2) NOT NULL,
			category TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW()
		);
		CREATE INDEX idx_products_category ON products(category);
		CREATE INDEX idx_products_price ON products(price);
	`)
	for i := range 1000 {
		sql.WriteString("INSERT INTO products (name, price, category) VALUES ('Product ")
		sql.WriteString(strconv.Itoa(i))
		sql.WriteString("', 19.99, 'Category');\n")
	}
	return sql.String()
}

func writeBenchFile(b *testing.B, name, content string) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatalf("write %s: %v", name, err)
	}
	return path
}

// ensureBenchTemplate creates a template for a benchmark and releases it
// when the benchmark ends.
func ensureBenchTemplate(b *testing.B, name string, setup postgres.SetupFunc) *postgres.Template {
	b.Helper()
	ctx := context.Background()
	tpl, err := postgres.EnsureTemplate(ctx, name, setup)
	if err != nil {
		b.Fatalf("EnsureTemplate: %v", err)
	}
	b.Cleanup(func() {
		if err := tpl.Release(ctx); err != nil {
			b.Errorf("Release: %v", err)
		}
	})
	return tpl
}

// BenchmarkFreshDatabase measures the time to create a fresh database from scratch
func BenchmarkFreshDatabase(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		postgres.New(b, &postgres.PoolInitializer{})
		// Cleanup happens automatically via b.Cleanup()
	}
}

// BenchmarkTemplateClone measures the time to clone from an empty template
func BenchmarkTemplateClone(b *testing.B) {
	tpl := ensureBenchTemplate(b, "bench_template_empty", nil)

	b.ReportAllocs()
	for b.Loop() {
		postgres.New(b, &postgres.PoolInitializer{}, tpl.Option())
	}
}

// BenchmarkFreshDatabaseWithSchema measures creating a fresh DB and applying the schema
func BenchmarkFreshDatabaseWithSchema(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		db := postgres.New(b, &postgres.PoolInitializer{})
		pool := db.Entity().(*pgxpool.Pool)
		if _, err := pool.Exec(context.Background(), benchSchemaSQL); err != nil {
			b.Fatalf("Failed to run migration: %v", err)
		}
	}
}

// BenchmarkTemplateCloneWithSchema measures cloning from a template that has the schema
func BenchmarkTemplateCloneWithSchema(b *testing.B) {
	schemaFile := writeBenchFile(b, "schema.sql", benchSchemaSQL)
	tpl := ensureBenchTemplate(b, "bench_template_with_schema", postgres.LoadSQLFile(schemaFile))

	b.ReportAllocs()
	for b.Loop() {
		postgres.New(b, &postgres.PoolInitializer{}, tpl.Option())
		// Schema already present - no migration needed
	}
}

// BenchmarkFreshDatabaseWithLargeFixtures measures loading the fixture into every fresh DB
func BenchmarkFreshDatabaseWithLargeFixtures(b *testing.B) {
	fixtureSQL := benchFixtureSQL()

	b.ReportAllocs()
	for b.Loop() {
		db := postgres.New(b, &postgres.PoolInitializer{})
		pool := db.Entity().(*pgxpool.Pool)
		if _, err := pool.Exec(context.Background(), fixtureSQL); err != nil {
			b.Fatalf("Failed to load fixtures: %v", err)
		}
	}
}

// BenchmarkTemplateCloneWithLargeFixtures measures cloning from a template with the fixture
func BenchmarkTemplateCloneWithLargeFixtures(b *testing.B) {
	fixtureFile := writeBenchFile(b, "fixtures.sql", benchFixtureSQL())
	tpl := ensureBenchTemplate(b, "bench_template_with_fixtures", postgres.LoadSQLFile(fixtureFile))

	b.ReportAllocs()
	for b.Loop() {
		postgres.New(b, &postgres.PoolInitializer{}, tpl.Option())
		// Fixtures already present
	}
}

// BenchmarkTemplateCreation measures the one-time cost of building and dropping a template
func BenchmarkTemplateCreation(b *testing.B) {
	ctx := context.Background()
	schemaFile := writeBenchFile(b, "schema.sql", benchSchemaSQL)
	setup := postgres.LoadSQLFile(schemaFile)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		tpl, err := postgres.EnsureTemplate(ctx, "bench_template_creation_"+strconv.Itoa(i), setup)
		if err != nil {
			b.Fatalf("EnsureTemplate: %v", err)
		}
		if err := tpl.Release(ctx); err != nil {
			b.Fatalf("Release: %v", err)
		}
		i++
	}
}
