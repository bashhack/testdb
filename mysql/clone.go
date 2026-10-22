package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// MySQL has nothing that clones a database, so a template is copied object
// by object: tables from SHOW CREATE TABLE, rows with INSERT ... SELECT,
// then views, triggers, procedures and functions from their SHOW CREATE
// forms. Tables named _testdb_* hold testdb's own bookkeeping inside a
// template and are not copied.
//
// Deliberately not copied: events (a scheduler job firing inside a test's
// clone would make the test nondeterministic) and grants (tests connect as
// the admin user). DEFINER clauses are dropped, so objects belong to
// whoever runs the clone. A foreign key that references a table in another
// database keeps pointing at that database.

// bookkeepingPrefix names the tables testdb keeps inside a template.
const bookkeepingPrefix = "_testdb_"

// definerClause matches the DEFINER=`user`@`host` clause in SHOW CREATE
// output. It is dropped so the clone is owned by whoever runs it, which
// also avoids needing SET_USER_ID to impersonate the original definer.
var definerClause = regexp.MustCompile("DEFINER=`[^`]*`@`[^`]*` ")

// cloneDatabase creates target as a copy of source. It opens its own
// connection and makes target the current database, because SHOW CREATE
// output names objects without a schema. On failure target is dropped.
func cloneDatabase(ctx context.Context, adminDSN, source, target string) (err error) {
	config, err := mysqldriver.ParseDSN(adminDSN)
	if err != nil {
		return fmt.Errorf("parse admin DSN: %w", err)
	}
	config.DBName = ""
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return fmt.Errorf("open connection: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()

	var charset, collation string
	err = conn.QueryRowContext(ctx, `
		SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME
		FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, source).Scan(&charset, &collation)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("template database %q does not exist (create it with mysql.EnsureTemplate)", source)
	}
	if err != nil {
		return fmt.Errorf("look up template: %w", err)
	}

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s CHARACTER SET %s COLLATE %s",
		quoteIdentifier(target), charset, collation)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	// From here, a failure must not leave a half-copied database behind.
	// The drop goes through db, not conn: if the failure was a cancelled
	// statement, database/sql has discarded conn, and db hands out a
	// working connection instead.
	defer func() {
		if err != nil {
			_, dropErr := db.ExecContext(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+quoteIdentifier(target))
			err = errors.Join(err, dropErr)
		}
	}()

	for _, stmt := range []string{
		"USE " + quoteIdentifier(target),
		"SET FOREIGN_KEY_CHECKS = 0",
		"SET UNIQUE_CHECKS = 0",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}

	tables, err := listNames(ctx, conn, `
		SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`, source)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	tables = withoutBookkeeping(tables)

	for _, table := range tables {
		create, err := showCreate(ctx, conn, "SHOW CREATE TABLE "+qualify(source, table), "Create Table")
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, create); err != nil {
			return fmt.Errorf("create table %s: %w", table, err)
		}
	}

	// Generated columns cannot be inserted into; the clone recomputes them.
	// One query for every table's columns rather than one per table.
	columnsByTable, err := insertableColumns(ctx, conn, source)
	if err != nil {
		return err
	}
	for _, table := range tables {
		columns := columnsByTable[table]
		if len(columns) == 0 {
			continue
		}
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = quoteIdentifier(c)
		}
		list := strings.Join(quoted, ", ")
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
			quoteIdentifier(table), list, list, qualify(source, table))); err != nil {
			return fmt.Errorf("copy rows of %s: %w", table, err)
		}
	}

	if err := cloneViews(ctx, conn, source, target); err != nil {
		return err
	}
	if err := cloneTriggers(ctx, conn, source, target); err != nil {
		return err
	}
	if err := cloneRoutines(ctx, conn, source, target); err != nil {
		return err
	}
	return nil
}

// cloneViews creates the source's views in the target. A view's body is
// stored with its tables schema-qualified, so the source's name is
// rewritten to the target's. Views may depend on other views, so creation
// runs in passes until every view exists or a pass makes no progress.
func cloneViews(ctx context.Context, conn *sql.Conn, source, target string) error {
	views, err := listNames(ctx, conn,
		"SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", source)
	if err != nil {
		return fmt.Errorf("list views: %w", err)
	}
	pending := views
	for len(pending) > 0 {
		var failed []string
		var lastErr error
		for _, view := range pending {
			create, err := showCreate(ctx, conn, "SHOW CREATE VIEW "+qualify(source, view), "Create View")
			if err != nil {
				return err
			}
			create = rewriteSchema(create, source, target)
			if _, err := conn.ExecContext(ctx, create); err != nil {
				failed = append(failed, view)
				lastErr = fmt.Errorf("create view %s: %w", view, err)
			}
		}
		if len(failed) == len(pending) {
			return lastErr
		}
		pending = failed
	}
	return nil
}

// cloneTriggers creates the source's triggers in the target, after the
// data so they do not fire during the copy.
func cloneTriggers(ctx context.Context, conn *sql.Conn, source, target string) error {
	triggers, err := listNames(ctx, conn,
		"SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ? ORDER BY TRIGGER_NAME", source)
	if err != nil {
		return fmt.Errorf("list triggers: %w", err)
	}
	for _, trigger := range triggers {
		if err := createWithSQLMode(ctx, conn, "SHOW CREATE TRIGGER "+qualify(source, trigger),
			"SQL Original Statement", source, target); err != nil {
			return fmt.Errorf("create trigger %s: %w", trigger, err)
		}
	}
	return nil
}

// cloneRoutines creates the source's stored procedures and functions in
// the target.
func cloneRoutines(ctx context.Context, conn *sql.Conn, source, target string) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_NAME", source)
	if err != nil {
		return fmt.Errorf("list routines: %w", err)
	}
	type routine struct{ name, kind string }
	var routines []routine
	for rows.Next() {
		var r routine
		if err := rows.Scan(&r.name, &r.kind); err != nil {
			return errors.Join(fmt.Errorf("list routines: %w", err), rows.Close())
		}
		routines = append(routines, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list routines: %w", err)
	}
	for _, r := range routines {
		// SHOW CREATE PROCEDURE / FUNCTION; the column is "Create Procedure" / "Create Function"
		kind := strings.ToUpper(r.kind)
		column := "Create " + strings.ToUpper(kind[:1]) + strings.ToLower(kind[1:])
		if err := createWithSQLMode(ctx, conn, fmt.Sprintf("SHOW CREATE %s %s", kind, qualify(source, r.name)),
			column, source, target); err != nil {
			return fmt.Errorf("create %s %s: %w", strings.ToLower(kind), r.name, err)
		}
	}
	return nil
}

// createWithSQLMode runs a SHOW CREATE statement and executes the
// definition it returns in the target, under the sql_mode the object was
// created with, without its DEFINER clause and with the source schema's
// name rewritten to the target's.
func createWithSQLMode(ctx context.Context, conn *sql.Conn, show, column, source, target string) error {
	result, err := showCreateRow(ctx, conn, show)
	if err != nil {
		return err
	}
	create, ok := result[column]
	if !ok || create == "" {
		return fmt.Errorf("%s returned no %q (missing privilege?)", show, column)
	}
	create = rewriteSchema(definerClause.ReplaceAllString(create, ""), source, target)

	if mode, ok := result["sql_mode"]; ok {
		if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode = ?", mode); err != nil {
			return fmt.Errorf("set sql_mode: %w", err)
		}
	}
	_, err = conn.ExecContext(ctx, create)
	return err
}

// showCreate runs a SHOW CREATE statement and returns one named column.
func showCreate(ctx context.Context, conn *sql.Conn, show, column string) (string, error) {
	result, err := showCreateRow(ctx, conn, show)
	if err != nil {
		return "", err
	}
	create, ok := result[column]
	if !ok || create == "" {
		return "", fmt.Errorf("%s returned no %q", show, column)
	}
	return create, nil
}

// showCreateRow runs a SHOW CREATE statement, whose result has a different
// set of columns for each object kind, and returns the row by column name.
func showCreateRow(ctx context.Context, conn *sql.Conn, show string) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, show)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", show, err)
	}
	defer func() { _ = rows.Close() }() // read fully below; nothing left to report
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", show, err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", show, err)
		}
		return nil, fmt.Errorf("%s returned no rows", show)
	}
	values := make([]sql.NullString, len(columns))
	ptrs := make([]any, len(columns))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("%s: %w", show, err)
	}
	result := make(map[string]string, len(columns))
	for i, c := range columns {
		result[c] = values[i].String
	}
	return result, rows.Err()
}

// insertableColumns returns, per table, the columns rows can be inserted
// into (everything but generated columns), in table order.
func insertableColumns(ctx context.Context, conn *sql.Conn, schema string) (map[string][]string, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND EXTRA NOT LIKE '%GENERATED%'
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, schema)
	if err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	byTable := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, errors.Join(fmt.Errorf("list columns: %w", err), rows.Close())
		}
		byTable[table] = append(byTable[table], column)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	return byTable, nil
}

// listNames runs a query whose rows each hold one name.
func listNames(ctx context.Context, conn *sql.Conn, query string, args ...any) ([]string, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		names = append(names, name)
	}
	return names, errors.Join(rows.Err(), rows.Close())
}

func withoutBookkeeping(tables []string) []string {
	kept := tables[:0]
	for _, t := range tables {
		if !strings.HasPrefix(t, bookkeepingPrefix) {
			kept = append(kept, t)
		}
	}
	return kept
}

// rewriteSchema points schema-qualified references in a definition at the
// target database instead of the source. SHOW CREATE output always quotes
// identifiers, so only the backticked form is rewritten.
func rewriteSchema(definition, source, target string) string {
	return strings.ReplaceAll(definition, quoteIdentifier(source)+".", quoteIdentifier(target)+".")
}

func qualify(schema, object string) string {
	return quoteIdentifier(schema) + "." + quoteIdentifier(object)
}
