package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/bashhack/testdb"
)

// adminDSNOrSkip returns the admin DSN when a MySQL server answers; it
// skips otherwise (fails in CI, where the server must be there).
func adminDSNOrSkip(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &Provider{}
	if err := p.Initialize(ctx, testdb.Config{}); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("MySQL is required in CI but not reachable: %v", err)
		}
		t.Skipf("MySQL not available: %v", err)
	}
	dsn := p.ResolvedAdminDSN()
	if err := p.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func openAdmin(t *testing.T, dsn string) *sql.DB {
	t.Helper()
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

func mustExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, s := range statements {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func dropOnCleanup(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range names {
			if _, err := db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+quoteIdentifier(name)); err != nil {
				t.Errorf("drop %s: %v", name, err)
			}
		}
	})
}

func scanInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestCloneDatabaseFidelity builds a source database with the things a
// logical clone can get wrong, clones it, and checks each one in the copy.
func TestCloneDatabaseFidelity(t *testing.T) {
	dsn := adminDSNOrSkip(t)
	admin := openAdmin(t, dsn)
	ctx := context.Background()
	source := uniqueName(t, "clone_src")
	target := uniqueName(t, "clone_dst")
	dropOnCleanup(t, admin, source, target)

	q := func(table string) string { return qualify(source, table) }
	mustExec(t, admin,
		"CREATE DATABASE "+quoteIdentifier(source)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_bin",
		"CREATE TABLE "+q("authors")+" (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100) NOT NULL)",
		"CREATE TABLE "+q("books")+` (
			id INT AUTO_INCREMENT PRIMARY KEY,
			author_id INT NOT NULL,
			title VARCHAR(200) NOT NULL,
			pages INT NOT NULL,
			weight_kg DECIMAL(6,3) GENERATED ALWAYS AS (pages * 0.005) STORED,
			CONSTRAINT fk_books_author FOREIGN KEY (author_id) REFERENCES `+q("authors")+`(id),
			INDEX idx_books_title (title)
		)`,
		"CREATE TABLE "+q("audit")+" (id INT AUTO_INCREMENT PRIMARY KEY, note VARCHAR(200) NOT NULL)",
		// Bookkeeping that must not be copied
		"CREATE TABLE "+q(bookkeepingPrefix+"info")+" (fingerprint VARCHAR(64))",
		"INSERT INTO "+q(bookkeepingPrefix+"info")+" VALUES ('abc')",
		"INSERT INTO "+q("authors")+" (name) VALUES ('Ann'), ('Bo')",
		"INSERT INTO "+q("books")+" (author_id, title, pages) VALUES (1, 'First', 100), (2, 'Second', 300)",
		// Push the counter well past the rows, so a clone that only copied rows would differ
		"ALTER TABLE "+q("authors")+" AUTO_INCREMENT = 1000",
		"CREATE VIEW "+q("book_titles")+" AS SELECT b.title, a.name FROM "+q("books")+" b JOIN "+q("authors")+" a ON a.id = b.author_id",
		"CREATE VIEW "+q("book_title_count")+" AS SELECT COUNT(*) AS n FROM "+q("book_titles"),
		"CREATE TRIGGER "+q("books_audit")+" AFTER INSERT ON "+q("books")+" FOR EACH ROW INSERT INTO "+q("audit")+" (note) VALUES (CONCAT('added ', NEW.title))",
		"CREATE PROCEDURE "+q("add_author")+" (IN p_name VARCHAR(100)) INSERT INTO "+q("authors")+" (name) VALUES (p_name)",
		"CREATE FUNCTION "+q("book_count")+" () RETURNS INT READS SQL DATA RETURN (SELECT COUNT(*) FROM "+q("books")+")",
	)

	if err := cloneDatabase(ctx, dsn, source, target); err != nil {
		t.Fatalf("cloneDatabase: %v", err)
	}

	// Charset and collation
	var charset, collation string
	if err := admin.QueryRowContext(ctx, "SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", target).Scan(&charset, &collation); err != nil {
		t.Fatal(err)
	}
	if charset != "utf8mb4" || collation != "utf8mb4_bin" {
		t.Errorf("charset/collation = %s/%s, want utf8mb4/utf8mb4_bin", charset, collation)
	}

	// Tables and rows, bookkeeping excluded
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'", target); n != 3 {
		t.Errorf("clone has %d base tables, want 3 (authors, books, audit; no bookkeeping)", n)
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE '\\_testdb\\_%'", target); n != 0 {
		t.Errorf("bookkeeping tables were copied")
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM "+qualify(target, "books")); n != 2 {
		t.Errorf("books rows = %d, want 2", n)
	}

	// Foreign key, index, generated column
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = ? AND CONSTRAINT_NAME = 'fk_books_author'", target); n != 1 {
		t.Error("foreign key was not copied")
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND INDEX_NAME = 'idx_books_title'", target); n == 0 {
		t.Error("index was not copied")
	}
	var weight string
	if err := admin.QueryRowContext(ctx, "SELECT weight_kg FROM "+qualify(target, "books")+" WHERE title = 'Second'").Scan(&weight); err != nil {
		t.Fatalf("generated column: %v", err)
	}
	if weight != "1.500" {
		t.Errorf("generated column recomputed to %s, want 1.500", weight)
	}

	// AUTO_INCREMENT position carried over
	var nextID int64
	mustExec(t, admin, "INSERT INTO "+qualify(target, "authors")+" (name) VALUES ('Cy')")
	if err := admin.QueryRowContext(ctx, "SELECT id FROM "+qualify(target, "authors")+" WHERE name = 'Cy'").Scan(&nextID); err != nil {
		t.Fatal(err)
	}
	if nextID < 1000 {
		t.Errorf("AUTO_INCREMENT did not carry over: new id %d, want >= 1000", nextID)
	}

	// Views point at the clone, not the source, and a view over a view works
	if n := scanInt(t, admin, "SELECT n FROM "+qualify(target, "book_title_count")); n != 2 {
		t.Errorf("view over view returned %d, want 2", n)
	}
	mustExec(t, admin, "INSERT INTO "+qualify(target, "books")+" (author_id, title, pages) VALUES (1, 'Third', 50)")
	if n := scanInt(t, admin, "SELECT n FROM "+qualify(target, "book_title_count")); n != 3 {
		t.Errorf("view does not read the clone's tables: %d, want 3", n)
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM "+qualify(source, "books")); n != 2 {
		t.Errorf("writing to the clone changed the source: %d books", n)
	}

	// Trigger fires in the clone (for the insert above) and did not fire during the copy
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM "+qualify(target, "audit")+" WHERE note = 'added Third'"); n != 1 {
		t.Errorf("trigger did not fire in the clone")
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM "+qualify(target, "audit")); n != 1 {
		t.Errorf("audit has %d rows, want 1: the trigger must not fire while rows are copied", n)
	}

	// Routines
	mustExec(t, admin, "CALL "+qualify(target, "add_author")+"('Di')")
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM "+qualify(target, "authors")); n != 4 {
		t.Errorf("procedure did not run in the clone: %d authors", n)
	}
	if n := scanInt(t, admin, "SELECT "+qualify(target, "book_count")+"()"); n != 3 {
		t.Errorf("function returned %d, want 3", n)
	}
}

func TestCloneDatabaseMissingSource(t *testing.T) {
	dsn := adminDSNOrSkip(t)
	admin := openAdmin(t, dsn)
	target := uniqueName(t, "clone_dst")
	dropOnCleanup(t, admin, target)

	err := cloneDatabase(context.Background(), dsn, uniqueName(t, "no_such"), target)
	if err == nil || !containsString(err.Error(), "does not exist") {
		t.Fatalf("expected a 'does not exist' error, got %v", err)
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", target); n != 0 {
		t.Error("target was created although the source is missing")
	}
}

func TestCloneDatabaseFailureLeavesNothing(t *testing.T) {
	dsn := adminDSNOrSkip(t)
	admin := openAdmin(t, dsn)
	source := uniqueName(t, "clone_src")
	target := uniqueName(t, "clone_dst")
	dropOnCleanup(t, admin, source, target)

	// A view whose table is gone cannot be recreated: SHOW CREATE VIEW works
	// but the CREATE fails, which must roll the whole clone back
	mustExec(t, admin,
		"CREATE DATABASE "+quoteIdentifier(source),
		"CREATE TABLE "+qualify(source, "t")+" (id INT)",
		"CREATE VIEW "+qualify(source, "v")+" AS SELECT id FROM "+qualify(source, "t"),
		"DROP TABLE "+qualify(source, "t"),
	)
	if err := cloneDatabase(context.Background(), dsn, source, target); err == nil {
		t.Fatal("expected the clone to fail")
	}
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", target); n != 0 {
		t.Error("a failed clone left the target database behind")
	}
}

func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
