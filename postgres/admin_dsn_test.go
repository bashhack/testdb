package postgres_test

import (
	"context"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/postgres"
)

// An address nothing listens on, so Initialize fails fast after resolving
// the DSN, which is the part under test.
const unreachableDSN = "postgres://u:p@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=1"

func TestAdminDSNEnvPrecedence(t *testing.T) {
	t.Setenv(postgres.AdminDSNEnv, unreachableDSN)
	t.Setenv("TEST_DATABASE_URL", "postgres://shared/should-lose")
	t.Setenv("DATABASE_URL", "postgres://generic/should-lose")

	provider := &postgres.PostgresProvider{}
	err := provider.Initialize(context.Background(), testdb.Config{})
	if err == nil {
		t.Cleanup(func() {
			if err := provider.Cleanup(context.Background()); err != nil {
				t.Error(err)
			}
		})
		t.Fatal("expected the connection to the unreachable address to fail")
	}
	if got := provider.ResolvedAdminDSN(); got != unreachableDSN {
		t.Errorf("resolved %q, want %s to win: %q", got, postgres.AdminDSNEnv, unreachableDSN)
	}
}

func TestAdminDSNDefault(t *testing.T) {
	t.Setenv(postgres.AdminDSNEnv, "")
	t.Setenv("TEST_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")

	provider := &postgres.PostgresProvider{}
	err := provider.Initialize(context.Background(), testdb.Config{})
	if err == nil {
		t.Cleanup(func() {
			if err := provider.Cleanup(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	// Whether or not a server answers at the default address, the DSN
	// chosen must be the documented default
	if got := provider.ResolvedAdminDSN(); got != postgres.DefaultAdminDSN {
		t.Errorf("resolved %q, want the default %q", got, postgres.DefaultAdminDSN)
	}
}
