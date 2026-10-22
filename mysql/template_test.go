package mysql_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bashhack/testdb/mysql"
)

func uniqueTemplateName(t *testing.T, prefix string) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func release(t *testing.T, tpl *mysql.Template) {
	t.Helper()
	if err := tpl.Release(context.Background()); err != nil {
		t.Errorf("Release: %v", err)
	}
}

// readyCount is how many prepared databases a template has recorded.
func readyCount(t *testing.T, admin *sql.DB, template string) int {
	t.Helper()
	var n int
	err := admin.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM `"+template+"`.`_testdb_ready`").Scan(&n)
	if err != nil {
		t.Fatalf("count prepared databases: %v", err)
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTemplateBasic(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_basic")

	tpl, err := mysql.EnsureTemplate(ctx, name, mysql.SQLFiles(schemaDir), mysql.WithReadyDatabases(0))
	if err != nil {
		t.Fatalf("EnsureTemplate: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })

	if tpl.Name() != name || !databaseExists(t, admin, name) {
		t.Fatal("template should exist under its name")
	}

	db := mysql.Setup(t, tpl.Option())
	for _, table := range []string{"test_table", "users", "products"} {
		if !tableExists(t, db, table) {
			t.Errorf("clone lacks %s", table)
		}
	}
	if tableExists(t, db, "_testdb_info") || tableExists(t, db, "_testdb_users") {
		t.Error("bookkeeping tables leaked into the clone")
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("fixture rows = %d, want 2", n)
	}
	if currentDatabase(t, db) == name {
		t.Fatal("test got the template itself, not a clone")
	}
}

func TestTemplateReleaseDropsWhenLastOut(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_lastout")

	setupRuns := 0
	setup := func(ctx context.Context, dsn string) error { setupRuns++; return nil }

	first, err := mysql.EnsureTemplate(ctx, name, setup, mysql.WithReadyDatabases(0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := mysql.EnsureTemplate(ctx, name, setup, mysql.WithReadyDatabases(0))
	if err != nil {
		t.Fatal(err)
	}
	if setupRuns != 1 {
		t.Errorf("setup ran %d times, want 1", setupRuns)
	}

	release(t, first)
	if !databaseExists(t, admin, name) {
		t.Fatal("template dropped while another handle used it")
	}
	release(t, second)
	if databaseExists(t, admin, name) {
		t.Fatal("template not dropped by the last release")
	}
	release(t, second) // harmless
}

func TestTemplateFingerprintAndKeep(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_fp")
	t.Cleanup(func() {
		if databaseExists(t, admin, name) {
			if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error(err)
			}
		}
	})

	setupRuns := 0
	setup := func(ctx context.Context, dsn string) error { setupRuns++; return nil }
	ensure := func(opts ...mysql.TemplateOption) *mysql.Template {
		t.Helper()
		tpl, err := mysql.EnsureTemplate(ctx, name, setup, append(opts, mysql.WithReadyDatabases(0))...)
		if err != nil {
			t.Fatalf("EnsureTemplate: %v", err)
		}
		return tpl
	}

	tpl := ensure(mysql.WithFingerprint("v1"), mysql.WithKeepTemplate())
	release(t, tpl)
	if !databaseExists(t, admin, name) {
		t.Fatal("kept template was dropped")
	}
	tpl = ensure(mysql.WithFingerprint("v1"), mysql.WithKeepTemplate())
	release(t, tpl)
	if setupRuns != 1 {
		t.Fatalf("setup ran %d times after a matching fingerprint, want 1", setupRuns)
	}
	tpl = ensure(mysql.WithFingerprint("v2"))
	if setupRuns != 2 {
		t.Fatalf("setup ran %d times after a changed fingerprint, want 2", setupRuns)
	}
	release(t, tpl)
	if databaseExists(t, admin, name) {
		t.Fatal("template without WithKeepTemplate was not dropped")
	}
}

func TestTemplateStaleWhileInUse(t *testing.T) {
	requireMySQL(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_stale")

	holder, err := mysql.EnsureTemplate(ctx, name, nil, mysql.WithFingerprint("v1"), mysql.WithReadyDatabases(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(t, holder) })

	_, err = mysql.EnsureTemplate(ctx, name, nil, mysql.WithFingerprint("v2"), mysql.WithReadyDatabases(0))
	if err == nil || !strings.Contains(err.Error(), "in use by") {
		t.Fatalf("expected an in-use error, got %v", err)
	}
}

func TestTemplateHalfBuiltIsRebuilt(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_half")

	// A database under the template's name with no completeness marker is
	// what a build that crashed leaves behind
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE TABLE `"+name+"`.leftover (id INT)"); err != nil {
		t.Fatal(err)
	}

	setupRuns := 0
	tpl, err := mysql.EnsureTemplate(ctx, name, func(ctx context.Context, dsn string) error { setupRuns++; return nil },
		mysql.WithReadyDatabases(0))
	if err != nil {
		t.Fatalf("EnsureTemplate over a half-built template: %v", err)
	}
	t.Cleanup(func() { release(t, tpl) })
	if setupRuns != 1 {
		t.Errorf("setup ran %d times, want 1 (rebuild)", setupRuns)
	}
	var n int
	if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'leftover'", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the half-built template's contents survived the rebuild")
	}
}

func TestTemplateDeadProcessIsIgnoredAndSwept(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_dead")

	tpl, err := mysql.EnsureTemplate(ctx, name, nil, mysql.WithReadyDatabases(0))
	if err != nil {
		t.Fatal(err)
	}
	// Fake another process that died: a user row and a prepared database
	// whose connection id no longer exists
	var now int64
	if err := admin.QueryRowContext(ctx, "SELECT UNIX_TIMESTAMP()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	var uptime string
	var ignored string
	if err := admin.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Uptime'").Scan(&ignored, &uptime); err != nil {
		t.Fatal(err)
	}
	var up int64
	if _, err := fmtSscan(uptime, &up); err != nil {
		t.Fatal(err)
	}
	start := now - up
	const deadID = 4000000000 // no live session has this id
	orphan := uniqueTemplateName(t, "test_orphan")
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+orphan+"`"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+name+"`.`_testdb_users` (connection_id, server_started_at) VALUES (?, ?)", deadID, start); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO `"+name+"`.`_testdb_ready` (db_name, connection_id, server_started_at) VALUES (?, ?, ?)", orphan, deadID, start); err != nil {
		t.Fatal(err)
	}

	// We are the only live user, so Release must drop the template and
	// sweep the dead process's prepared database
	release(t, tpl)
	if databaseExists(t, admin, name) {
		t.Error("template kept alive by a dead process's row")
	}
	if databaseExists(t, admin, orphan) {
		t.Error("a dead process's prepared database was not swept")
		_, _ = admin.ExecContext(ctx, "DROP DATABASE `"+orphan+"`")
	}
}

func TestTemplateReadyPool(t *testing.T) {
	requireMySQL(t)
	admin := adminDB(t)
	ctx := context.Background()
	name := uniqueTemplateName(t, "tpl_pool")

	tpl, err := mysql.EnsureTemplate(ctx, name, mysql.SQLFiles(schemaDir),
		mysql.WithReadyDatabases(2), mysql.WithReadyPrefix("ready_pool"))
	if err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs after the test databases taken below
	// have been dropped by their own cleanups: then nothing may remain
	t.Cleanup(func() {
		release(t, tpl)
		var leftovers int
		if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'ready\\_pool\\_%'").Scan(&leftovers); err != nil {
			t.Fatal(err)
		}
		if leftovers != 0 {
			t.Errorf("%d prepared databases left behind after Release", leftovers)
		}
		if databaseExists(t, admin, name) {
			t.Error("template not dropped")
		}
	})

	waitFor(t, "the pool to fill", func() bool { return readyCount(t, admin, name) == 2 })

	// A test takes a prepared database: the name carries the pool's prefix,
	// the clone is complete, and the pool refills
	db := mysql.Setup(t, tpl.Option())
	got := currentDatabase(t, db)
	if !strings.HasPrefix(got, "ready_pool_") {
		t.Errorf("test got %s, want a prepared database", got)
	}
	if !tableExists(t, db, "users") {
		t.Error("prepared database lacks the schema")
	}
	waitFor(t, "the pool to refill", func() bool { return readyCount(t, admin, name) == 2 })

	// Taking faster than the pool refills never waits: whatever is not
	// ready is cloned on demand, under a regular test name. Every database
	// handed out has the schema either way.
	onDemand := 0
	for range 5 {
		fresh := mysql.Setup(t, tpl.Option())
		if !tableExists(t, fresh, "users") {
			t.Fatal("clone lacks the schema")
		}
		if strings.HasPrefix(currentDatabase(t, fresh), "test_") {
			onDemand++
		}
	}
	t.Logf("5 rapid takes: %d served on demand, %d from the pool", onDemand, 5-onDemand)
}

func TestTemplateNameValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := mysql.EnsureTemplate(ctx, "", nil); err == nil {
		t.Error("expected an error for an empty name")
	}
	if _, err := mysql.EnsureTemplate(ctx, strings.Repeat("x", 65), nil); err == nil || !strings.Contains(err.Error(), "maximum is 64") {
		t.Errorf("expected a length error, got %v", err)
	}
}

func TestTemplateContextCancelled(t *testing.T) {
	requireMySQL(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mysql.EnsureTemplate(ctx, uniqueTemplateName(t, "tpl_ctx"), nil, mysql.WithReadyDatabases(0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// fmtSscan parses a decimal string into n.
func fmtSscan(s string, n *int64) (int, error) {
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number: " + s)
		}
		v = v*10 + int64(c-'0')
	}
	*n = v
	return 1, nil
}
