package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bashhack/testdb"
	mysqldriver "github.com/go-sql-driver/mysql"
)

// LoadSQLFile returns a migrator that runs one SQL file against the
// database, for schema dumps and fixture data. The file may hold several
// statements. Client-only syntax such as the mysql CLI's DELIMITER command
// is not SQL and will not work.
//
//	mysql.Setup(t, testdb.WithMigrator(mysql.LoadSQLFile("schema.sql")))
func LoadSQLFile(path string) testdb.MigratorFunc {
	return func(ctx context.Context, dsn string) error {
		return execSQLFiles(ctx, dsn, []string{path})
	}
}

// SQLFiles returns a migrator that runs every .sql file in dir, in lexical
// order, against the database: the simplest way to apply a schema without a
// migration tool. Name files so they sort the way they should run
// (001_users.sql, 002_orders.sql). The directory is read when the migrator
// runs, not when it is created.
//
//	mysql.Setup(t, testdb.WithMigrator(mysql.SQLFiles("./schema")))
func SQLFiles(dir string) testdb.MigratorFunc {
	return func(ctx context.Context, dsn string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read SQL directory: %w", err)
		}
		var paths []string
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".sql") {
				paths = append(paths, filepath.Join(dir, entry.Name()))
			}
		}
		if len(paths) == 0 {
			return fmt.Errorf("no .sql files in %s", dir)
		}
		return execSQLFiles(ctx, dsn, paths)
	}
}

// execSQLFiles runs the files in order on one connection opened with
// multiStatements, so a file is one Exec however many statements it holds.
// Every file is read before connecting, so a missing file fails without
// touching the database.
func execSQLFiles(ctx context.Context, dsn string, paths []string) (err error) {
	contents := make([]string, len(paths))
	for i, path := range paths {
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read SQL file %s: %w", path, err)
		}
		contents[i] = string(sqlBytes)
	}

	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	config.MultiStatements = true
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()

	for i, statements := range contents {
		if _, err := conn.ExecContext(ctx, statements); err != nil {
			return fmt.Errorf("execute SQL from %s: %w", paths[i], err)
		}
	}
	return nil
}
