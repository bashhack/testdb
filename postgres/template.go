package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bashhack/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SetupFunc populates a template database. It receives a DSN for the freshly
// created, empty database and loads whatever the template should contain:
// schema, fixtures, seed data. See SQLFiles, LoadSQLFile and Compose for
// ready-made setup functions; a migration-tool adapter's Migrate method
// (for example goose.Dir("./migrations").Migrate) is one as well.
//
// SetupFunc is testdb.MigratorFunc, so anything that prepares a template
// also works per test through testdb.WithMigrator.
type SetupFunc = testdb.MigratorFunc

// Template is this process's handle on a template database obtained from
// EnsureTemplate. It keeps one connection to the admin database open for as
// long as it lives; that connection holds the lock which tells other
// processes the template is in use. Call Release when the process is done.
type Template struct {
	name     string
	provider *PostgresProvider
	keep     bool
	released bool
}

// Name returns the template database's name, for use with testdb.WithTemplate.
func (t *Template) Name() string { return t.name }

// Option returns the testdb.Option that makes a test database a clone of
// this template, so tests can write postgres.Setup(t, tpl.Option()).
func (t *Template) Option() testdb.Option { return testdb.WithTemplate(t.name) }

// TemplateOption configures EnsureTemplate.
type TemplateOption func(*templateConfig)

type templateConfig struct {
	adminDSN    string
	fingerprint string
	keep        bool
}

// WithFingerprint identifies the template's content, typically a hash of the
// files it is built from (see HashFiles). EnsureTemplate reuses an existing
// template whose fingerprint matches and rebuilds one whose fingerprint
// differs, so templates can safely outlive a test run (see WithKeepTemplate).
// Without a fingerprint an existing template is always reused as it is.
func WithFingerprint(fingerprint string) TemplateOption {
	return func(c *templateConfig) {
		c.fingerprint = fingerprint
	}
}

// WithKeepTemplate leaves the template in place when the last process
// releases it, so the next run can reuse it instead of rebuilding. Pair it
// with WithFingerprint, otherwise a kept template is never refreshed.
func WithKeepTemplate() TemplateOption {
	return func(c *templateConfig) {
		c.keep = true
	}
}

// WithTemplateAdminDSN overrides the admin connection string used to create
// and manage the template. By default it is discovered the same way as for
// test databases (TEST_DATABASE_URL, then DATABASE_URL, then the local
// default). Every process sharing a template must use the same admin
// database, because that is where the coordinating locks live.
func WithTemplateAdminDSN(dsn string) TemplateOption {
	return func(c *templateConfig) {
		c.adminDSN = dsn
	}
}

// EnsureTemplate makes sure the named template database exists and marks it
// as in use by this process, returning a handle to Release when done.
// Tests then clone it with testdb.WithTemplate(name).
//
// It is safe to call from several test packages or CI jobs at once against
// the same server. The first process to arrive builds the template with
// setup; the others wait for it and reuse it; the last one to Release drops
// it. A process that crashes drops out of the count automatically, because
// its lock dies with its connection. Templates are built under a temporary
// name and renamed into place, so a half-built template is never visible to
// another process.
//
// The usual place to call it is TestMain:
//
//	func TestMain(m *testing.M) {
//	    ctx := context.Background()
//	    tpl, err := postgres.EnsureTemplate(ctx, "us_region",
//	        postgres.LoadSQLFile("fixtures/us_region.sql"))
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    code := m.Run()
//	    if err := tpl.Release(ctx); err != nil {
//	        log.Print(err)
//	    }
//	    os.Exit(code)
//	}
//
// A ctx with a deadline bounds the whole operation, including waiting for
// another process's build to finish.
func EnsureTemplate(ctx context.Context, name string, setup SetupFunc, opts ...TemplateOption) (*Template, error) {
	if err := validateTemplateName(name); err != nil {
		return nil, fmt.Errorf("postgres.EnsureTemplate: %w", err)
	}
	var cfg templateConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	provider := &PostgresProvider{}
	if err := provider.Initialize(ctx, testdb.Config{AdminDSNOverride: cfg.adminDSN}); err != nil {
		return nil, fmt.Errorf("postgres.EnsureTemplate %q: %w", name, err)
	}
	tpl, err := provider.ensureTemplate(ctx, name, setup, cfg)
	if err != nil {
		// Closing the connection also releases any lock still held on it.
		err = errors.Join(err, provider.Cleanup(context.WithoutCancel(ctx)))
		return nil, fmt.Errorf("postgres.EnsureTemplate %q: %w", name, err)
	}
	return tpl, nil
}

// ensureTemplate does the work of EnsureTemplate under the build lock.
func (p *PostgresProvider) ensureTemplate(ctx context.Context, name string, setup SetupFunc, cfg templateConfig) (*Template, error) {
	if err := p.lockBuild(ctx, name); err != nil {
		return nil, err
	}
	tpl, err := p.ensureTemplateLocked(ctx, name, setup, cfg)
	// Failing to release the build lock would stall every other process
	// sharing this template, so it is an error even when the rest succeeded;
	// the caller then closes the connection, which releases the lock.
	if unlockErr := p.unlockBuild(context.WithoutCancel(ctx), name); unlockErr != nil {
		return nil, errors.Join(err, unlockErr)
	}
	return tpl, err
}

func (p *PostgresProvider) ensureTemplateLocked(ctx context.Context, name string, setup SetupFunc, cfg templateConfig) (*Template, error) {
	info, err := p.templateInfo(ctx, name)
	if err != nil {
		return nil, err
	}
	switch {
	case !info.exists:
		if err := p.buildTemplate(ctx, name, setup, cfg.fingerprint); err != nil {
			return nil, err
		}
	case !info.isTemplate:
		return nil, fmt.Errorf("database %q exists but is not a template; drop it or choose another name", name)
	case cfg.fingerprint != "" && info.fingerprint != cfg.fingerprint:
		// Stale content. Rebuild only if no other process is using it.
		locked, err := p.tryLockUseExclusive(ctx, name)
		if err != nil {
			return nil, err
		}
		if !locked {
			return nil, fmt.Errorf("template %q has a different fingerprint and is in use by another process; let that run finish first", name)
		}
		dropErr := p.dropTemplate(ctx, name)
		unlockErr := p.unlockUseExclusive(context.WithoutCancel(ctx), name)
		if err := errors.Join(dropErr, unlockErr); err != nil {
			return nil, fmt.Errorf("replace stale template: %w", err)
		}
		if err := p.buildTemplate(ctx, name, setup, cfg.fingerprint); err != nil {
			return nil, err
		}
	}

	if err := p.lockUseShared(ctx, name); err != nil {
		return nil, err
	}
	return &Template{name: name, provider: p, keep: cfg.keep}, nil
}

// Release ends this process's use of the template. If no other process is
// still using it, and WithKeepTemplate was not given, the template is
// dropped. It is safe to call more than once.
func (t *Template) Release(ctx context.Context) error {
	if t.released {
		return nil
	}
	t.released = true

	err := t.provider.release(ctx, t.name, t.keep)
	// Closing the connection releases whatever locks this process still
	// holds, which is how the other processes learn it is gone.
	err = errors.Join(err, t.provider.Cleanup(context.WithoutCancel(ctx)))
	if err != nil {
		return fmt.Errorf("postgres: release template %q: %w", t.name, err)
	}
	return nil
}

func (p *PostgresProvider) release(ctx context.Context, name string, keep bool) error {
	if err := p.unlockUseShared(ctx, name); err != nil {
		return err
	}
	if keep {
		return nil
	}
	return p.dropIfLastOut(ctx, name)
}

// dropIfLastOut drops the template if no process is using it any more.
func (p *PostgresProvider) dropIfLastOut(ctx context.Context, name string) error {
	if err := p.lockBuild(ctx, name); err != nil {
		return err
	}
	err := p.dropIfLastOutLocked(ctx, name)
	return errors.Join(err, p.unlockBuild(context.WithoutCancel(ctx), name))
}

func (p *PostgresProvider) dropIfLastOutLocked(ctx context.Context, name string) error {
	// The exclusive lock is only granted once every shared holder is gone,
	// so getting it means no process is using the template.
	last, err := p.tryLockUseExclusive(ctx, name)
	if err != nil {
		return err
	}
	if !last {
		return nil
	}
	defer func() { err = errors.Join(err, p.unlockUseExclusive(context.WithoutCancel(ctx), name)) }()

	// Two processes can both get here: one that dropped the template and
	// closed, and one that had let go of its shared lock and was waiting
	// for the build lock. The second finds the template already gone.
	info, err := p.templateInfo(ctx, name)
	if err != nil || !info.exists {
		return err
	}
	return p.dropTemplate(ctx, name)
}

// TemplateExists reports whether a database with the given name exists.
// This is a debugging aid; EnsureTemplate handles existence itself.
func TemplateExists(ctx context.Context, name string, opts ...TemplateOption) (bool, error) {
	var cfg templateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	provider := &PostgresProvider{}
	if err := provider.Initialize(ctx, testdb.Config{AdminDSNOverride: cfg.adminDSN}); err != nil {
		return false, fmt.Errorf("postgres.TemplateExists: %w", err)
	}
	exists, err := provider.TemplateExists(ctx, name)
	if err := errors.Join(err, provider.Cleanup(context.WithoutCancel(ctx))); err != nil {
		return false, fmt.Errorf("postgres.TemplateExists: %w", err)
	}
	return exists, nil
}

// buildTemplate creates the template under a temporary name, runs setup,
// then renames it into place and marks it as a template, so other processes
// only ever see a complete template or none at all.
func (p *PostgresProvider) buildTemplate(ctx context.Context, name string, setup SetupFunc, fingerprint string) error {
	tmp, err := buildName(name)
	if err != nil {
		return err
	}
	if err := p.CreateDatabase(ctx, tmp); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	// discard removes the half-built database after a failure, reporting
	// its own problems alongside the failure that caused it.
	discard := func(cause error) error {
		c := context.WithoutCancel(ctx)
		return errors.Join(cause, p.TerminateConnections(c, tmp), p.DropDatabase(c, tmp))
	}

	dsn, err := p.BuildDSN(tmp)
	if err != nil {
		return discard(err)
	}
	if setup != nil {
		if err := setup(ctx, dsn); err != nil {
			return discard(fmt.Errorf("set up template: %w", err))
		}
	}
	// Disallow and terminate connections: both the rename and later clones
	// need the database free of sessions.
	if err := p.TerminateConnections(ctx, tmp); err != nil {
		return discard(err)
	}

	quotedTmp := pgx.Identifier{tmp}.Sanitize()
	quotedName := pgx.Identifier{name}.Sanitize()
	err = retryWhileInUse(ctx, func() error {
		_, err := p.conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", quotedTmp, quotedName))
		return err
	})
	if err != nil {
		return discard(fmt.Errorf("rename template into place: %w", err))
	}

	// From here on the database has its final name; on failure drop it by
	// that name so a broken template is not left behind.
	if _, err := p.conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s IS_TEMPLATE true", quotedName)); err != nil {
		return errors.Join(fmt.Errorf("mark as template: %w", err),
			p.DropDatabase(context.WithoutCancel(ctx), name))
	}
	if fingerprint != "" {
		literal, err := p.conn.PgConn().EscapeString(fingerprint)
		if err == nil {
			_, err = p.conn.Exec(ctx, fmt.Sprintf("COMMENT ON DATABASE %s IS '%s'", quotedName, literal))
		}
		if err != nil {
			return errors.Join(fmt.Errorf("record fingerprint: %w", err),
				p.dropTemplate(context.WithoutCancel(ctx), name))
		}
	}
	return nil
}

// dropTemplate drops a template database. Postgres refuses to drop a
// database while IS_TEMPLATE is set, which is the point of setting it.
func (p *PostgresProvider) dropTemplate(ctx context.Context, name string) error {
	quotedName := pgx.Identifier{name}.Sanitize()
	if _, err := p.conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s IS_TEMPLATE false", quotedName)); err != nil {
		return fmt.Errorf("unmark template: %w", err)
	}
	return p.DropDatabase(ctx, name)
}

type templateInfo struct {
	exists      bool
	isTemplate  bool
	fingerprint string
}

// templateInfo looks up a database by name along with its template flag and
// the fingerprint recorded in its comment.
func (p *PostgresProvider) templateInfo(ctx context.Context, name string) (templateInfo, error) {
	var info templateInfo
	err := p.conn.QueryRow(ctx, `
		SELECT d.datistemplate, COALESCE(s.description, '')
		FROM pg_database d
		LEFT JOIN pg_shdescription s ON s.objoid = d.oid AND s.classoid = 'pg_database'::regclass
		WHERE d.datname = $1`, name).Scan(&info.isTemplate, &info.fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return templateInfo{}, nil
	}
	if err != nil {
		return templateInfo{}, fmt.Errorf("look up template: %w", err)
	}
	info.exists = true
	return info, nil
}

// Advisory locks coordinate processes sharing a template. They are
// session-level, so a process that dies releases them with its connection.
//
//   - build: exclusive, held briefly while a template is checked, created
//     or dropped.
//   - use: shared by every process using the template for its lifetime;
//     taken exclusively only to test whether anyone else is still using it.
//
// Every lock is taken in the order build then use, so there is no deadlock.
// Keys are computed here rather than with hashtext() so they can be looked
// up in pg_locks (see templateLockKey).
const (
	lockClassBuild int32 = 0x54534442 // "TSDB"
	lockClassUse   int32 = 0x54534455 // "TSDU"
)

// templateLockKey returns the advisory lock key for a template name. It is
// kept non-negative so it compares directly with pg_locks.objid.
func templateLockKey(name string) int32 {
	h := fnv.New32a()
	h.Write([]byte(name))
	return int32(h.Sum32() & 0x7fffffff)
}

func (p *PostgresProvider) lockBuild(ctx context.Context, name string) error {
	return p.advisoryLock(ctx, "pg_advisory_lock", lockClassBuild, name)
}

func (p *PostgresProvider) unlockBuild(ctx context.Context, name string) error {
	return p.advisoryUnlock(ctx, "pg_advisory_unlock", lockClassBuild, name)
}

func (p *PostgresProvider) lockUseShared(ctx context.Context, name string) error {
	return p.advisoryLock(ctx, "pg_advisory_lock_shared", lockClassUse, name)
}

func (p *PostgresProvider) unlockUseShared(ctx context.Context, name string) error {
	return p.advisoryUnlock(ctx, "pg_advisory_unlock_shared", lockClassUse, name)
}

func (p *PostgresProvider) tryLockUseExclusive(ctx context.Context, name string) (bool, error) {
	var locked bool
	err := p.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", lockClassUse, templateLockKey(name)).Scan(&locked)
	if err != nil {
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	return locked, nil
}

func (p *PostgresProvider) unlockUseExclusive(ctx context.Context, name string) error {
	return p.advisoryUnlock(ctx, "pg_advisory_unlock", lockClassUse, name)
}

// advisoryLock blocks until the lock is granted. fn is one of the
// pg_advisory_lock* functions, never user input.
func (p *PostgresProvider) advisoryLock(ctx context.Context, fn string, class int32, name string) error {
	if _, err := p.conn.Exec(ctx, "SELECT "+fn+"($1, $2)", class, templateLockKey(name)); err != nil {
		return fmt.Errorf("%s: %w", fn, err)
	}
	return nil
}

// advisoryUnlock releases a lock this session holds. Postgres reports false
// when the session did not hold it, which would mean the lock sequence is
// wrong somewhere, so that is an error too.
func (p *PostgresProvider) advisoryUnlock(ctx context.Context, fn string, class int32, name string) error {
	var released bool
	if err := p.conn.QueryRow(ctx, "SELECT "+fn+"($1, $2)", class, templateLockKey(name)).Scan(&released); err != nil {
		return fmt.Errorf("%s: %w", fn, err)
	}
	if !released {
		return fmt.Errorf("%s: lock was not held by this session", fn)
	}
	return nil
}

// retryWhileInUse retries an operation that fails with SQLSTATE 55006
// ("database is being accessed by other users"), which can happen briefly
// after pg_terminate_backend while the terminated sessions are still
// winding down.
func retryWhileInUse(ctx context.Context, op func() error) error {
	var err error
	for attempt := range 5 {
		err = op()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55006" {
			return err
		}
		select {
		case <-time.After(time.Duration(10<<(attempt*2)) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// maxTemplateNameLen leaves room for the "_build_xxxxxxxx" suffix within
// Postgres's 63-byte identifier limit, so a build name is never truncated.
const maxTemplateNameLen = 63 - len("_build_") - 8

func validateTemplateName(name string) error {
	if name == "" {
		return errors.New("template name is empty")
	}
	if len(name) > maxTemplateNameLen {
		return fmt.Errorf("template name %q is %d bytes; the maximum is %d", name, len(name), maxTemplateNameLen)
	}
	return nil
}

func buildName(name string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate build name: %w", err)
	}
	return name + "_build_" + hex.EncodeToString(b[:]), nil
}

// HashFiles returns a fingerprint of the given files and directories, from
// their relative paths and contents. Directories are walked recursively in
// sorted order, so the result is stable. Use it with WithFingerprint:
//
//	fp, err := postgres.HashFiles("migrations", "fixtures/us.sql")
//	...
//	tpl, err := postgres.EnsureTemplate(ctx, "us_region", setup,
//	    postgres.WithFingerprint(fp), postgres.WithKeepTemplate())
func HashFiles(paths ...string) (string, error) {
	h := sha256.New()
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel == "." {
				rel = filepath.Base(root)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			// hash.Hash writes never fail, by contract
			h.Write([]byte(filepath.ToSlash(rel)))
			h.Write([]byte{0})
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			h.Write([]byte{0})
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", root, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LoadSQLFile returns a setup function that runs one SQL file against the
// database, for SQL dumps and fixture data. It also works per test:
//
//	postgres.EnsureTemplate(ctx, "fixtures",
//	    postgres.LoadSQLFile("fixtures/seed_data.sql"))
//	postgres.Setup(t, testdb.WithMigrator(postgres.LoadSQLFile("schema.sql")))
func LoadSQLFile(path string) SetupFunc {
	return func(ctx context.Context, dsn string) error {
		return execSQLFiles(ctx, dsn, []string{path})
	}
}

// SQLFiles returns a setup function that runs every .sql file in dir, in
// lexical order, against the database: the simplest way to apply a schema
// without a migration tool. Name files so they sort the way they should run
// (001_users.sql, 002_orders.sql). The directory is read when the function
// runs, not when it is created.
//
//	postgres.Setup(t, testdb.WithMigrator(postgres.SQLFiles("./schema")))
func SQLFiles(dir string) SetupFunc {
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

// execSQLFiles runs the files in order on one connection. Every file is read
// before connecting, so a missing file fails without touching the database.
func execSQLFiles(ctx context.Context, dsn string, paths []string) error {
	contents := make([]string, len(paths))
	for i, path := range paths {
		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read SQL file %s: %w", path, err)
		}
		contents[i] = string(sqlBytes)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	var execErr error
	for i, sql := range contents {
		if _, err := conn.Exec(ctx, sql); err != nil {
			execErr = fmt.Errorf("execute SQL from %s: %w", paths[i], err)
			break
		}
	}
	closeErr := conn.Close(ctx)
	if execErr != nil {
		return execErr
	}
	if closeErr != nil {
		return fmt.Errorf("close connection: %w", closeErr)
	}
	return nil
}

// Compose runs several setup functions in order, stopping at the first
// error. Nil functions are skipped.
//
//	postgres.EnsureTemplate(ctx, "full",
//	    postgres.Compose(
//	        goose.Dir("./migrations").Migrate,
//	        postgres.LoadSQLFile("fixtures/seed.sql"),
//	    ))
func Compose(setupFuncs ...SetupFunc) SetupFunc {
	return func(ctx context.Context, dsn string) error {
		for _, fn := range setupFuncs {
			if fn == nil {
				continue
			}
			if err := fn(ctx, dsn); err != nil {
				return err
			}
		}
		return nil
	}
}
