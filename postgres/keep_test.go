package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// currentDatabase returns the name of the database a pool is connected to.
func currentDatabase(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	return name
}

// dropDatabase removes a database a test deliberately left behind.
func dropDatabase(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	provider := &postgres.PostgresProvider{}
	if err := provider.Initialize(ctx, testdb.Config{}); err != nil {
		t.Fatalf("initialize provider: %v", err)
	}
	defer func() {
		if err := provider.Cleanup(ctx); err != nil {
			t.Errorf("provider cleanup: %v", err)
		}
	}()
	if err := provider.TerminateConnections(ctx, name); err != nil {
		t.Errorf("terminate connections to %s: %v", name, err)
	}
	if err := provider.DropDatabase(ctx, name); err != nil {
		t.Errorf("drop %s: %v", name, err)
	}
}

func TestKeepOnFailureKeepsFailedTestDatabase(t *testing.T) {
	spy := &spyTB{TB: t}
	pool := postgres.Setup(spy, testdb.WithKeepOnFailure(), testdb.WithDBPrefix("keep_test"))
	name := currentDatabase(t, pool)
	// Make sure the database is gone even if an assertion below fails
	t.Cleanup(func() {
		if exists, _ := postgres.TemplateExists(context.Background(), name); exists {
			dropDatabase(t, name)
		}
	})

	spy.failed = true // the test under observation fails
	spy.runCleanups()

	exists, err := postgres.TemplateExists(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("database %s was dropped although the test failed with KeepOnFailure", name)
	}

	var kept string
	for _, msg := range spy.logMessages {
		if strings.Contains(msg, "kept database") {
			kept = msg
		}
	}
	if kept == "" {
		t.Fatalf("expected a 'kept database' log line, got %v", spy.logMessages)
	}
	if !strings.Contains(kept, name) || !strings.Contains(kept, "psql") {
		t.Errorf("log should name the database and give a psql command, got: %s", kept)
	}
}

func TestKeepOnFailureDropsPassingTestDatabase(t *testing.T) {
	spy := &spyTB{TB: t}
	pool := postgres.Setup(spy, testdb.WithKeepOnFailure())
	name := currentDatabase(t, pool)

	spy.runCleanups() // the test under observation passed

	exists, err := postgres.TemplateExists(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		dropDatabase(t, name)
		t.Fatalf("database %s of a passing test was kept", name)
	}
}

func TestFailedTestDatabaseIsDroppedByDefault(t *testing.T) {
	t.Setenv(testdb.KeepOnFailureEnv, "")
	spy := &spyTB{TB: t}
	pool := postgres.Setup(spy)
	name := currentDatabase(t, pool)

	spy.failed = true
	spy.runCleanups()

	exists, err := postgres.TemplateExists(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		dropDatabase(t, name)
		t.Fatalf("database %s was kept without KeepOnFailure", name)
	}
}

func TestKeepOnFailureFromEnvironment(t *testing.T) {
	t.Setenv(testdb.KeepOnFailureEnv, "1")
	spy := &spyTB{TB: t}
	pool := postgres.Setup(spy, testdb.WithDBPrefix("keep_env_test"))
	name := currentDatabase(t, pool)
	t.Cleanup(func() {
		if exists, _ := postgres.TemplateExists(context.Background(), name); exists {
			dropDatabase(t, name)
		}
	})

	spy.failed = true
	spy.runCleanups()

	exists, err := postgres.TemplateExists(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("database %s was dropped although %s=1 and the test failed", name, testdb.KeepOnFailureEnv)
	}
}
