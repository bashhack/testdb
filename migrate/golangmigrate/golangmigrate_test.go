package golangmigrate_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/migrate/golangmigrate"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationsDir = "../../testdata/postgres/migrations_migrate"

func tableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)", table).Scan(&exists)
	if err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

func TestDir(t *testing.T) {
	pool := postgres.Setup(t, testdb.WithMigrator(golangmigrate.Dir(migrationsDir)))

	if !tableExists(t, pool, "test_table") {
		t.Fatal("expected test_table from the golang-migrate migrations")
	}
	if !tableExists(t, pool, "schema_migrations") {
		t.Fatal("expected golang-migrate's schema_migrations table")
	}
}

func TestFS(t *testing.T) {
	pool := postgres.Setup(t, testdb.WithMigrator(
		golangmigrate.FS(os.DirFS("../../testdata/postgres"), "migrations_migrate")))

	if !tableExists(t, pool, "test_table") {
		t.Fatal("expected test_table from the golang-migrate migrations")
	}
}

func TestMigrateTwiceIsANoOp(t *testing.T) {
	db := postgres.New(t, &postgres.PoolInitializer{}, testdb.WithMigrator(golangmigrate.Dir(migrationsDir)))

	if err := golangmigrate.Dir(migrationsDir).Migrate(context.Background(), db.DSN()); err != nil {
		t.Fatalf("second Migrate on an up-to-date database: %v", err)
	}
}

func TestMissingDirectory(t *testing.T) {
	err := golangmigrate.Dir("/nonexistent/migrations").Migrate(context.Background(), "postgres://unused")
	if err == nil || !strings.Contains(err.Error(), "migrations directory") {
		t.Fatalf("expected a migrations directory error, got: %v", err)
	}
}

func TestNonURLDSN(t *testing.T) {
	err := golangmigrate.Dir(migrationsDir).Migrate(context.Background(), "host=localhost dbname=x")
	if err == nil || !strings.Contains(err.Error(), "must be a PostgreSQL URL") {
		t.Fatalf("expected a DSN form error, got: %v", err)
	}
}

func TestTemplate(t *testing.T) {
	ctx := context.Background()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "gm_tpl_" + hex.EncodeToString(b[:])

	tpl, err := postgres.EnsureTemplate(ctx, name, golangmigrate.Dir(migrationsDir).Migrate)
	if err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	t.Cleanup(func() {
		if err := tpl.Release(ctx); err != nil {
			t.Errorf("Release: %v", err)
		}
	})

	pool := postgres.Setup(t, tpl.Option())
	if !tableExists(t, pool, "test_table") {
		t.Fatal("expected test_table in a clone of the template")
	}
}
