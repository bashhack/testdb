// Package mysql provides isolated MySQL databases for tests: each test gets
// its own database, created with CREATE DATABASE and dropped when the test
// ends, so tests can run in parallel against one server.
//
//	import (
//	    "github.com/bashhack/testdb"
//	    "github.com/bashhack/testdb/migrate/goose"
//	    "github.com/bashhack/testdb/mysql"
//	)
//
//	func TestOrders(t *testing.T) {
//	    db := mysql.Setup(t, testdb.WithMigrator(goose.Dir("./migrations")))
//	    // db is a *sql.DB on a database nobody else touches; cleanup is automatic
//	}
//
// The admin connection string is discovered from TEST_MYSQL_URL, then
// TEST_DATABASE_URL, then DATABASE_URL, falling back to DefaultAdminDSN. It
// may be in go-sql-driver's native form (user:pass@tcp(host:port)/db?params)
// or a mysql://user:pass@host:port/db?params URL. The user needs CREATE and
// DROP DATABASE and the right to kill other sessions (CONNECTION_ADMIN).
//
// Templates are not available for MySQL yet; see testdb.TemplateProvider.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashhack/testdb"
	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	// AdminDSNEnv is the environment variable this provider checks first for
	// its admin connection string, before the shared TEST_DATABASE_URL and
	// DATABASE_URL. Set it when a project tests against more than one
	// database server.
	AdminDSNEnv = "TEST_MYSQL_URL"

	// DefaultAdminDSN is used when no override or environment variable is set.
	DefaultAdminDSN = "root:root@tcp(localhost:3306)/?parseTime=true"

	// lockWaitTimeout bounds how long DROP DATABASE waits for metadata locks
	// held by sessions that could not be killed. MySQL's default is a year,
	// which would turn a leaked transaction into a hung test run.
	lockWaitTimeout = 5 * time.Second
)

// MySQL error numbers this package reacts to.
const (
	errLockWaitTimeout = 1205 // ER_LOCK_WAIT_TIMEOUT
	errUnknownThread   = 1094 // ER_NO_SUCH_THREAD: the session ended on its own
)

// Provider implements testdb.Provider for MySQL. It does not implement
// testdb.TemplateProvider: MySQL has no way to clone one database from
// another, so WithTemplate is refused with testdb.ErrTemplatesNotSupported.
type Provider struct {
	db          *sql.DB
	conn        *sql.Conn // one session, so SET SESSION settings stick
	adminDSN    string
	adminConfig *mysqldriver.Config
}

var (
	_ testdb.Provider         = (*Provider)(nil)
	_ testdb.InspectCommander = (*Provider)(nil)
)

// Initialize resolves the admin DSN and opens the admin session.
func (p *Provider) Initialize(ctx context.Context, cfg testdb.Config) error {
	raw := testdb.ResolveAdminDSN(cfg, DefaultAdminDSN, AdminDSNEnv)
	config, err := parseAdminDSN(raw)
	if err != nil {
		if strings.HasPrefix(strings.ToLower(raw), "postgres") {
			// The shared TEST_DATABASE_URL points at PostgreSQL; the usual
			// case in a project that tests against both databases
			return fmt.Errorf("parse admin DSN: it is a PostgreSQL URL; set %s to the MySQL server (%w)", AdminDSNEnv, err)
		}
		return fmt.Errorf("parse admin DSN: %w", err)
	}
	p.adminConfig = config
	p.adminDSN = config.FormatDSN()

	db, err := sql.Open("mysql", p.adminDSN)
	if err != nil {
		return fmt.Errorf("open admin connection: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("connect to admin database: %w", err), db.Close())
	}
	// Bound how long a drop can wait on locks (see lockWaitTimeout)
	seconds := int(lockWaitTimeout / time.Second)
	if _, err := conn.ExecContext(ctx, "SET SESSION lock_wait_timeout = "+strconv.Itoa(seconds)); err != nil {
		return errors.Join(fmt.Errorf("set lock_wait_timeout: %w", err), conn.Close(), db.Close())
	}
	p.db, p.conn = db, conn
	return nil
}

// CreateDatabase creates an empty database with the server's default
// character set and collation.
func (p *Provider) CreateDatabase(ctx context.Context, name string) error {
	if _, err := p.conn.ExecContext(ctx, "CREATE DATABASE "+quoteIdentifier(name)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// DropDatabase drops a database if it exists. Unlike PostgreSQL, MySQL does
// not refuse while other sessions are connected; it waits for the metadata
// locks they hold. TerminateConnections first, so the wait is short, and
// lockWaitTimeout so it is never long.
func (p *Provider) DropDatabase(ctx context.Context, name string) error {
	_, err := p.conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(name))
	if errNumber(err) == errLockWaitTimeout {
		return fmt.Errorf("drop database: sessions still held locks on %s after %s: %w", name, lockWaitTimeout, err)
	}
	if err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

// TerminateConnections kills every other session whose current database is
// the given one. Sessions that use the database only through qualified
// names (other_db.table) are not found this way; the pools this package
// creates always connect to the database directly.
func (p *Provider) TerminateConnections(ctx context.Context, name string) error {
	rows, err := p.conn.QueryContext(ctx,
		"SELECT id FROM information_schema.PROCESSLIST WHERE db = ? AND id <> CONNECTION_ID()", name)
	if err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return errors.Join(fmt.Errorf("list connections: %w", err), rows.Close())
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list connections: %w", err)
	}

	for _, id := range ids {
		// KILL takes no placeholders; id is a number we just read
		_, err := p.conn.ExecContext(ctx, "KILL CONNECTION "+strconv.FormatUint(id, 10))
		if err != nil && errNumber(err) != errUnknownThread {
			return fmt.Errorf("kill connection %d: %w", id, err)
		}
	}
	return nil
}

// BuildDSN returns a DSN for the given database that carries every setting
// of the admin DSN (credentials, address, parseTime, TLS, timeouts).
func (p *Provider) BuildDSN(dbName string) (string, error) {
	if p.adminConfig == nil {
		return "", errors.New("provider not initialized")
	}
	config := p.adminConfig.Clone()
	config.DBName = dbName
	return config.FormatDSN(), nil
}

// ResolvedAdminDSN returns the admin DSN in use, in native form.
func (p *Provider) ResolvedAdminDSN() string {
	return p.adminDSN
}

// Cleanup closes the admin session.
func (p *Provider) Cleanup(ctx context.Context) error {
	var errs []error
	if p.conn != nil {
		errs = append(errs, p.conn.Close())
		p.conn = nil
	}
	if p.db != nil {
		errs = append(errs, p.db.Close())
		p.db = nil
	}
	return errors.Join(errs...)
}

// InspectCommand returns a mysql command line that opens the database, for
// the kept-database log (see testdb.WithKeepOnFailure).
func (p *Provider) InspectCommand(dsn string) string {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return dsn
	}
	args := []string{"mysql"}
	if config.Net == "unix" {
		args = append(args, "--socket="+shellQuote(config.Addr))
	} else {
		host, port, err := net.SplitHostPort(config.Addr)
		if err != nil {
			host = config.Addr
		}
		args = append(args, "--host="+shellQuote(host))
		if port != "" {
			args = append(args, "--port="+port)
		}
	}
	if config.User != "" {
		args = append(args, "--user="+shellQuote(config.User))
	}
	if config.Passwd != "" {
		args = append(args, "--password="+shellQuote(config.Passwd))
	}
	if config.DBName != "" {
		args = append(args, shellQuote(config.DBName))
	}
	return strings.Join(args, " ")
}

// SQLDBInitializer is the default initializer: a *sql.DB on the test
// database with pool settings suited to tests.
type SQLDBInitializer struct {
	// ConfigModifier can adjust the pool after it is opened and before it is
	// pinged, for example to change SetMaxOpenConns.
	ConfigModifier func(*sql.DB)
}

// InitializeTestDatabase opens and pings a *sql.DB for the test database.
func (i *SQLDBInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Hour)
	if i.ConfigModifier != nil {
		i.ConfigModifier(db)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping database: %w", err), db.Close())
	}
	return db, nil
}

// Setup creates an isolated MySQL database, runs the configured migrator,
// and returns a *sql.DB on it. Cleanup is registered with t.Cleanup; do not
// close the returned *sql.DB yourself. Calls t.Fatal on any error.
//
//	db := mysql.Setup(t, testdb.WithMigrator(goose.Dir("./migrations")))
func Setup(t testing.TB, opts ...testdb.Option) *sql.DB {
	t.Helper()

	db, err := testdb.New(t, &Provider{}, &SQLDBInitializer{}, opts...)
	if err != nil {
		t.Fatalf("mysql.Setup: %v", err)
	}
	testdb.RunConfiguredMigrations(t, db, "mysql.Setup")
	testdb.RegisterCleanup(t, db)

	return db.Entity().(*sql.DB)
}

// New creates an isolated MySQL database with a custom initializer (sqlx,
// GORM, your own wrapper) and returns the TestDatabase. Cleanup is
// registered with t.Cleanup. Calls t.Fatal on any error.
//
//	db := mysql.New(t, &SqlxInitializer{}, testdb.WithMigrator(goose.Dir("./migrations")))
//	sqlxDB := db.Entity().(*sqlx.DB)
func New(t testing.TB, initializer testdb.DBInitializer, opts ...testdb.Option) *testdb.TestDatabase {
	t.Helper()

	if initializer == nil {
		t.Fatalf("mysql.New: initializer cannot be nil\n" +
			"  Use mysql.Setup() for a ready-to-use *sql.DB\n" +
			"  Use testdb.New() for the low-level API with manual initialization")
	}

	db, err := testdb.New(t, &Provider{}, initializer, opts...)
	if err != nil {
		t.Fatalf("mysql.New: %v", err)
	}
	testdb.RunConfiguredMigrations(t, db, "mysql.New")
	testdb.RegisterCleanup(t, db)

	return db
}

// parseAdminDSN accepts go-sql-driver's native DSN form or a mysql:// URL.
func parseAdminDSN(raw string) (*mysqldriver.Config, error) {
	if strings.HasPrefix(strings.ToLower(raw), "mysql://") {
		native, err := urlToNative(raw)
		if err != nil {
			return nil, err
		}
		raw = native
	}
	return mysqldriver.ParseDSN(raw)
}

// urlToNative rewrites mysql://user:pass@host:port/db?params as
// user:pass@tcp(host:port)/db?params. A missing port means 3306.
func urlToNative(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if u.Host == "" {
		return "", errors.New("mysql:// URL has no host")
	}
	var b strings.Builder
	if u.User != nil {
		b.WriteString(u.User.Username())
		if password, ok := u.User.Password(); ok {
			b.WriteString(":" + password)
		}
		b.WriteString("@")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "3306")
	}
	b.WriteString("tcp(" + host + ")/")
	b.WriteString(strings.TrimPrefix(u.Path, "/"))
	if u.RawQuery != "" {
		b.WriteString("?" + u.RawQuery)
	}
	return b.String(), nil
}

// quoteIdentifier quotes a name for use as a MySQL identifier.
func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// shellQuote single-quotes a value for a shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// errNumber returns the MySQL error number in err, or 0.
func errNumber(err error) uint16 {
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number
	}
	return 0
}
