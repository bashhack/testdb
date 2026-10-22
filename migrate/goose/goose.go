// Package goose runs pressly/goose migrations for testdb through goose's Go
// library, so no goose binary is needed.
//
//	pool := postgres.Setup(t, testdb.WithMigrator(goose.Dir("./migrations")))
//
// A Migrator's Migrate method is also a template setup function:
//
//	tpl, err := postgres.EnsureTemplate(ctx, "schema", goose.Dir("./migrations").Migrate)
package goose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	// Registers the "pgx" database/sql driver that goose runs through.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migrator applies goose migrations to a database. Create one with Dir or FS.
type Migrator struct {
	fsys    fs.FS
	path    string
	dir     string // set by Dir, for a clear error when it does not exist
	dialect goose.Dialect
	driver  string
	opts    []goose.ProviderOption
}

// Option configures a Migrator.
type Option func(*Migrator)

// WithDialect sets the goose dialect. By default it is inferred from the
// DSN's scheme (postgres, mysql, sqlite).
func WithDialect(dialect goose.Dialect) Option {
	return func(m *Migrator) {
		m.dialect = dialect
	}
}

// WithSQLDriver sets the database/sql driver name used to connect. The
// default follows the dialect: "pgx" for PostgreSQL, "mysql" for MySQL and
// "sqlite" for SQLite. Only the pgx driver is registered by this package;
// import the others yourself.
func WithSQLDriver(name string) Option {
	return func(m *Migrator) {
		m.driver = name
	}
}

// WithProviderOptions passes options through to goose.NewProvider, for
// example goose.WithVerbose(true) or goose.WithAllowOutofOrder(true).
func WithProviderOptions(opts ...goose.ProviderOption) Option {
	return func(m *Migrator) {
		m.opts = append(m.opts, opts...)
	}
}

// Dir returns a Migrator for the migration files in a directory on disk.
func Dir(dir string, opts ...Option) *Migrator {
	m := FS(os.DirFS(dir), ".", opts...)
	m.dir = dir
	return m
}

// FS returns a Migrator for the migration files under path in fsys, which
// lets migrations ship inside the test binary with embed.FS:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	goose.FS(migrations, "migrations")
func FS(fsys fs.FS, path string, opts ...Option) *Migrator {
	m := &Migrator{fsys: fsys, path: path}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Migrate applies every pending migration to the database at dsn.
func (m *Migrator) Migrate(ctx context.Context, dsn string) (err error) {
	fsys, err := m.migrations()
	if err != nil {
		return err
	}

	dialect := m.dialect
	if dialect == "" {
		dialect, err = dialectFromDSN(dsn)
		if err != nil {
			return err
		}
	}
	driver := m.driver
	if driver == "" {
		driver = defaultDriver(dialect)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return fmt.Errorf("goose: open database with driver %q: %w", driver, err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	provider, err := goose.NewProvider(dialect, db, fsys, m.opts...)
	if err != nil {
		return fmt.Errorf("goose: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("goose: up: %w", err)
	}
	return nil
}

// migrations returns the file system holding the migrations, checking the
// directory first when one was given so a typo is reported as such.
func (m *Migrator) migrations() (fs.FS, error) {
	if m.dir != "" {
		if _, err := os.Stat(m.dir); err != nil {
			return nil, fmt.Errorf("goose: migrations directory: %w", err)
		}
	}
	if m.path == "" || m.path == "." {
		return m.fsys, nil
	}
	sub, err := fs.Sub(m.fsys, m.path)
	if err != nil {
		return nil, fmt.Errorf("goose: migrations path %q: %w", m.path, err)
	}
	return sub, nil
}

// dialectFromDSN picks the goose dialect from the DSN's scheme.
func dialectFromDSN(dsn string) (goose.Dialect, error) {
	scheme, _, ok := strings.Cut(dsn, "://")
	if !ok {
		return "", errors.New("goose: cannot infer the dialect from a DSN without a scheme; use goose.WithDialect")
	}
	switch strings.ToLower(scheme) {
	case "postgres", "postgresql", "pgx", "pgx5":
		return goose.DialectPostgres, nil
	case "mysql":
		return goose.DialectMySQL, nil
	case "sqlite", "sqlite3":
		return goose.DialectSQLite3, nil
	default:
		return "", fmt.Errorf("goose: no default dialect for DSN scheme %q; use goose.WithDialect", scheme)
	}
}

func defaultDriver(dialect goose.Dialect) string {
	switch dialect {
	case goose.DialectPostgres:
		return "pgx"
	case goose.DialectMySQL:
		return "mysql"
	case goose.DialectSQLite3:
		return "sqlite"
	default:
		return string(dialect)
	}
}
