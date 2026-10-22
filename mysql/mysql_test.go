package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/mysql"
)

const schemaDir = "../testdata/mysql/schema"

// requireMySQL skips the test when no MySQL server answers, so a checkout
// with only Postgres running stays green; in CI (where the service must be
// there) it fails instead, so a broken service cannot pass silently.
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

// adminDB opens a plain connection with the admin credentials, for
// assertions about what exists on the server.
func adminDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	p := &mysql.Provider{}
	if err := p.Initialize(ctx, testdb.Config{}); err != nil {
		t.Fatal(err)
	}
	dsn := p.ResolvedAdminDSN()
	if err := p.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func databaseExists(t *testing.T, admin *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := admin.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", name).Scan(&n)
	if err != nil {
		t.Fatalf("query SCHEMATA: %v", err)
	}
	return n == 1
}

func currentDatabase(t *testing.T, db *sql.DB) string {
	t.Helper()
	var name string
	if err := db.QueryRowContext(context.Background(), "SELECT DATABASE()").Scan(&name); err != nil {
		t.Fatalf("SELECT DATABASE(): %v", err)
	}
	return name
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&n)
	if err != nil {
		t.Fatalf("query TABLES: %v", err)
	}
	return n == 1
}

func TestSetupIsolation(t *testing.T) {
	requireMySQL(t)
	first := mysql.Setup(t)
	second := mysql.Setup(t)

	if currentDatabase(t, first) == currentDatabase(t, second) {
		t.Fatal("two Setup calls returned the same database")
	}
	if _, err := first.ExecContext(context.Background(), "CREATE TABLE only_here (id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if tableExists(t, second, "only_here") {
		t.Fatal("a table created in one test database is visible in another")
	}
}

func TestParallel(t *testing.T) {
	requireMySQL(t)
	for i := range 4 {
		t.Run(fmt.Sprintf("test%d", i), func(t *testing.T) {
			t.Parallel()
			db := mysql.Setup(t, testdb.WithMigrator(mysql.SQLFiles(schemaDir)))
			if _, err := db.ExecContext(context.Background(),
				"INSERT INTO users (email, name) VALUES (?, ?)", fmt.Sprintf("u%d@example.com", i), "n"); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
				t.Fatal(err)
			}
			// 2 from the fixture, plus ours; nobody else's
			if n != 3 {
				t.Fatalf("expected 3 users in an isolated database, got %d", n)
			}
		})
	}
}

func TestCleanupDropsDatabase(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)

	db, err := testdb.New(t, &mysql.Provider{}, &mysql.SQLDBInitializer{})
	if err != nil {
		t.Fatal(err)
	}
	name := db.Name()
	if !databaseExists(t, admin, name) {
		t.Fatalf("database %s should exist after New", name)
	}
	if err := db.Entity().(*sql.DB).Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if databaseExists(t, admin, name) {
		t.Fatalf("database %s still exists after Close", name)
	}
}

// TestCleanupWithOpenTransactionDoesNotHang is the MySQL-specific hazard:
// DROP DATABASE waits on metadata locks held by open transactions, for up
// to lock_wait_timeout (a year by default). Cleanup must kill the session
// first and finish promptly.
func TestCleanupWithOpenTransactionDoesNotHang(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()

	db, err := testdb.New(t, &mysql.Provider{}, &mysql.SQLDBInitializer{},
		testdb.WithMigrator(mysql.SQLFiles(schemaDir)))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	pool := db.Entity().(*sql.DB)

	// Another session takes a metadata lock on a table and never commits
	holder, err := sql.Open("mysql", db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() }) // its connection was killed; errors here are expected
	holderConn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := holderConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO users (email, name) VALUES ('locker@example.com', 'locker')"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close with an open transaction in another session: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cleanup took %s; it should not wait on the leaked transaction", elapsed)
	}
	if databaseExists(t, admin, db.Name()) {
		t.Fatalf("database %s still exists", db.Name())
	}
	// The leaked session is gone too
	if err := tx.Commit(); err == nil {
		t.Error("the leaked transaction should have been killed")
	}
}

// spyTB records what a test under observation does to its testing.TB.
type spyTB struct {
	testing.TB
	failed   bool
	cleanups []func()
	logs     []string
}

func (s *spyTB) Failed() bool            { return s.failed }
func (s *spyTB) Helper()                 {}
func (s *spyTB) Cleanup(f func())        { s.cleanups = append(s.cleanups, f) }
func (s *spyTB) Logf(f string, a ...any) { s.logs = append(s.logs, fmt.Sprintf(f, a...)) }
func (s *spyTB) runCleanups() {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
}

func TestKeepOnFailureKeepsDatabaseAndSaysHowToOpenIt(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)

	spy := &spyTB{TB: t}
	db := mysql.Setup(spy, testdb.WithKeepOnFailure(), testdb.WithDBPrefix("keep_mysql"))
	name := currentDatabase(t, db)
	t.Cleanup(func() {
		if databaseExists(t, admin, name) {
			if _, err := admin.ExecContext(context.Background(), "DROP DATABASE `"+name+"`"); err != nil {
				t.Errorf("drop kept database: %v", err)
			}
		}
	})

	spy.failed = true
	spy.runCleanups()

	if !databaseExists(t, admin, name) {
		t.Fatalf("database %s was dropped although the test failed with KeepOnFailure", name)
	}
	var kept string
	for _, msg := range spy.logs {
		if strings.Contains(msg, "kept database") {
			kept = msg
		}
	}
	if !strings.Contains(kept, name) || !strings.Contains(kept, "mysql --host=") {
		t.Errorf("expected a mysql command line naming %s, got: %q", name, kept)
	}
}

func TestWithTemplateIsRefused(t *testing.T) {
	_, err := testdb.New(t, &mysql.Provider{}, nil, testdb.WithTemplate("anything"))
	if !errors.Is(err, testdb.ErrTemplatesNotSupported) {
		t.Fatalf("expected ErrTemplatesNotSupported, got %v", err)
	}
}

func TestSQLFilesAndLoadSQLFile(t *testing.T) {
	requireMySQL(t)

	t.Run("SQLFiles runs every file, multi-statement included", func(t *testing.T) {
		db := mysql.Setup(t, testdb.WithMigrator(mysql.SQLFiles(schemaDir)))
		for _, table := range []string{"test_table", "users", "products"} {
			if !tableExists(t, db, table) {
				t.Errorf("expected %s table", table)
			}
		}
		var n int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("expected the 2 fixture users from the second statement of 002_users.sql, got %d", n)
		}
	})

	t.Run("LoadSQLFile runs just the one", func(t *testing.T) {
		db := mysql.Setup(t, testdb.WithMigrator(mysql.LoadSQLFile(schemaDir+"/003_products.sql")))
		if !tableExists(t, db, "products") || tableExists(t, db, "users") {
			t.Fatal("LoadSQLFile should create products and nothing else")
		}
	})

	t.Run("empty directory is an error", func(t *testing.T) {
		err := mysql.SQLFiles(t.TempDir())(context.Background(), "root@tcp(unused:3306)/x")
		if err == nil || !strings.Contains(err.Error(), "no .sql files") {
			t.Fatalf("expected a 'no .sql files' error, got %v", err)
		}
	})
}

func TestAdminDSNEnvPrecedence(t *testing.T) {
	// An address nothing listens on, so Initialize fails fast after resolving
	t.Setenv(mysql.AdminDSNEnv, "root:root@tcp(127.0.0.1:1)/?timeout=1s")
	t.Setenv("TEST_DATABASE_URL", "root:root@tcp(shared.should.lose:3306)/")

	p := &mysql.Provider{}
	if err := p.Initialize(context.Background(), testdb.Config{}); err == nil {
		t.Cleanup(func() { _ = p.Cleanup(context.Background()) })
		t.Fatal("expected the connection to the unreachable address to fail")
	}
	if !strings.Contains(p.ResolvedAdminDSN(), "127.0.0.1:1") {
		t.Errorf("resolved %q, want %s to win", p.ResolvedAdminDSN(), mysql.AdminDSNEnv)
	}
}
