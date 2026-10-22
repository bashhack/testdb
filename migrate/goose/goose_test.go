package goose_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/migrate/goose"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationsDir = "../../testdata/postgres/migrations_goose"

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
	pool := postgres.Setup(t, testdb.WithMigrator(goose.Dir(migrationsDir)))

	if !tableExists(t, pool, "products") {
		t.Fatal("expected the products table from the goose migrations")
	}
	// goose records what it applied
	if !tableExists(t, pool, "goose_db_version") {
		t.Fatal("expected goose's version table")
	}
}

func TestFS(t *testing.T) {
	pool := postgres.Setup(t, testdb.WithMigrator(
		goose.FS(os.DirFS("../../testdata/postgres"), "migrations_goose")))

	if !tableExists(t, pool, "products") {
		t.Fatal("expected the products table from the goose migrations")
	}
}

func TestMigrateTwiceIsANoOp(t *testing.T) {
	db := postgres.New(t, &postgres.PoolInitializer{}, testdb.WithMigrator(goose.Dir(migrationsDir)))

	if err := goose.Dir(migrationsDir).Migrate(context.Background(), db.DSN()); err != nil {
		t.Fatalf("second Migrate on an up-to-date database: %v", err)
	}
}

func TestMissingDirectory(t *testing.T) {
	err := goose.Dir("/nonexistent/migrations").Migrate(context.Background(), "postgres://unused")
	if err == nil || !strings.Contains(err.Error(), "migrations directory") {
		t.Fatalf("expected a migrations directory error, got: %v", err)
	}
}

func TestDialectFromDSN(t *testing.T) {
	err := goose.Dir(migrationsDir).Migrate(context.Background(), "host=localhost dbname=x")
	if err == nil || !strings.Contains(err.Error(), "WithDialect") {
		t.Fatalf("expected a hint to use WithDialect for a scheme-less DSN, got: %v", err)
	}
}

func TestTemplate(t *testing.T) {
	ctx := context.Background()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "goose_tpl_" + hex.EncodeToString(b[:])

	// The adapter's Migrate method is a template setup function as it stands
	tpl, err := postgres.EnsureTemplate(ctx, name, goose.Dir(migrationsDir).Migrate)
	if err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	t.Cleanup(func() {
		if err := tpl.Release(ctx); err != nil {
			t.Errorf("Release: %v", err)
		}
	})

	pool := postgres.Setup(t, tpl.Option())
	if !tableExists(t, pool, "products") {
		t.Fatal("expected the products table in a clone of the template")
	}
}
