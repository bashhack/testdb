package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/bashhack/testdb"
)

// Templates for MySQL: a database built once and copied for each test. The
// guarantees are the ones postgres gives (first process builds, others wait
// and reuse, last one out drops, crashes release, a half-built template is
// never used), reached with MySQL's means: GET_LOCK for the build step,
// bookkeeping tables inside the template for the use count and ready
// databases, and liveness checked against the process list.
// See .internal/MYSQL_PROVIDER_DESIGN.md.

// SetupFunc populates a template database; see postgres.SetupFunc. It is
// testdb.MigratorFunc, so a migration adapter's Migrate method is one.
type SetupFunc = testdb.MigratorFunc

// Template is this process's handle on a template database obtained from
// EnsureTemplate. It holds one connection to the server for as long as it
// lives; that connection's id is how other processes know the template is
// in use. Call Release when the process is done.
type Template struct {
	name        string
	provider    *Provider // the long-lived session
	connID      uint64
	serverStart int64
	keep        bool
	readyPrefix string
	pool        *readyPool

	mu       sync.Mutex
	released bool
}

// Name returns the template database's name, for use with testdb.WithTemplate.
func (t *Template) Name() string { return t.name }

// Option returns the testdb.Option that makes a test database a clone of
// this template, so tests can write mysql.Setup(t, tpl.Option()).
func (t *Template) Option() testdb.Option { return testdb.WithTemplate(t.name) }

// Ready reports how many prepared clones are waiting to be taken right now
// (see WithReadyDatabases); 0 when the pool is disabled or has run dry.
func (t *Template) Ready() int {
	if t.pool == nil {
		return 0
	}
	return len(t.pool.ready)
}

// TemplateOption configures EnsureTemplate.
type TemplateOption func(*templateConfig)

type templateConfig struct {
	adminDSN     string
	fingerprint  string
	keep         bool
	ready        int
	readyWorkers int
	readyPrefix  string
}

// WithFingerprint identifies the template's content, typically a hash of the
// files it is built from. EnsureTemplate reuses an existing template whose
// fingerprint matches and rebuilds one whose fingerprint differs, so
// templates can safely outlive a test run (see WithKeepTemplate). Without a
// fingerprint an existing template is always reused as it is.
func WithFingerprint(fingerprint string) TemplateOption {
	return func(c *templateConfig) { c.fingerprint = fingerprint }
}

// WithKeepTemplate leaves the template in place when the last process
// releases it, so the next run can reuse it instead of rebuilding. Pair it
// with WithFingerprint, otherwise a kept template is never refreshed.
func WithKeepTemplate() TemplateOption {
	return func(c *templateConfig) { c.keep = true }
}

// WithTemplateAdminDSN overrides the admin connection string used to create
// and manage the template. By default it is discovered as for test
// databases (TEST_MYSQL_URL, then the shared variables, then the default).
func WithTemplateAdminDSN(dsn string) TemplateOption {
	return func(c *templateConfig) { c.adminDSN = dsn }
}

// WithReadyDatabases sets how many clones of the template this process
// keeps prepared ahead of demand. Cloning a MySQL database costs hundreds of
// milliseconds for a real schema (every table is created again); taking a
// prepared one costs nothing, and the pool refills while tests run. The
// default is runtime.GOMAXPROCS(0), which is also go test's default
// -parallel. 0 disables the pool: every test clones on demand.
//
// The pool keeps up as long as tests take clones no faster than the
// workers (see WithReadyWorkers) can make them. When it runs dry, a test
// clones on demand rather than wait.
func WithReadyDatabases(n int) TemplateOption {
	return func(c *templateConfig) { c.ready = n }
}

// WithReadyWorkers sets how many clones are made in parallel to keep the
// pool full. The server handles concurrent clones well, so the default is
// the pool size.
func WithReadyWorkers(n int) TemplateOption {
	return func(c *templateConfig) { c.readyWorkers = n }
}

// WithReadyPrefix sets the name prefix of prepared databases (default
// "test", the same as test databases get).
func WithReadyPrefix(prefix string) TemplateOption {
	return func(c *templateConfig) { c.readyPrefix = prefix }
}

// registry holds this process's templates by name, so the provider can
// hand a test a database the template prepared in advance.
var registry sync.Map // string -> *Template

// EnsureTemplate makes sure the named template database exists and marks it
// as in use by this process, returning a handle to Release when done.
// Tests then clone it with testdb.WithTemplate(name) or tpl.Option().
//
// It is safe to call from several test packages or CI jobs at once against
// the same server: the first process to arrive builds the template with
// setup, the others wait for it and reuse it, and the last one to Release
// drops it. A process that crashes drops out of the count because its
// connection is gone. A build that crashed half-way is recognised (its
// completeness marker is missing) and redone.
//
//	func TestMain(m *testing.M) {
//	    ctx := context.Background()
//	    tpl, err := mysql.EnsureTemplate(ctx, "schema", goose.Dir("./migrations").Migrate)
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    code := m.Run()
//	    if err := tpl.Release(ctx); err != nil {
//	        log.Print(err)
//	    }
//	    os.Exit(code)
//	}
func EnsureTemplate(ctx context.Context, name string, setup SetupFunc, opts ...TemplateOption) (*Template, error) {
	if err := validateTemplateName(name); err != nil {
		return nil, fmt.Errorf("mysql.EnsureTemplate: %w", err)
	}
	cfg := templateConfig{ready: runtime.GOMAXPROCS(0), readyPrefix: "test"}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.readyWorkers <= 0 {
		cfg.readyWorkers = cfg.ready
	}

	p := &Provider{}
	if err := p.Initialize(ctx, testdb.Config{AdminDSNOverride: cfg.adminDSN}); err != nil {
		return nil, fmt.Errorf("mysql.EnsureTemplate %q: %w", name, err)
	}
	tpl := &Template{name: name, provider: p, keep: cfg.keep, readyPrefix: cfg.readyPrefix}
	if err := tpl.ensure(ctx, setup, cfg); err != nil {
		err = errors.Join(err, p.Cleanup(context.WithoutCancel(ctx)))
		return nil, fmt.Errorf("mysql.EnsureTemplate %q: %w", name, err)
	}
	registry.Store(name, tpl)
	if cfg.ready > 0 {
		pool, err := startReadyPool(tpl, cfg.ready, cfg.readyWorkers)
		if err != nil {
			// The pool is an optimisation; without it tests clone on demand
			log.Printf("testdb: template %q: cannot prepare databases ahead, tests will clone on demand: %v", name, err)
			return tpl, nil
		}
		tpl.pool = pool
	}
	return tpl, nil
}

func (t *Template) ensure(ctx context.Context, setup SetupFunc, cfg templateConfig) error {
	conn := t.provider.conn
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&t.connID); err != nil {
		return fmt.Errorf("connection id: %w", err)
	}
	start, err := serverStartedAt(ctx, conn)
	if err != nil {
		return err
	}
	t.serverStart = start

	if err := lockBuild(ctx, conn, t.name); err != nil {
		return err
	}
	err = t.ensureLocked(ctx, setup, cfg)
	// Failing to release the build lock would stall every other process
	// sharing this template; the caller then closes the session, which
	// releases it.
	return errors.Join(err, unlockBuild(context.WithoutCancel(ctx), conn, t.name))
}

func (t *Template) ensureLocked(ctx context.Context, setup SetupFunc, cfg templateConfig) error {
	conn := t.provider.conn
	info, err := t.info(ctx)
	if err != nil {
		return err
	}
	switch {
	case !info.exists:
		if err := t.build(ctx, setup, cfg.fingerprint); err != nil {
			return err
		}
	case !info.complete:
		// A build that crashed before writing its marker
		if err := t.dropTemplate(ctx); err != nil {
			return fmt.Errorf("remove half-built template: %w", err)
		}
		if err := t.build(ctx, setup, cfg.fingerprint); err != nil {
			return err
		}
	case cfg.fingerprint != "" && info.fingerprint != cfg.fingerprint:
		live, err := t.liveUsers(ctx)
		if err != nil {
			return err
		}
		if live > 0 {
			return fmt.Errorf("template %q has a different fingerprint and is in use by %d other process(es); let that run finish first", t.name, live)
		}
		if err := t.dropTemplate(ctx); err != nil {
			return fmt.Errorf("replace stale template: %w", err)
		}
		if err := t.build(ctx, setup, cfg.fingerprint); err != nil {
			return err
		}
	default:
		// Reusing: tidy up after processes that died with prepared databases
		if err := t.sweepDeadReady(ctx); err != nil {
			return err
		}
	}

	_, err = conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (connection_id, server_started_at) VALUES (?, ?)",
		qualify(t.name, bookkeepingPrefix+"users")), t.connID, t.serverStart)
	if err != nil {
		return fmt.Errorf("register as a user of the template: %w", err)
	}
	return nil
}

// build creates the template under its final name (MySQL cannot rename a
// database), runs setup, creates the bookkeeping tables, and writes the
// completeness marker last. On failure the database is dropped.
func (t *Template) build(ctx context.Context, setup SetupFunc, fingerprint string) (err error) {
	conn := t.provider.conn
	if err := t.provider.CreateDatabase(ctx, t.name); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, t.provider.DropDatabase(context.WithoutCancel(ctx), t.name))
		}
	}()

	if setup != nil {
		dsn, err := t.provider.BuildDSN(t.name)
		if err != nil {
			return err
		}
		if err := setup(ctx, dsn); err != nil {
			return fmt.Errorf("set up template: %w", err)
		}
	}

	for _, stmt := range []string{
		fmt.Sprintf(`CREATE TABLE %s (
			id TINYINT PRIMARY KEY,
			fingerprint VARCHAR(255) NOT NULL,
			complete TINYINT NOT NULL,
			built_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`, qualify(t.name, bookkeepingPrefix+"info")),
		fmt.Sprintf(`CREATE TABLE %s (
			connection_id BIGINT UNSIGNED NOT NULL,
			server_started_at BIGINT NOT NULL,
			PRIMARY KEY (connection_id, server_started_at)
		)`, qualify(t.name, bookkeepingPrefix+"users")),
		fmt.Sprintf(`CREATE TABLE %s (
			db_name VARCHAR(64) PRIMARY KEY,
			connection_id BIGINT UNSIGNED NOT NULL,
			server_started_at BIGINT NOT NULL
		)`, qualify(t.name, bookkeepingPrefix+"ready")),
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create bookkeeping table: %w", err)
		}
	}
	// The marker: anything that sees this row sees a finished template
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (id, fingerprint, complete) VALUES (1, ?, 1)",
		qualify(t.name, bookkeepingPrefix+"info")), fingerprint); err != nil {
		return fmt.Errorf("mark template complete: %w", err)
	}
	return nil
}

// Release ends this process's use of the template: prepared databases this
// process still holds are dropped, and if no other process is using the
// template (and WithKeepTemplate was not given) the template is dropped
// too, along with anything left by processes that died. It is safe to call
// more than once.
func (t *Template) Release(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return nil
	}
	t.released = true
	registry.CompareAndDelete(t.name, t)

	var errs []error
	if t.pool != nil {
		errs = append(errs, t.pool.stop())
	}
	errs = append(errs, t.release(ctx))
	// Closing the session releases the GET_LOCK and ends this connection id,
	// which is how other processes see this one leave
	errs = append(errs, t.provider.Cleanup(context.WithoutCancel(ctx)))
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("mysql: release template %q: %w", t.name, err)
	}
	return nil
}

func (t *Template) release(ctx context.Context) error {
	conn := t.provider.conn
	if err := lockBuild(ctx, conn, t.name); err != nil {
		return err
	}
	err := t.releaseLocked(ctx)
	return errors.Join(err, unlockBuild(context.WithoutCancel(ctx), conn, t.name))
}

func (t *Template) releaseLocked(ctx context.Context) error {
	conn := t.provider.conn
	info, err := t.info(ctx)
	if err != nil {
		return err
	}
	if !info.exists {
		// Already dropped by another last-out process; nothing to do
		return nil
	}
	// Our prepared databases, whether or not the pool managed to drop them
	if err := t.dropReadyOf(ctx, t.connID, t.serverStart); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE connection_id = ? AND server_started_at = ?",
		qualify(t.name, bookkeepingPrefix+"users")), t.connID, t.serverStart); err != nil {
		return fmt.Errorf("deregister: %w", err)
	}
	if t.keep {
		return t.sweepDeadReady(ctx)
	}
	live, err := t.liveUsers(ctx)
	if err != nil {
		return err
	}
	if live > 0 {
		return nil
	}
	return t.dropTemplate(ctx)
}

// dropTemplate drops the template and every prepared database recorded in
// it, whoever made them.
func (t *Template) dropTemplate(ctx context.Context) error {
	conn := t.provider.conn
	names, err := listNames(ctx, conn, "SELECT db_name FROM "+qualify(t.name, bookkeepingPrefix+"ready"))
	if err != nil && errNumber(err) != errNoSuchTable {
		return fmt.Errorf("list prepared databases: %w", err)
	}
	for _, name := range names {
		if err := t.provider.DropDatabase(ctx, name); err != nil {
			return err
		}
	}
	return t.provider.DropDatabase(ctx, t.name)
}

// templateInfo is what one look at a template tells us.
type templateInfo struct {
	exists      bool
	complete    bool
	fingerprint string
}

func (t *Template) info(ctx context.Context) (templateInfo, error) {
	conn := t.provider.conn
	exists, err := t.provider.TemplateExists(ctx, t.name)
	if err != nil || !exists {
		return templateInfo{}, err
	}
	var info templateInfo
	info.exists = true
	var complete int
	err = conn.QueryRowContext(ctx, "SELECT fingerprint, complete FROM "+qualify(t.name, bookkeepingPrefix+"info")+" WHERE id = 1").
		Scan(&info.fingerprint, &complete)
	switch {
	case errors.Is(err, sql.ErrNoRows), errNumber(err) == errNoSuchTable:
		return info, nil // exists, not complete
	case err != nil:
		return templateInfo{}, fmt.Errorf("read template info: %w", err)
	}
	info.complete = complete == 1
	return info, nil
}

// liveUsers counts processes using the template and removes the rows of
// processes that are gone. It must run under the build lock.
func (t *Template) liveUsers(ctx context.Context) (int, error) {
	conn := t.provider.conn
	alive, err := liveConnections(ctx, conn)
	if err != nil {
		return 0, err
	}
	rows, err := conn.QueryContext(ctx, "SELECT connection_id, server_started_at FROM "+qualify(t.name, bookkeepingPrefix+"users"))
	if err != nil {
		return 0, fmt.Errorf("list template users: %w", err)
	}
	type user struct {
		id    uint64
		start int64
	}
	var users []user
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.id, &u.start); err != nil {
			return 0, errors.Join(fmt.Errorf("list template users: %w", err), rows.Close())
		}
		users = append(users, u)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, fmt.Errorf("list template users: %w", err)
	}

	live := 0
	for _, u := range users {
		if alive.has(u.id, u.start) {
			live++
			continue
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE connection_id = ? AND server_started_at = ?",
			qualify(t.name, bookkeepingPrefix+"users")), u.id, u.start); err != nil {
			return 0, fmt.Errorf("remove dead template user: %w", err)
		}
	}
	return live, nil
}

// sweepDeadReady drops prepared databases whose creating process is gone.
// It must run under the build lock.
func (t *Template) sweepDeadReady(ctx context.Context) error {
	conn := t.provider.conn
	alive, err := liveConnections(ctx, conn)
	if err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, "SELECT db_name, connection_id, server_started_at FROM "+qualify(t.name, bookkeepingPrefix+"ready"))
	if err != nil {
		return fmt.Errorf("list prepared databases: %w", err)
	}
	var dead []string
	for rows.Next() {
		var name string
		var id uint64
		var start int64
		if err := rows.Scan(&name, &id, &start); err != nil {
			return errors.Join(fmt.Errorf("list prepared databases: %w", err), rows.Close())
		}
		if !alive.has(id, start) {
			dead = append(dead, name)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list prepared databases: %w", err)
	}
	for _, name := range dead {
		if err := t.forgetReady(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// dropReadyOf drops the prepared databases one connection made.
func (t *Template) dropReadyOf(ctx context.Context, connID uint64, start int64) error {
	names, err := listNames(ctx, t.provider.conn,
		"SELECT db_name FROM "+qualify(t.name, bookkeepingPrefix+"ready")+" WHERE connection_id = ? AND server_started_at = ?", connID, start)
	if err != nil {
		return fmt.Errorf("list prepared databases: %w", err)
	}
	for _, name := range names {
		if err := t.forgetReady(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// forgetReady drops a prepared database and its record.
func (t *Template) forgetReady(ctx context.Context, name string) error {
	if err := t.provider.DropDatabase(ctx, name); err != nil {
		return err
	}
	if _, err := t.provider.conn.ExecContext(ctx, "DELETE FROM "+qualify(t.name, bookkeepingPrefix+"ready")+" WHERE db_name = ?", name); err != nil {
		return fmt.Errorf("forget prepared database %s: %w", name, err)
	}
	return nil
}

// Session-level named locks coordinate the processes sharing a template.
// GET_LOCK names are limited to 64 characters, so the template's name is
// hashed into the lock name.

const errNoSuchTable = 1146 // ER_NO_SUCH_TABLE

func lockName(template string) string {
	sum := sha256.Sum256([]byte(template))
	return "testdb:template:" + hex.EncodeToString(sum[:12])
}

// lockBuild takes the template's build lock, waiting as long as ctx allows.
func lockBuild(ctx context.Context, conn *sql.Conn, template string) error {
	timeout := -1 // forever, as far as MySQL is concerned; ctx still applies
	if deadline, ok := ctx.Deadline(); ok {
		timeout = max(int(time.Until(deadline).Seconds()), 1)
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName(template), timeout).Scan(&got); err != nil {
		return fmt.Errorf("acquire build lock: %w", err)
	}
	if !got.Valid {
		return errors.New("acquire build lock: GET_LOCK returned NULL")
	}
	if got.Int64 != 1 {
		return fmt.Errorf("acquire build lock: another process held it for over %d seconds", timeout)
	}
	return nil
}

// unlockBuild releases the build lock; not holding it is a sequencing bug.
func unlockBuild(ctx context.Context, conn *sql.Conn, template string) error {
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName(template)).Scan(&released); err != nil {
		return fmt.Errorf("release build lock: %w", err)
	}
	if !released.Valid || released.Int64 != 1 {
		return errors.New("release build lock: it was not held by this session")
	}
	return nil
}

// serverStartedAt returns when the server started, as a Unix timestamp.
// Connection ids restart from 1 when the server does, so a user row is only
// trusted if it was written during the current run of the server.
func serverStartedAt(ctx context.Context, conn *sql.Conn) (int64, error) {
	var now int64
	if err := conn.QueryRowContext(ctx, "SELECT UNIX_TIMESTAMP()").Scan(&now); err != nil {
		return 0, fmt.Errorf("server time: %w", err)
	}
	var name, value string
	if err := conn.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Uptime'").Scan(&name, &value); err != nil {
		return 0, fmt.Errorf("server uptime: %w", err)
	}
	uptime, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("server uptime %q: %w", value, err)
	}
	return now - uptime, nil
}

// serverStartTolerance absorbs the second-level rounding of two processes
// computing the server's start time at different moments.
const serverStartTolerance = 5

// liveSet answers whether a (connection id, server start) pair belongs to a
// session that exists right now.
type liveSet struct {
	ids   map[uint64]struct{}
	start int64
}

func (s liveSet) has(id uint64, start int64) bool {
	if start < s.start-serverStartTolerance || start > s.start+serverStartTolerance {
		return false // written during an earlier run of the server
	}
	_, ok := s.ids[id]
	return ok
}

func liveConnections(ctx context.Context, conn *sql.Conn) (liveSet, error) {
	start, err := serverStartedAt(ctx, conn)
	if err != nil {
		return liveSet{}, err
	}
	rows, err := conn.QueryContext(ctx, "SELECT id FROM information_schema.PROCESSLIST")
	if err != nil {
		return liveSet{}, fmt.Errorf("list sessions: %w", err)
	}
	set := liveSet{ids: map[uint64]struct{}{}, start: start}
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return liveSet{}, errors.Join(fmt.Errorf("list sessions: %w", err), rows.Close())
		}
		set.ids[id] = struct{}{}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return liveSet{}, fmt.Errorf("list sessions: %w", err)
	}
	return set, nil
}

// maxTemplateNameLen is MySQL's identifier limit.
const maxTemplateNameLen = 64

func validateTemplateName(name string) error {
	if name == "" {
		return errors.New("template name is empty")
	}
	if len(name) > maxTemplateNameLen {
		return fmt.Errorf("template name %q is %d bytes; the maximum is %d", name, len(name), maxTemplateNameLen)
	}
	return nil
}

// CreateDatabaseFromTemplate hands the test a database cloned from the
// template: one this process prepared in advance if there is one ready
// (see WithReadyDatabases), otherwise a clone made now under the requested
// name. It never waits for the pool.
func (p *Provider) CreateDatabaseFromTemplate(ctx context.Context, requestedName, templateName string) (string, error) {
	if v, ok := registry.Load(templateName); ok {
		if tpl := v.(*Template); tpl.pool != nil {
			if name, ok := tpl.pool.take(); ok {
				if _, err := p.conn.ExecContext(ctx, "DELETE FROM "+qualify(templateName, bookkeepingPrefix+"ready")+" WHERE db_name = ?", name); err != nil {
					// Not ours after all; do not leave it unaccounted for
					return "", errors.Join(fmt.Errorf("claim prepared database %s: %w", name, err),
						p.DropDatabase(context.WithoutCancel(ctx), name))
				}
				return name, nil
			}
		}
	}
	if err := cloneDatabase(ctx, p.adminDSN, templateName, requestedName); err != nil {
		return "", err
	}
	return requestedName, nil
}

// TemplateExists reports whether a database with the given name exists.
func (p *Provider) TemplateExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := p.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check template exists: %w", err)
	}
	return n == 1, nil
}

// TemplateExists reports whether a database with the given name exists.
// This is a debugging aid; EnsureTemplate handles existence itself.
func TemplateExists(ctx context.Context, name string, opts ...TemplateOption) (bool, error) {
	var cfg templateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	p := &Provider{}
	if err := p.Initialize(ctx, testdb.Config{AdminDSNOverride: cfg.adminDSN}); err != nil {
		return false, fmt.Errorf("mysql.TemplateExists: %w", err)
	}
	exists, err := p.TemplateExists(ctx, name)
	if err := errors.Join(err, p.Cleanup(context.WithoutCancel(ctx))); err != nil {
		return false, fmt.Errorf("mysql.TemplateExists: %w", err)
	}
	return exists, nil
}

var _ testdb.TemplateProvider = (*Provider)(nil)
