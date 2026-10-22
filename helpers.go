package testdb

import (
	"io"
	"testing"
)

// RunConfiguredMigrations and RegisterCleanup are what every database
// package's Setup and New are built from, so all providers behave the same
// way around a test: migrations run under the test's context, cleanup closes
// the entity before dropping, and KeepOnFailure means the same thing
// everywhere.

// RunConfiguredMigrations runs the Migrator set with WithMigrator, if any.
// On failure it drops the database (or keeps it, under KeepOnFailure, since
// a half-applied migration is exactly what one wants to look at) and calls
// t.Fatalf, so it does not return. callerName is the function the test
// called, for the message.
func RunConfiguredMigrations(t testing.TB, db *TestDatabase, callerName string) {
	t.Helper()
	if db.Config().Migrator == nil {
		return
	}
	err := db.RunMigrations(ContextOf(t))
	if err == nil {
		return
	}
	if db.Config().KeepOnFailure {
		db.Keep()
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Logf("Warning: failed to close database after migration error: %v", closeErr)
	}
	t.Fatalf("%s: migrations failed: %v", callerName, err)
}

// RegisterCleanup arranges for the database to go away when the test ends:
// the entity is closed first if it implements io.Closer, then Close drops
// the database. With KeepOnFailure, a failed test's database is kept
// instead (see Keep).
func RegisterCleanup(t testing.TB, db *TestDatabase) {
	t.Cleanup(func() {
		if entity := db.Entity(); entity != nil {
			if closer, ok := entity.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Logf("Warning: failed to close entity: %v", err)
				}
			}
		}

		if db.Config().KeepOnFailure && t.Failed() {
			db.Keep()
		}

		if err := db.Close(); err != nil {
			t.Errorf("testdb cleanup failed: %v", err)
		}
	})
}
