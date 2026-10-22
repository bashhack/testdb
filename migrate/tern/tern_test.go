package tern_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/migrate/tern"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationsDir = "../../testdata/postgres/migrations_tern"

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
	pool := postgres.Setup(t, testdb.WithMigrator(tern.Dir(migrationsDir)))

	if !tableExists(t, pool, "users") {
		t.Fatal("expected the users table from the tern migrations")
	}
	if !tableExists(t, pool, tern.DefaultVersionTable) {
		t.Fatalf("expected tern's %s table", tern.DefaultVersionTable)
	}
}

func TestFS(t *testing.T) {
	pool := postgres.Setup(t, testdb.WithMigrator(
		tern.FS(os.DirFS("../../testdata/postgres"), "migrations_tern")))

	if !tableExists(t, pool, "users") {
		t.Fatal("expected the users table from the tern migrations")
	}
}

func TestVersionTable(t *testing.T) {
	pool := postgres.Setup(t, testdb.WithMigrator(
		tern.Dir(migrationsDir, tern.WithVersionTable("my_versions"))))

	if !tableExists(t, pool, "my_versions") {
		t.Fatal("expected the custom version table")
	}
	if tableExists(t, pool, tern.DefaultVersionTable) {
		t.Fatal("default version table should not exist when a custom one is set")
	}
}

func TestMigrateTwiceIsANoOp(t *testing.T) {
	db := postgres.New(t, &postgres.PoolInitializer{}, testdb.WithMigrator(tern.Dir(migrationsDir)))

	if err := tern.Dir(migrationsDir).Migrate(context.Background(), db.DSN()); err != nil {
		t.Fatalf("second Migrate on an up-to-date database: %v", err)
	}
}

func TestMissingDirectory(t *testing.T) {
	err := tern.Dir("/nonexistent/migrations").Migrate(context.Background(), "postgres://unused")
	if err == nil || !strings.Contains(err.Error(), "migrations directory") {
		t.Fatalf("expected a migrations directory error, got: %v", err)
	}
}

func TestTemplate(t *testing.T) {
	ctx := context.Background()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "tern_tpl_" + hex.EncodeToString(b[:])

	tpl, err := postgres.EnsureTemplate(ctx, name, tern.Dir(migrationsDir).Migrate)
	if err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	t.Cleanup(func() {
		if err := tpl.Release(ctx); err != nil {
			t.Errorf("Release: %v", err)
		}
	})

	pool := postgres.Setup(t, tpl.Option())
	if !tableExists(t, pool, "users") {
		t.Fatal("expected the users table in a clone of the template")
	}
}
