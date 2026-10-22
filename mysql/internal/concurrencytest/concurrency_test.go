package concurrencytest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/mysql"
)

var templateName string

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Without a server there is nothing to test here
	probe := &mysql.Provider{}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := probe.Initialize(probeCtx, testdb.Config{})
	cancel()
	if err != nil {
		if os.Getenv("CI") != "" {
			fmt.Fprintln(os.Stderr, "MySQL is required in CI but not reachable:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "skipping: MySQL not available:", err)
		os.Exit(0)
	}
	if err := probe.Cleanup(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	templateName = os.Getenv("TESTDB_CONC_TEMPLATE")
	if templateName == "" {
		templateName = "testdb_mysql_conc_standalone"
	}

	setup := func(ctx context.Context, dsn string) error {
		if path := os.Getenv("TESTDB_CONC_SETUP_LOG"); path != "" {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, writeErr := fmt.Fprintf(f, "setup pid=%d\n", os.Getpid())
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return mysql.SQLFiles("../../../testdata/mysql/schema")(ctx, dsn)
	}

	opts := []mysql.TemplateOption{mysql.WithReadyDatabases(2)}
	if fp := os.Getenv("TESTDB_CONC_FINGERPRINT"); fp != "" {
		opts = append(opts, mysql.WithFingerprint(fp))
	}
	if prefix := os.Getenv("TESTDB_CONC_READY_PREFIX"); prefix != "" {
		opts = append(opts, mysql.WithReadyPrefix(prefix))
	}

	tpl, err := mysql.EnsureTemplate(ctx, templateName, setup, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if path := os.Getenv("TESTDB_CONC_READY_FILE"); path != "" {
		if err := os.WriteFile(path, []byte("ready\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if hold := os.Getenv("TESTDB_CONC_HOLD"); hold != "" {
		d, err := time.ParseDuration(hold)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(d)
	}

	code := m.Run()

	if err := tpl.Release(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}

func TestClonesFromTemplate(t *testing.T) {
	for i := range 3 {
		t.Run(fmt.Sprintf("clone%d", i), func(t *testing.T) {
			t.Parallel()
			// Spread the clones out so processes interleave differently each run
			time.Sleep(time.Duration(rand.IntN(50)) * time.Millisecond)

			db := mysql.Setup(t, testdb.WithTemplate(templateName))
			var n int
			if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
				t.Fatalf("query clone: %v", err)
			}
			if n == 0 {
				t.Fatal("clone has no fixture rows")
			}
		})
	}
}
