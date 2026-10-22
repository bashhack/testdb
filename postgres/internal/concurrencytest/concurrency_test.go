package concurrencytest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
)

var templateName string

func TestMain(m *testing.M) {
	ctx := context.Background()

	templateName = os.Getenv("TESTDB_CONC_TEMPLATE")
	if templateName == "" {
		templateName = "testdb_conc_standalone"
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
		return postgres.LoadSQLFile("../../../testdata/postgres/fixtures/seed_data.sql")(ctx, dsn)
	}

	var opts []postgres.TemplateOption
	if fp := os.Getenv("TESTDB_CONC_FINGERPRINT"); fp != "" {
		opts = append(opts, postgres.WithFingerprint(fp))
	}

	tpl, err := postgres.EnsureTemplate(ctx, templateName, setup, opts...)
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

			pool := postgres.Setup(t, testdb.WithTemplate(templateName))
			var n int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM users").Scan(&n); err != nil {
				t.Fatalf("query clone: %v", err)
			}
			if n == 0 {
				t.Fatal("clone has no fixture rows")
			}
		})
	}
}
