package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTemplateBasic(t *testing.T) {
	ctx := context.Background()
	templateName := "test_template_basic"

	// Create a simple template
	err := postgres.CreateTemplate(ctx, templateName, nil)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	// Cleanup
	defer func() {
		if err := postgres.DropTemplate(ctx, templateName); err != nil {
			t.Errorf("DropTemplate failed: %v", err)
		}
	}()

	// Verify template exists
	exists, err := postgres.TemplateExists(ctx, templateName)
	if err != nil {
		t.Fatalf("TemplateExists failed: %v", err)
	}
	if !exists {
		t.Fatal("Template should exist after creation")
	}

	// Use the template to create a test database
	db := postgres.New(t, &postgres.PoolInitializer{},
		testdb.WithTemplate(templateName))

	pool := db.Entity().(*pgxpool.Pool)

	// Verify we can query the cloned database
	var result int
	err = pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if result != 1 {
		t.Errorf("Expected 1, got %d", result)
	}
}

func TestTemplateWithSQL(t *testing.T) {
	ctx := context.Background()
	templateName := "test_template_with_sql"

	// Create template with SQL fixture file
	err := postgres.CreateTemplate(ctx, templateName,
		postgres.LoadSQLFile("../testdata/postgres/fixtures/seed_data.sql"))
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	defer func() {
		if err := postgres.DropTemplate(ctx, templateName); err != nil {
			t.Errorf("DropTemplate failed: %v", err)
		}
	}()

	// Use the template
	db := postgres.New(t, &postgres.PoolInitializer{},
		testdb.WithTemplate(templateName))

	pool := db.Entity().(*pgxpool.Pool)

	// Verify tables and seed data from SQL file exist
	var userEmail string
	err = pool.QueryRow(context.Background(),
		"SELECT email FROM users WHERE email = $1",
		"admin@example.com").Scan(&userEmail)
	if err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if userEmail != "admin@example.com" {
		t.Errorf("expected 'admin@example.com', got %q", userEmail)
	}

	// Verify products table and data
	var productName string
	var price float64
	err = pool.QueryRow(context.Background(),
		"SELECT name, price FROM products WHERE name = $1",
		"Widget").Scan(&productName, &price)
	if err != nil {
		t.Fatalf("failed to query products: %v", err)
	}
	if productName != "Widget" || price != 9.99 {
		t.Errorf("expected ('Widget', 9.99), got (%q, %f)", productName, price)
	}

	// Insert new data (should not affect template)
	_, err = pool.Exec(context.Background(),
		"INSERT INTO users (email, name) VALUES ($1, $2)",
		"test@example.com", "Test User")
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}
}

func TestTemplateIsolation(t *testing.T) {
	ctx := context.Background()
	templateName := "test_template_isolation"

	// Create template with SQL fixture file
	err := postgres.CreateTemplate(ctx, templateName,
		postgres.LoadSQLFile("../testdata/postgres/fixtures/isolation_test.sql"))
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	defer func() {
		if err := postgres.DropTemplate(ctx, templateName); err != nil {
			t.Errorf("DropTemplate failed: %v", err)
		}
	}()

	// Create first database from template
	db1 := postgres.New(t, &postgres.PoolInitializer{},
		testdb.WithTemplate(templateName))
	pool1 := db1.Entity().(*pgxpool.Pool)

	// Create second database from template
	db2 := postgres.New(t, &postgres.PoolInitializer{},
		testdb.WithTemplate(templateName))
	pool2 := db2.Entity().(*pgxpool.Pool)

	// Modify first database
	_, err = pool1.Exec(context.Background(),
		"UPDATE counters SET value = 200 WHERE id = 1")
	if err != nil {
		t.Fatalf("Update in db1 failed: %v", err)
	}

	// Verify first database sees change
	var value1 int
	err = pool1.QueryRow(context.Background(),
		"SELECT value FROM counters WHERE id = 1").Scan(&value1)
	if err != nil {
		t.Fatalf("Query db1 failed: %v", err)
	}
	if value1 != 200 {
		t.Errorf("db1: expected 200, got %d", value1)
	}

	// Verify second database still has original value (isolation)
	var value2 int
	err = pool2.QueryRow(context.Background(),
		"SELECT value FROM counters WHERE id = 1").Scan(&value2)
	if err != nil {
		t.Fatalf("Query db2 failed: %v", err)
	}
	if value2 != 100 {
		t.Errorf("db2: expected 100 (isolation), got %d", value2)
	}
}

func TestTemplateDoesNotExist(t *testing.T) {
	// Use testdb.New directly to capture the error instead of t.Fatal
	provider := &postgres.PostgresProvider{}
	initializer := &postgres.PoolInitializer{}

	_, err := testdb.New(t, provider, initializer,
		testdb.WithTemplate("nonexistent_template"))

	// Should fail with specific error message
	if err == nil {
		t.Fatal("Expected error when using non-existent template, got nil")
	}

	// Check exact error message
	expectedMsg := `template database "nonexistent_template" does not exist (create it with postgres.CreateTemplate)`
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("Expected error to contain %q, got: %v", expectedMsg, err)
	}
}

func TestTemplateCustomSetup(t *testing.T) {
	ctx := context.Background()
	templateName := "test_template_custom"

	// Custom setup function
	setupFunc := func(dsn string) error {
		// Connect and do custom initialization
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return err
		}
		defer pool.Close()

		// Create table
		_, err = pool.Exec(context.Background(), `
			CREATE TABLE products (
				id SERIAL PRIMARY KEY,
				name TEXT NOT NULL,
				price DECIMAL(10,2) NOT NULL
			)
		`)
		if err != nil {
			return err
		}

		// Insert seed data
		_, err = pool.Exec(context.Background(), `
			INSERT INTO products (name, price) VALUES
				('Widget', 9.99),
				('Gadget', 19.99)
		`)
		return err
	}

	// Create template with custom setup
	err := postgres.CreateTemplate(ctx, templateName, setupFunc)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	defer func() {
		if err := postgres.DropTemplate(ctx, templateName); err != nil {
			t.Errorf("DropTemplate failed: %v", err)
		}
	}()

	// Use the template
	db := postgres.New(t, &postgres.PoolInitializer{},
		testdb.WithTemplate(templateName))

	pool := db.Entity().(*pgxpool.Pool)

	// Verify custom setup worked
	var count int
	err = pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM products").Scan(&count)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 products, got %d", count)
	}
}

func TestTemplateWithLoadMigrations(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		tool          testdb.MigrationTool
		migrationDir  string
		templateName  string
		expectedTable string
	}{
		"Goose": {
			tool:          testdb.MigrationToolGoose,
			migrationDir:  "../testdata/postgres/migrations_goose",
			templateName:  "test_template_goose",
			expectedTable: "products",
		},
		"Tern": {
			tool:          testdb.MigrationToolTern,
			migrationDir:  "../testdata/postgres/migrations_tern",
			templateName:  "test_template_tern",
			expectedTable: "users",
		},
		"Migrate": {
			tool:          testdb.MigrationToolMigrate,
			migrationDir:  "../testdata/postgres/migrations_migrate",
			templateName:  "test_template_migrate",
			expectedTable: "test_table",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// Create template with migrations
			err := postgres.CreateTemplate(ctx, tc.templateName,
				postgres.LoadMigrations(tc.migrationDir, tc.tool))
			if err != nil {
				t.Fatalf("CreateTemplate with LoadMigrations failed: %v", err)
			}

			defer func() {
				if err := postgres.DropTemplate(ctx, tc.templateName); err != nil {
					t.Errorf("DropTemplate failed: %v", err)
				}
			}()

			// Verify template exists
			exists, err := postgres.TemplateExists(ctx, tc.templateName)
			if err != nil {
				t.Fatalf("TemplateExists failed: %v", err)
			}
			if !exists {
				t.Fatal("Template should exist after creation")
			}

			// Use template to create a test database
			db := postgres.New(t, &postgres.PoolInitializer{},
				testdb.WithTemplate(tc.templateName))

			pool := db.Entity().(*pgxpool.Pool)

			// Verify table from migrations exists
			var tableExists bool
			err = pool.QueryRow(context.Background(), `
				SELECT EXISTS (
					SELECT FROM information_schema.tables
					WHERE table_name = $1
				)
			`, tc.expectedTable).Scan(&tableExists)

			if err != nil {
				t.Fatalf("failed to check table existence: %v", err)
			}
			if !tableExists {
				t.Fatalf("expected %s table from migrations to exist", tc.expectedTable)
			}

			// Verify we can insert and query data (schema-specific operations)
			switch tc.tool {
			case testdb.MigrationToolGoose:
				// products (name, price)
				var productID int
				err = pool.QueryRow(context.Background(),
					"INSERT INTO products (name, price) VALUES ($1, $2) RETURNING id",
					"Test Product", 29.99).Scan(&productID)
				if err != nil {
					t.Fatalf("failed to insert into products: %v", err)
				}

				var name string
				var price float64
				err = pool.QueryRow(context.Background(),
					"SELECT name, price FROM products WHERE id = $1", productID).Scan(&name, &price)
				if err != nil {
					t.Fatalf("failed to query products: %v", err)
				}
				if name != "Test Product" || price != 29.99 {
					t.Errorf("expected ('Test Product', 29.99), got (%q, %f)", name, price)
				}

			case testdb.MigrationToolTern:
				// users (email, name, created_at)
				var userID int
				err = pool.QueryRow(context.Background(),
					"INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id",
					"test@example.com", "Test User").Scan(&userID)
				if err != nil {
					t.Fatalf("failed to insert into users: %v", err)
				}

				var email, name string
				err = pool.QueryRow(context.Background(),
					"SELECT email, name FROM users WHERE id = $1", userID).Scan(&email, &name)
				if err != nil {
					t.Fatalf("failed to query users: %v", err)
				}
				if email != "test@example.com" || name != "Test User" {
					t.Errorf("expected ('test@example.com', 'Test User'), got (%q, %q)", email, name)
				}

			case testdb.MigrationToolMigrate:
				// test_table (id, name)
				var testID int
				err = pool.QueryRow(context.Background(),
					"INSERT INTO test_table (name) VALUES ($1) RETURNING id",
					"Test Name").Scan(&testID)
				if err != nil {
					t.Fatalf("failed to insert into test_table: %v", err)
				}

				var name string
				err = pool.QueryRow(context.Background(),
					"SELECT name FROM test_table WHERE id = $1", testID).Scan(&name)
				if err != nil {
					t.Fatalf("failed to query test_table: %v", err)
				}
				if name != "Test Name" {
					t.Errorf("expected 'Test Name', got %q", name)
				}
			}
		})
	}
}

func TestTemplateCompose(t *testing.T) {
	ctx := context.Background()
	templateName := "test_template_compose"

	// Create template with composed setup (migrations + additional fixtures)
	err := postgres.CreateTemplate(ctx, templateName,
		postgres.Compose(
			postgres.LoadMigrations("../testdata/postgres/migrations_goose", testdb.MigrationToolGoose),
			postgres.LoadSQLFile("../testdata/postgres/fixtures/additional_data.sql"),
		))
	if err != nil {
		t.Fatalf("CreateTemplate with Compose failed: %v", err)
	}

	defer func() {
		if err := postgres.DropTemplate(ctx, templateName); err != nil {
			t.Errorf("DropTemplate failed: %v", err)
		}
	}()

	// Use the template
	db := postgres.New(t, &postgres.PoolInitializer{},
		testdb.WithTemplate(templateName))

	pool := db.Entity().(*pgxpool.Pool)

	// Verify migrations ran (products table from migrations)
	var productsExists bool
	err = pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'products'
		)
	`).Scan(&productsExists)

	if err != nil {
		t.Fatalf("failed to check products table: %v", err)
	}
	if !productsExists {
		t.Fatal("expected products table from migrations to exist")
	}

	// Verify additional fixtures loaded (categories table)
	var categoriesExists bool
	err = pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'categories'
		)
	`).Scan(&categoriesExists)

	if err != nil {
		t.Fatalf("failed to check categories table: %v", err)
	}
	if !categoriesExists {
		t.Fatal("expected categories table from fixtures to exist")
	}

	// Verify seed data from both migrations and fixtures
	var productName string
	err = pool.QueryRow(context.Background(),
		"SELECT name FROM products WHERE name = $1",
		"Composed Widget").Scan(&productName)
	if err != nil {
		t.Fatalf("failed to query product seed data: %v", err)
	}
	if productName != "Composed Widget" {
		t.Errorf("expected 'Composed Widget', got %q", productName)
	}

	var categoryName string
	err = pool.QueryRow(context.Background(),
		"SELECT name FROM categories WHERE name = $1",
		"Electronics").Scan(&categoryName)
	if err != nil {
		t.Fatalf("failed to query category seed data: %v", err)
	}
	if categoryName != "Electronics" {
		t.Errorf("expected 'Electronics', got %q", categoryName)
	}
}
