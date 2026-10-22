// Package golangmigrate runs golang-migrate/migrate migrations for testdb
// through its Go library, so no migrate binary is needed.
//
//	pool := postgres.Setup(t, testdb.WithMigrator(golangmigrate.Dir("./migrations")))
//
// A Migrator's Migrate method is also a template setup function:
//
//	tpl, err := postgres.EnsureTemplate(ctx, "schema", golangmigrate.Dir("./migrations").Migrate)
package golangmigrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	// Registers the mysql:// database driver.
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	// Registers the pgx5:// database driver.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrator applies golang-migrate migrations to a PostgreSQL or MySQL
// database. Create one with Dir or FS.
type Migrator struct {
	fsys fs.FS
	path string
	dir  string // set by Dir, for a clear error when it does not exist
}

// Dir returns a Migrator for the migration files in a directory on disk.
func Dir(dir string) *Migrator {
	return &Migrator{fsys: os.DirFS(dir), path: ".", dir: dir}
}

// FS returns a Migrator for the migration files under path in fsys, which
// lets migrations ship inside the test binary with embed.FS:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	golangmigrate.FS(migrations, "migrations")
func FS(fsys fs.FS, path string) *Migrator {
	return &Migrator{fsys: fsys, path: path}
}

// Migrate applies every pending up migration to the database at dsn. It
// stops between migrations if ctx is cancelled.
func (m *Migrator) Migrate(ctx context.Context, dsn string) (err error) {
	if m.dir != "" {
		if _, err := os.Stat(m.dir); err != nil {
			return fmt.Errorf("golangmigrate: migrations directory: %w", err)
		}
	}
	source, err := iofs.New(m.fsys, m.path)
	if err != nil {
		return fmt.Errorf("golangmigrate: read migrations: %w", err)
	}
	dbURL, err := databaseURL(dsn)
	if err != nil {
		return err
	}

	mg, err := migrate.NewWithSourceInstance("iofs", source, dbURL)
	if err != nil {
		return fmt.Errorf("golangmigrate: %w", err)
	}
	defer func() {
		sourceErr, dbErr := mg.Close()
		err = errors.Join(err, sourceErr, dbErr)
	}()

	// Up has no context of its own; GracefulStop asks it to stop after the
	// migration it is on.
	done := make(chan error, 1)
	go func() { done <- mg.Up() }()
	var upErr error
	select {
	case upErr = <-done:
	case <-ctx.Done():
		select {
		case mg.GracefulStop <- true:
		default:
		}
		// Wait for Up to stop, and report anything it ran into on the way
		upErr = <-done
		if errors.Is(upErr, migrate.ErrNoChange) {
			upErr = nil
		}
		return errors.Join(ctx.Err(), upErr)
	}
	if errors.Is(upErr, migrate.ErrNoChange) {
		return nil
	}
	if upErr != nil {
		return fmt.Errorf("golangmigrate: up: %w", upErr)
	}
	return nil
}

// databaseURL turns a testdb DSN into the URL golang-migrate's drivers
// register for: a PostgreSQL URL gets the pgx5 scheme, and go-sql-driver's
// native MySQL DSN (which has no scheme) is prefixed with mysql://, which is
// the form golang-migrate's MySQL driver parses.
func databaseURL(dsn string) (string, error) {
	if !strings.Contains(dsn, "://") {
		if _, err := mysqldriver.ParseDSN(dsn); err == nil {
			return "mysql://" + dsn, nil
		}
		return "", errors.New("golangmigrate: the DSN must be a PostgreSQL URL (postgres://...) or a native MySQL DSN (user:pass@tcp(host:port)/db)")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("golangmigrate: parse DSN: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql", "pgx", "pgx5":
		u.Scheme = "pgx5"
	case "mysql":
		return "", errors.New("golangmigrate: give MySQL DSNs in go-sql-driver's native form (user:pass@tcp(host:port)/db), as testdb's mysql package does")
	default:
		return "", fmt.Errorf("golangmigrate: unsupported DSN scheme %q", u.Scheme)
	}
	return u.String(), nil
}
