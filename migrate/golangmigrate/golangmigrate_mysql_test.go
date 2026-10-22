package golangmigrate_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/migrate/golangmigrate"
	"github.com/bashhack/testdb/mysql"
)

const mysqlMigrationsDir = "../../testdata/mysql/migrations_migrate"

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
	db := mysql.Setup(t, testdb.WithMigrator(golangmigrate.Dir(mysqlMigrationsDir)))

	if !mysqlTableExists(t, db, "test_table") {
		t.Fatal("expected test_table from the golang-migrate migrations")
	}
	if !mysqlTableExists(t, db, "schema_migrations") {
		t.Fatal("expected golang-migrate's schema_migrations table")
	}
}

func TestMySQLMigrateTwiceIsANoOp(t *testing.T) {
	requireMySQL(t)
	db := mysql.New(t, &mysql.SQLDBInitializer{}, testdb.WithMigrator(golangmigrate.Dir(mysqlMigrationsDir)))

	if err := golangmigrate.Dir(mysqlMigrationsDir).Migrate(context.Background(), db.DSN()); err != nil {
		t.Fatalf("second Migrate on an up-to-date database: %v", err)
	}
}

func TestMySQLURLFormIsRejectedWithGuidance(t *testing.T) {
	err := golangmigrate.Dir(mysqlMigrationsDir).Migrate(context.Background(), "mysql://root:root@localhost:3306/x")
	if err == nil || !contains(err.Error(), "native form") {
		t.Fatalf("expected guidance to use the native DSN form, got: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
