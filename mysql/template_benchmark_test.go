package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/mysql"
)

// The benchmarks compare what a test pays for an isolated database with a
// realistic schema: creating it and running the migrations, cloning a
// template on demand, and taking a clone the template prepared ahead.
// These are the numbers the README quotes for MySQL.

const benchTables = 50

// writeBenchSchema writes a schema of benchTables tables with an index each
// and some rows, as one multi-statement SQL file.
func writeBenchSchema(b *testing.B) string {
	b.Helper()
	var sql strings.Builder
	for i := range benchTables {
		fmt.Fprintf(&sql, `CREATE TABLE t%d (
			id INT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(100) NOT NULL,
			price DECIMAL(10,2) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			INDEX (name)
		);
`, i)
	}
	for i := range 5 {
		fmt.Fprintf(&sql, "INSERT INTO t%d (name, price) WITH RECURSIVE n AS (SELECT 1 AS k UNION ALL SELECT k+1 FROM n WHERE k < 1000) SELECT CONCAT('Product ', k), 19.99 FROM n;\n", i)
	}
	dir := b.TempDir()
	path := filepath.Join(dir, "001_schema.sql")
	if err := os.WriteFile(path, []byte(sql.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	return dir
}

func requireMySQLB(b *testing.B) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &mysql.Provider{}
	if err := p.Initialize(ctx, testdb.Config{}); err != nil {
		b.Skipf("MySQL not available: %v", err)
	}
	if err := p.Cleanup(ctx); err != nil {
		b.Fatal(err)
	}
}

func ensureBenchTemplate(b *testing.B, name string, schemaDir string, opts ...mysql.TemplateOption) *mysql.Template {
	b.Helper()
	ctx := context.Background()
	tpl, err := mysql.EnsureTemplate(ctx, name, mysql.SQLFiles(schemaDir), opts...)
	if err != nil {
		b.Fatalf("EnsureTemplate: %v", err)
	}
	b.Cleanup(func() {
		if err := tpl.Release(ctx); err != nil {
			b.Errorf("Release: %v", err)
		}
	})
	return tpl
}

// BenchmarkEmptyDatabase measures CREATE DATABASE and the drop, nothing else.
func BenchmarkEmptyDatabase(b *testing.B) {
	requireMySQLB(b)
	b.ReportAllocs()
	for b.Loop() {
		mysql.New(b, &mysql.SQLDBInitializer{})
	}
}

// BenchmarkFreshWithMigrations measures what every test pays without a
// template: a new database plus the whole schema.
func BenchmarkFreshWithMigrations(b *testing.B) {
	requireMySQLB(b)
	schemaDir := writeBenchSchema(b)
	b.ReportAllocs()
	for b.Loop() {
		mysql.New(b, &mysql.SQLDBInitializer{}, testdb.WithMigrator(mysql.SQLFiles(schemaDir)))
	}
}

// BenchmarkTemplateCloneOnDemand measures a clone made when the test asks,
// with no databases prepared ahead.
func BenchmarkTemplateCloneOnDemand(b *testing.B) {
	requireMySQLB(b)
	schemaDir := writeBenchSchema(b)
	tpl := ensureBenchTemplate(b, "bench_mysql_tpl_ondemand", schemaDir, mysql.WithReadyDatabases(0))
	b.ReportAllocs()
	for b.Loop() {
		mysql.New(b, &mysql.SQLDBInitializer{}, tpl.Option())
	}
}

// BenchmarkTemplateReady measures taking databases the template prepared
// ahead, with a pause between takes standing in for the test's own work.
// The pool keeps up only while tests take clones no faster than the
// workers make them, so the pause length is what decides the result; the
// sub-benchmarks cover a quick test and an integration-sized one, and
// report how many takes were served from the pool.
func BenchmarkTemplateReady(b *testing.B) {
	for _, workers := range []int{2, 4, 8} {
		for _, work := range []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond} {
			b.Run(fmt.Sprintf("workers=%d/test=%s", workers, work), func(b *testing.B) {
				requireMySQLB(b)
				schemaDir := writeBenchSchema(b)
				tpl := ensureBenchTemplate(b, "bench_mysql_tpl_ready", schemaDir,
					mysql.WithReadyDatabases(8), mysql.WithReadyWorkers(workers), mysql.WithReadyPrefix("bench_ready"))
				time.Sleep(6 * time.Second) // let the pool fill once
				pooled := 0
				b.ReportAllocs()
				for b.Loop() {
					db := mysql.New(b, &mysql.SQLDBInitializer{}, tpl.Option())
					b.StopTimer()
					if strings.HasPrefix(db.Name(), "bench_ready_") {
						pooled++
					}
					// The test's own work; not part of what we measure
					var n int
					if err := db.Entity().(*sql.DB).QueryRowContext(context.Background(), "SELECT COUNT(*) FROM t0").Scan(&n); err != nil {
						b.Fatal(err)
					}
					time.Sleep(work)
					b.StartTimer()
				}
				b.ReportMetric(float64(pooled)/float64(b.N)*100, "%pooled")
			})
		}
	}
}
