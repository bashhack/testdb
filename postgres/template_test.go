package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueTemplateName makes a template name no other test can collide with,
// so template tests can run in parallel and never see each other's leftovers.
func uniqueTemplateName(t *testing.T, prefix string) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// adminConn opens a plain connection to the admin database for assertions
// about what exists on the server.
func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	provider := &postgres.PostgresProvider{}
	if err := provider.Initialize(ctx, testdb.Config{}); err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}
	t.Cleanup(func() { _ = provider.Cleanup(ctx) })

	conn, err := pgx.Connect(ctx, provider.ResolvedAdminDSN())
	if err != nil {
		t.Fatalf("connect to admin database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

func databaseExists(t *testing.T, conn *pgx.Conn, name string) bool {
	t.Helper()
	var exists bool
	err := conn.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	if err != nil {
		t.Fatalf("query pg_database: %v", err)
	}
	return exists
}

// databasesLike lists databases whose name matches a LIKE pattern, for
// checking that no build-in-progress databases were left behind.
func databasesLike(t *testing.T, conn *pgx.Conn, pattern string) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		"SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY datname", pattern)
	if err != nil {
		t.Fatalf("query pg_database: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func release(t *testing.T, tpl *postgres.Template) {
	t.Helper()
	if err := tpl.Release(context.Background()); err != nil {
		t.Errorf("Release failed: %v", err)
	}
}

func TestTemplateBasic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := adminConn(t)
	name := uniqueTemplateName(t, "test_template_basic")

	tpl, err := postgres.EnsureTemplate(ctx, name, nil)
	if err != nil {
		t.Fatalf("EnsureTemplate failed: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })

	if tpl.Name() != name {
		t.Errorf("Name() = %q, want %q", tpl.Name(), name)
	}
	exists, err := postgres.TemplateExists(ctx, name)
	if err != nil {
		t.Fatalf("TemplateExists failed: %v", err)
	}
	if !exists {
		t.Fatal("template should exist after EnsureTemplate")
	}

	// It is a real Postgres template, so an accidental DROP DATABASE is refused
	_, err = conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Errorf("expected DROP DATABASE on a template to be refused, got: %v", err)
	}

	pool := postgres.Setup(t, tpl.Option())
	var result int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&result); err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if result != 1 {
		t.Errorf("expected 1, got %d", result)
	}
}

func TestTemplateReleaseDropsWhenLastOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := adminConn(t)
	name := uniqueTemplateName(t, "test_template_lastout")

	setupRuns := 0
	setup := func(ctx context.Context, dsn string) error {
		setupRuns++
		return nil
	}

	// Two handles stand in for two processes
	first, err := postgres.EnsureTemplate(ctx, name, setup)
	if err != nil {
		t.Fatalf("first EnsureTemplate failed: %v", err)
	}
	second, err := postgres.EnsureTemplate(ctx, name, setup)
	if err != nil {
		t.Fatalf("second EnsureTemplate failed: %v", err)
	}
	if setupRuns != 1 {
		t.Errorf("setup ran %d times, want 1", setupRuns)
	}

	release(t, first)
	if !databaseExists(t, conn, name) {
		t.Fatal("template was dropped while another handle still used it")
	}

	release(t, second)
	if databaseExists(t, conn, name) {
		t.Fatal("template was not dropped by the last release")
	}

	// Releasing again is harmless
	release(t, second)
}

func TestTemplateWithSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	name := uniqueTemplateName(t, "test_template_with_sql")

	tpl, err := postgres.EnsureTemplate(ctx, name,
		postgres.LoadSQLFile("../testdata/postgres/fixtures/seed_data.sql"))
	if err != nil {
		t.Fatalf("EnsureTemplate failed: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })

	pool := postgres.Setup(t, testdb.WithTemplate(name))

	var userEmail string
	err = pool.QueryRow(ctx,
		"SELECT email FROM users WHERE email = $1", "admin@example.com").Scan(&userEmail)
	if err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if userEmail != "admin@example.com" {
		t.Errorf("expected 'admin@example.com', got %q", userEmail)
	}

	var productName string
	var price float64
	err = pool.QueryRow(ctx,
		"SELECT name, price FROM products WHERE name = $1", "Widget").Scan(&productName, &price)
	if err != nil {
		t.Fatalf("failed to query products: %v", err)
	}
	if productName != "Widget" || price != 9.99 {
		t.Errorf("expected ('Widget', 9.99), got (%q, %f)", productName, price)
	}

	// Writes go to the clone, never the template
	_, err = pool.Exec(ctx,
		"INSERT INTO users (email, name) VALUES ($1, $2)", "test@example.com", "Test User")
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}
}

func TestTemplateIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	name := uniqueTemplateName(t, "test_template_isolation")

	tpl, err := postgres.EnsureTemplate(ctx, name,
		postgres.LoadSQLFile("../testdata/postgres/fixtures/isolation_test.sql"))
	if err != nil {
		t.Fatalf("EnsureTemplate failed: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })

	pool1 := postgres.Setup(t, testdb.WithTemplate(name))
	pool2 := postgres.Setup(t, testdb.WithTemplate(name))

	if _, err := pool1.Exec(ctx, "UPDATE counters SET value = 200 WHERE id = 1"); err != nil {
		t.Fatalf("update in db1 failed: %v", err)
	}

	var value1, value2 int
	if err := pool1.QueryRow(ctx, "SELECT value FROM counters WHERE id = 1").Scan(&value1); err != nil {
		t.Fatalf("query db1 failed: %v", err)
	}
	if value1 != 200 {
		t.Errorf("db1: expected 200, got %d", value1)
	}
	if err := pool2.QueryRow(ctx, "SELECT value FROM counters WHERE id = 1").Scan(&value2); err != nil {
		t.Fatalf("query db2 failed: %v", err)
	}
	if value2 != 100 {
		t.Errorf("db2: expected 100 (isolation), got %d", value2)
	}
}

func TestTemplateDoesNotExist(t *testing.T) {
	t.Parallel()
	// Use testdb.New directly to capture the error instead of t.Fatal
	provider := &postgres.PostgresProvider{}
	initializer := &postgres.PoolInitializer{}

	_, err := testdb.New(t, provider, initializer,
		testdb.WithTemplate("nonexistent_template"))
	if err == nil {
		t.Fatal("expected error when using non-existent template, got nil")
	}

	expectedMsg := `template database "nonexistent_template" does not exist (create it with postgres.EnsureTemplate)`
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error to contain %q, got: %v", expectedMsg, err)
	}
}

func TestTemplateCustomSetup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	name := uniqueTemplateName(t, "test_template_custom")

	setup := func(ctx context.Context, dsn string) error {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer pool.Close()

		_, err = pool.Exec(ctx, `
			CREATE TABLE products (
				id SERIAL PRIMARY KEY,
				name TEXT NOT NULL,
				price DECIMAL(10,2) NOT NULL
			)
		`)
		if err != nil {
			return err
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO products (name, price) VALUES
				('Widget', 9.99),
				('Gadget', 19.99)
		`)
		return err
	}

	tpl, err := postgres.EnsureTemplate(ctx, name, setup)
	if err != nil {
		t.Fatalf("EnsureTemplate failed: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })

	pool := postgres.Setup(t, testdb.WithTemplate(name))
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM products").Scan(&count); err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 products, got %d", count)
	}
}

func TestTemplateSetupFailureLeavesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := adminConn(t)
	name := uniqueTemplateName(t, "test_template_setupfail")

	setupErr := errors.New("fixture load exploded")
	_, err := postgres.EnsureTemplate(ctx, name, func(ctx context.Context, dsn string) error {
		return setupErr
	})
	if !errors.Is(err, setupErr) {
		t.Fatalf("expected the setup error to be returned, got: %v", err)
	}

	if databaseExists(t, conn, name) {
		t.Errorf("template %q exists after a failed setup", name)
	}
	if leftovers := databasesLike(t, conn, name+"%"); len(leftovers) > 0 {
		t.Errorf("build databases left behind: %v", leftovers)
	}

	// The name is free to use again
	tpl, err := postgres.EnsureTemplate(ctx, name, nil)
	if err != nil {
		t.Fatalf("EnsureTemplate after failure: %v", err)
	}
	release(t, tpl)
}

func TestTemplateFingerprint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := adminConn(t)
	name := uniqueTemplateName(t, "test_template_fp")

	setupRuns := 0
	setup := func(ctx context.Context, dsn string) error {
		setupRuns++
		return nil
	}

	// Kept templates survive their last release...
	tpl, err := postgres.EnsureTemplate(ctx, name, setup,
		postgres.WithFingerprint("v1"), postgres.WithKeepTemplate())
	if err != nil {
		t.Fatalf("EnsureTemplate v1: %v", err)
	}
	release(t, tpl)
	if !databaseExists(t, conn, name) {
		t.Fatal("kept template was dropped")
	}

	// ...and are reused when the fingerprint matches...
	tpl, err = postgres.EnsureTemplate(ctx, name, setup,
		postgres.WithFingerprint("v1"), postgres.WithKeepTemplate())
	if err != nil {
		t.Fatalf("EnsureTemplate v1 again: %v", err)
	}
	release(t, tpl)
	if setupRuns != 1 {
		t.Fatalf("setup ran %d times after a matching fingerprint, want 1", setupRuns)
	}

	// ...or when no fingerprint is given...
	tpl, err = postgres.EnsureTemplate(ctx, name, setup, postgres.WithKeepTemplate())
	if err != nil {
		t.Fatalf("EnsureTemplate without fingerprint: %v", err)
	}
	release(t, tpl)
	if setupRuns != 1 {
		t.Fatalf("setup ran %d times without a fingerprint, want 1", setupRuns)
	}

	// ...but rebuilt when it changes
	tpl, err = postgres.EnsureTemplate(ctx, name, setup, postgres.WithFingerprint("v2"))
	if err != nil {
		t.Fatalf("EnsureTemplate v2: %v", err)
	}
	if setupRuns != 2 {
		t.Fatalf("setup ran %d times after a changed fingerprint, want 2", setupRuns)
	}
	release(t, tpl)
	if databaseExists(t, conn, name) {
		t.Fatal("template without WithKeepTemplate was not dropped")
	}
}

func TestTemplateStaleWhileInUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	name := uniqueTemplateName(t, "test_template_stale")

	holder, err := postgres.EnsureTemplate(ctx, name, nil, postgres.WithFingerprint("v1"))
	if err != nil {
		t.Fatalf("EnsureTemplate v1: %v", err)
	}
	t.Cleanup(func() { release(t, holder) })

	_, err = postgres.EnsureTemplate(ctx, name, nil, postgres.WithFingerprint("v2"))
	if err == nil {
		t.Fatal("expected an error ensuring a changed template that is in use")
	}
	if !strings.Contains(err.Error(), "in use by another process") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTemplateNameIsNotATemplate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := adminConn(t)
	name := uniqueTemplateName(t, "test_template_plaindb")

	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create plain database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Errorf("drop plain database: %v", err)
		}
	})

	_, err := postgres.EnsureTemplate(ctx, name, nil)
	if err == nil || !strings.Contains(err.Error(), "is not a template") {
		t.Fatalf("expected 'is not a template' error, got: %v", err)
	}
	if !databaseExists(t, conn, name) {
		t.Fatal("EnsureTemplate touched a database that was not a template")
	}
}

func TestTemplateNameValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	if _, err := postgres.EnsureTemplate(ctx, "", nil); err == nil {
		t.Error("expected an error for an empty name")
	}
	long := strings.Repeat("x", 49)
	_, err := postgres.EnsureTemplate(ctx, long, nil)
	if err == nil || !strings.Contains(err.Error(), "maximum is 48") {
		t.Errorf("expected a length error for a 49-byte name, got: %v", err)
	}
}

func TestTemplateContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := postgres.EnsureTemplate(ctx, uniqueTemplateName(t, "test_template_ctx"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestHashFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.sql")
	b := filepath.Join(dir, "sub", "b.sql")
	if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("CREATE TABLE a (id int);"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("CREATE TABLE b (id int);"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := postgres.HashFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := postgres.HashFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Error("hash is not stable across calls")
	}

	single, err := postgres.HashFiles(a)
	if err != nil {
		t.Fatal(err)
	}
	if single == first {
		t.Error("hash of one file equals hash of the directory")
	}

	if err := os.WriteFile(b, []byte("CREATE TABLE b (id bigint);"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := postgres.HashFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Error("hash did not change when a file's content changed")
	}

	if _, err := postgres.HashFiles(filepath.Join(dir, "missing.sql")); err == nil {
		t.Error("expected an error for a missing path")
	}
}

func TestTemplateWithLoadMigrations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := map[string]struct {
		tool          testdb.MigrationTool
		migrationDir  string
		expectedTable string
	}{
		"Goose": {
			tool:          testdb.MigrationToolGoose,
			migrationDir:  "../testdata/postgres/migrations_goose",
			expectedTable: "products",
		},
		"Tern": {
			tool:          testdb.MigrationToolTern,
			migrationDir:  "../testdata/postgres/migrations_tern",
			expectedTable: "users",
		},
		"Migrate": {
			tool:          testdb.MigrationToolMigrate,
			migrationDir:  "../testdata/postgres/migrations_migrate",
			expectedTable: "test_table",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			templateName := uniqueTemplateName(t, "test_template_"+strings.ToLower(name))

			tpl, err := postgres.EnsureTemplate(ctx, templateName,
				postgres.LoadMigrations(tc.migrationDir, tc.tool))
			if err != nil {
				t.Fatalf("EnsureTemplate with LoadMigrations failed: %v", err)
			}
			t.Cleanup(func() { release(t, tpl) })

			pool := postgres.Setup(t, testdb.WithTemplate(templateName))

			var tableExists bool
			err = pool.QueryRow(ctx, `
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

			switch tc.tool {
			case testdb.MigrationToolGoose:
				var productID int
				err = pool.QueryRow(ctx,
					"INSERT INTO products (name, price) VALUES ($1, $2) RETURNING id",
					"Test Product", 29.99).Scan(&productID)
				if err != nil {
					t.Fatalf("failed to insert into products: %v", err)
				}
				var name string
				var price float64
				err = pool.QueryRow(ctx,
					"SELECT name, price FROM products WHERE id = $1", productID).Scan(&name, &price)
				if err != nil {
					t.Fatalf("failed to query products: %v", err)
				}
				if name != "Test Product" || price != 29.99 {
					t.Errorf("expected ('Test Product', 29.99), got (%q, %f)", name, price)
				}

			case testdb.MigrationToolTern:
				var userID int
				err = pool.QueryRow(ctx,
					"INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id",
					"test@example.com", "Test User").Scan(&userID)
				if err != nil {
					t.Fatalf("failed to insert into users: %v", err)
				}
				var email, name string
				err = pool.QueryRow(ctx,
					"SELECT email, name FROM users WHERE id = $1", userID).Scan(&email, &name)
				if err != nil {
					t.Fatalf("failed to query users: %v", err)
				}
				if email != "test@example.com" || name != "Test User" {
					t.Errorf("expected ('test@example.com', 'Test User'), got (%q, %q)", email, name)
				}

			case testdb.MigrationToolMigrate:
				var testID int
				err = pool.QueryRow(ctx,
					"INSERT INTO test_table (name) VALUES ($1) RETURNING id",
					"Test Name").Scan(&testID)
				if err != nil {
					t.Fatalf("failed to insert into test_table: %v", err)
				}
				var name string
				err = pool.QueryRow(ctx,
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
	t.Parallel()
	ctx := context.Background()
	name := uniqueTemplateName(t, "test_template_compose")

	tpl, err := postgres.EnsureTemplate(ctx, name,
		postgres.Compose(
			postgres.LoadMigrations("../testdata/postgres/migrations_goose", testdb.MigrationToolGoose),
			nil, // skipped
			postgres.LoadSQLFile("../testdata/postgres/fixtures/additional_data.sql"),
		))
	if err != nil {
		t.Fatalf("EnsureTemplate with Compose failed: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })

	pool := postgres.Setup(t, testdb.WithTemplate(name))

	for _, table := range []string{"products", "categories"} {
		var exists bool
		err = pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_name = $1
			)
		`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check %s table: %v", table, err)
		}
		if !exists {
			t.Fatalf("expected %s table to exist", table)
		}
	}

	var productName string
	err = pool.QueryRow(ctx,
		"SELECT name FROM products WHERE name = $1", "Composed Widget").Scan(&productName)
	if err != nil {
		t.Fatalf("failed to query product seed data: %v", err)
	}
	if productName != "Composed Widget" {
		t.Errorf("expected 'Composed Widget', got %q", productName)
	}

	var categoryName string
	err = pool.QueryRow(ctx,
		"SELECT name FROM categories WHERE name = $1", "Electronics").Scan(&categoryName)
	if err != nil {
		t.Fatalf("failed to query category seed data: %v", err)
	}
	if categoryName != "Electronics" {
		t.Errorf("expected 'Electronics', got %q", categoryName)
	}
}
