// Package tern runs jackc/tern migrations for testdb through tern's Go
// library, so no tern binary or tern.conf is needed.
//
//	pool := postgres.Setup(t, testdb.WithMigrator(tern.Dir("./migrations")))
//
// A Migrator's Migrate method is also a template setup function:
//
//	tpl, err := postgres.EnsureTemplate(ctx, "schema", tern.Dir("./migrations").Migrate)
package tern

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/tern/v2/migrate"
)

// DefaultVersionTable is where tern records the applied version, matching
// the tern CLI's default.
const DefaultVersionTable = "schema_version"

// Migrator applies tern migrations to a PostgreSQL database. Create one with
// Dir or FS.
type Migrator struct {
	fsys         fs.FS
	path         string
	dir          string // set by Dir, for a clear error when it does not exist
	versionTable string
	disableTx    bool
}

// Option configures a Migrator.
type Option func(*Migrator)

// WithVersionTable sets the table tern records the schema version in.
// Default: DefaultVersionTable.
func WithVersionTable(name string) Option {
	return func(m *Migrator) {
		m.versionTable = name
	}
}

// WithDisableTx runs migrations outside a transaction, for statements that
// cannot run inside one (CREATE INDEX CONCURRENTLY, for example).
func WithDisableTx() Option {
	return func(m *Migrator) {
		m.disableTx = true
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
//	tern.FS(migrations, "migrations")
func FS(fsys fs.FS, path string, opts ...Option) *Migrator {
	m := &Migrator{fsys: fsys, path: path, versionTable: DefaultVersionTable}
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

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("tern: connect: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close(context.WithoutCancel(ctx))) }()

	migrator, err := migrate.NewMigratorEx(ctx, conn, m.versionTable, &migrate.MigratorOptions{DisableTx: m.disableTx})
	if err != nil {
		return fmt.Errorf("tern: %w", err)
	}
	if err := migrator.LoadMigrations(fsys); err != nil {
		return fmt.Errorf("tern: load migrations: %w", err)
	}
	if err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("tern: migrate: %w", err)
	}
	return nil
}

// migrations returns the file system holding the migrations, checking the
// directory first when one was given so a typo is reported as such.
func (m *Migrator) migrations() (fs.FS, error) {
	if m.dir != "" {
		if _, err := os.Stat(m.dir); err != nil {
			return nil, fmt.Errorf("tern: migrations directory: %w", err)
		}
	}
	if m.path == "" || m.path == "." {
		return m.fsys, nil
	}
	sub, err := fs.Sub(m.fsys, m.path)
	if err != nil {
		return nil, fmt.Errorf("tern: migrations path %q: %w", m.path, err)
	}
	return sub, nil
}
