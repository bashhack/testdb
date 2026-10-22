package testdb

import "context"

// RunMigrations runs the configured Migrator (see WithMigrator) against this
// database. postgres.Setup and postgres.New call it for you right after
// creating the database; it is exposed for the low-level testdb.New path,
// where you choose when it runs.
//
// It returns ErrNoMigrator (wrapped) if no Migrator was configured. The
// database is not dropped on failure; call Close if you want that.
//
// Example:
//
//	db, err := testdb.New(t, provider, initializer,
//	    testdb.WithMigrator(goose.Dir("./migrations")))
//	if err != nil {
//	    t.Fatal(err)
//	}
//	defer db.Close()
//
//	if err := db.RunMigrations(t.Context()); err != nil {
//	    t.Fatalf("migrations failed: %v", err)
//	}
func (td *TestDatabase) RunMigrations(ctx context.Context) error {
	if td.config.Migrator == nil {
		return &Error{Op: "RunMigrations", Err: ErrNoMigrator}
	}
	if err := td.config.Migrator.Migrate(ctx, td.dsn); err != nil {
		return &Error{Op: "RunMigrations", Err: err}
	}
	td.logf("testdb: migrations completed for %s", td.name)
	return nil
}
