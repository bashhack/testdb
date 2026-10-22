package goose_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/migrate/goose"
	"github.com/bashhack/testdb/mysql"
)

const mysqlMigrationsDir = "../../testdata/mysql/migrations_goose"

func requireMySQL(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &mysql.Provider{}
	if err := p.Initialize(ctx, testdb.Config{}); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("MySQL is required in CI but not reachable: %v", err)
		}
		t.Skipf("MySQL not available: %v", err)
	}
	if err := p.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func mysqlTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&n)
	if err != nil {
		t.Fatalf("query TABLES: %v", err)
	}
	return n == 1
}

func TestMySQLDir(t *testing.T) {
	requireMySQL(t)
	// The dialect and driver are inferred from the native MySQL DSN
	db := mysql.Setup(t, testdb.WithMigrator(goose.Dir(mysqlMigrationsDir)))

	if !mysqlTableExists(t, db, "products") {
		t.Fatal("expected the products table from the goose migrations")
	}
	if !mysqlTableExists(t, db, "goose_db_version") {
		t.Fatal("expected goose's version table")
	}
}

func TestMySQLMigrateTwiceIsANoOp(t *testing.T) {
	requireMySQL(t)
	db := mysql.New(t, &mysql.SQLDBInitializer{}, testdb.WithMigrator(goose.Dir(mysqlMigrationsDir)))

	if err := goose.Dir(mysqlMigrationsDir).Migrate(context.Background(), db.DSN()); err != nil {
		t.Fatalf("second Migrate on an up-to-date database: %v", err)
	}
}
