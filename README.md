# testdb

> True database isolation for Go tests using PostgreSQL's `CREATE DATABASE`. No Docker required.

[![Go Reference](https://pkg.go.dev/badge/github.com/bashhack/testdb.svg)](https://pkg.go.dev/github.com/bashhack/testdb)

## Features

- **True Isolation** - Each test gets its own database
- **Parallel Tests** - Run tests concurrently with `t.Parallel()`
- **Zero Docker** - Uses native PostgreSQL CREATE DATABASE
- **Auto Cleanup** - Databases are dropped automatically
- **Migration Support** - goose, tern and golang-migrate through their Go libraries, or plain SQL files; nothing to install
- **PostgreSQL and MySQL** - The same API and the same guarantees on both; pgxpool for PostgreSQL, database/sql for MySQL

## Quick Start

### Prerequisites

A running PostgreSQL or MySQL server (local installation, managed instance, or remote). testdb connects to it - it doesn't manage the server itself.

### Install

```bash
go get github.com/bashhack/testdb/postgres   # PostgreSQL
go get github.com/bashhack/testdb/mysql      # MySQL
```

### Basic Usage

```go
package myapp_test

import (
    "context"
    "testing"

    "github.com/bashhack/testdb"
    "github.com/bashhack/testdb/migrate/goose"
    "github.com/bashhack/testdb/postgres"
)

func TestUsers(t *testing.T) {
    // Create isolated test database with your migrations
    pool := postgres.Setup(t,
        testdb.WithMigrator(goose.Dir("./migrations")),
    )

    // Use the database - tables from migrations are ready
    _, err := pool.Exec(context.Background(),
        "INSERT INTO users (email) VALUES ($1)", "test@example.com")
    if err != nil {
        t.Fatalf("Insert failed: %v", err)
    }

    // Cleanup is automatic via t.Cleanup()
}
```

That's it! Each test gets an isolated database with your schema ready.

## Why testdb?

testdb uses PostgreSQL's native `CREATE DATABASE` to give each test its own isolated database.

**Complete isolation** - Each test gets an actual database, not a transaction or schema. Test transactions, DDL, concurrent operations - anything your application does in production.

**Simple setup** - Works with your existing PostgreSQL server. No containers to orchestrate, no Docker daemon required.

**True parallelism** - Run hundreds of tests concurrently with `t.Parallel()`. Each test has its own database, eliminating coordination complexity.

**Standard tooling** - Use the migrations you already have, run through goose, tern or golang-migrate's own Go libraries. No binaries on PATH, no version drift between CI and laptops.

**Shared database approach:**
```go
func TestUsers(t *testing.T) {
    // Share a database with all other tests
    db := getSharedTestDB()

    // Manually clean up between tests
    defer truncateTables(db, "users", "orders")

    // Can't use t.Parallel() safely
    // State from other tests can leak
}
```

**testdb approach:**
```go
func TestUsers(t *testing.T) {
    t.Parallel() // Safe - each test has its own database

    pool := postgres.Setup(t,
        testdb.WithMigrator(goose.Dir("./migrations")),
    )
    // Complete isolation - no state leakage possible
}
```

## Configuration

### Environment Variables

Set `TEST_DATABASE_URL` or `DATABASE_URL`:

```bash
export TEST_DATABASE_URL="postgres://user:pass@localhost:5432/postgres"
```

Or use the default: `postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable`

Each database provider also has a variable of its own that wins over the shared ones, so a project that tests against more than one database server can point each provider at the right one: `TEST_POSTGRES_URL` for PostgreSQL, `TEST_MYSQL_URL` for MySQL. The full order is `WithAdminDSN`, the provider's variable, `TEST_DATABASE_URL`, `DATABASE_URL`, then the default.

### Options

```go
pool := postgres.Setup(t,
    testdb.WithMigrator(goose.Dir("./migrations")),
    testdb.WithAdminDSN("postgres://custom:5432/postgres"),
    testdb.WithDBPrefix("myapp_test"),
)
```

Available options:
- `WithMigrator(m)` - How each new database is prepared: a migration-tool adapter, `postgres.SQLFiles(dir)`, or any `func(ctx, dsn) error`
- `WithTemplate(name)` - Clone a template database instead of starting empty (see Templates)
- `WithAdminDSN(dsn)` - Override admin connection string
- `WithDBPrefix(prefix)` - Database name prefix (default: "test")
- `WithKeepOnFailure()` - Keep a failed test's database for inspection (see below)
- `WithVerbose()` - Enable verbose logging for debugging

### Looking at a Failed Test's Database

By default every test database is dropped, pass or fail. When a test fails and you want to see what it left behind, keep its database:

```bash
TESTDB_KEEP_ON_FAILURE=1 go test ./... -run TestCheckout
```

```
    checkout_test.go:42: expected 2 orders, got 1
    testdb: kept database test_1761100000_a1b2c3d4 for inspection:
        psql "postgres://postgres:postgres@localhost:5432/test_1761100000_a1b2c3d4?sslmode=disable"
```

Passing tests still clean up after themselves. The same switch exists in code as `testdb.WithKeepOnFailure()`, and with the low-level `testdb.New` you call `db.Keep()` yourself. On MySQL the log line gives a `mysql` command line instead. Kept databases are yours to drop afterwards; a `WithDBPrefix` makes them easy to find. Leave this off in CI, where nobody will look and the databases would only pile up.

## Advanced Usage

### Built-in Initializers

testdb provides two built-in initializers for PostgreSQL:

#### PoolInitializer (Default)

Creates `*pgxpool.Pool` - recommended for most PostgreSQL applications. Provides full access to PostgreSQL-specific features:

```go
// postgres.Setup() uses PoolInitializer automatically
pool := postgres.Setup(t)

// Or explicitly with postgres.New()
db := postgres.New(t, &postgres.PoolInitializer{})
pool := db.Entity().(*pgxpool.Pool)

// Full pgx capabilities: arrays, JSON, COPY, LISTEN/NOTIFY
pool.QueryRow(ctx, "SELECT ARRAY[1,2,3]").Scan(&arr)
```

#### SqlDbInitializer

Creates `*sql.DB` - use when your application code or dependencies expect database/sql interfaces:

```go
db := postgres.New(t, &postgres.SqlDbInitializer{})
sqlDB := db.Entity().(*sql.DB)

// Standard database/sql operations
sqlDB.QueryRow("SELECT * FROM users WHERE id = $1", 1).Scan(&name)

// Works with libraries that expect *sql.DB
repo := myorm.NewRepository(sqlDB)
```

SqlDbInitializer uses pgx/v5/stdlib under the hood, so you get pgx's PostgreSQL support with database/sql compatibility.

**When to use SqlDbInitializer:**
- Your application uses `*sql.DB`, `*sql.Tx`, or `*sql.Rows`
- You're working with ORMs or libraries that expect `*sql.DB`
- You need database/sql semantics (standard connection pooling, transactions)

**When to use PoolInitializer:**
- You're using pgx directly (recommended for new PostgreSQL projects)
- You need PostgreSQL-specific features (arrays, JSON types, COPY, LISTEN/NOTIFY)
- You want the best performance and feature set

### Custom Initializer

If you need custom database initialization (e.g., using GORM, sqlx):

```go
type MyInitializer struct{}

func (m *MyInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (interface{}, error) {
    // Your custom initialization
    return myapp.InitDB(dsn)
}

func TestAdvanced(t *testing.T) {
    db := postgres.New(t, &MyInitializer{},
        testdb.WithMigrator(goose.Dir("./migrations")),
    )
    myDB := db.Entity().(*myapp.DB)
    // Use your custom DB type
}
```

### Using Just the DSN

If you want full control over connections without an initializer:

```go
provider := &postgres.PostgresProvider{}
db, err := testdb.New(t, provider, nil,
    testdb.WithMigrator(goose.Dir("./migrations")),
)
if err != nil {
    t.Fatalf("Failed to create database: %v", err)
}

// With testdb.New you decide when migrations run
if err := db.RunMigrations(t.Context()); err != nil {
    t.Fatalf("Migrations failed: %v", err)
}

// Use the DSN to create your own connection
myCustomPool := myapp.ConnectDB(db.DSN())
defer myCustomPool.Close()

// Don't forget cleanup
defer db.Close()
```

### Helper Function Pattern

```go
func setupDB(t *testing.T) *pgxpool.Pool {
    t.Helper()
    return postgres.Setup(t,
        testdb.WithMigrator(goose.Dir("./migrations")),
    )
}

func TestSomething(t *testing.T) {
    pool := setupDB(t)
    // Use pool...
}
```

### Testing Without Migrations

For demonstrating isolation mechanics or simple tests:

```go
func TestIsolation(t *testing.T) {
    // Create database without migrations
    pool := postgres.Setup(t)

    // Use for simple queries
    var result int
    pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
}
```

This is useful for testing the library itself, but production applications should use migrations.

## Supported Databases

### PostgreSQL

```go
import "github.com/bashhack/testdb/postgres"

pool := postgres.Setup(t,
    testdb.WithMigrator(goose.Dir("./migrations")),
)
```

### MySQL

```go
import "github.com/bashhack/testdb/mysql"

db := mysql.Setup(t,                       // *sql.DB
    testdb.WithMigrator(goose.Dir("./migrations")),
)
```

The admin connection string may be in go-sql-driver's native form (`user:pass@tcp(host:port)/?parseTime=true`) or a `mysql://` URL, from `TEST_MYSQL_URL` or the shared variables; the default is `root:root@tcp(localhost:3306)/?parseTime=true`. The user needs `CREATE`/`DROP DATABASE` and the right to kill other sessions (`CONNECTION_ADMIN`), which `root` has.

Two things about MySQL that testdb handles for you. `DROP DATABASE` does not refuse while other sessions are connected, it waits for the locks they hold (by default, for a year); cleanup kills the test's sessions first and bounds the wait at five seconds, so a leaked transaction can never hang a test run. And a MySQL database with 50 tables takes about half a second to create however you do it, so for speed see [Templates on MySQL](#templates-on-mysql).

For the fastest possible test server, start it without durability, as `docker-compose.test.yml` does: `--innodb-flush-log-at-trx-commit=0 --sync-binlog=0 --skip-log-bin --innodb-doublewrite=0`. Tested against MySQL 8.4; MariaDB is likely to work but is not tested.

Other databases can be added by implementing `testdb.Provider`, plus `testdb.TemplateProvider` for templates.

## Migrations

`testdb.WithMigrator` takes anything that can prepare a database from a DSN:

```go
type Migrator interface {
    Migrate(ctx context.Context, dsn string) error
}
```

The migrations run inside your test process, under the test's context, so a hung migration stops when the test times out and there is no binary to install or keep in sync.

### Plain SQL files

No tool needed. `SQLFiles` runs every `.sql` file in a directory in lexical order:

```go
pool := postgres.Setup(t, testdb.WithMigrator(postgres.SQLFiles("./schema")))
```

### goose, tern, golang-migrate

Each adapter is its own Go module, so your project only depends on the tool it uses. goose and golang-migrate work with PostgreSQL and MySQL; tern is PostgreSQL-only.

```bash
go get github.com/bashhack/testdb/migrate/goose          # github.com/pressly/goose
go get github.com/bashhack/testdb/migrate/tern           # github.com/jackc/tern
go get github.com/bashhack/testdb/migrate/golangmigrate  # github.com/golang-migrate/migrate
```

```go
pool := postgres.Setup(t, testdb.WithMigrator(goose.Dir("./migrations")))
pool := postgres.Setup(t, testdb.WithMigrator(tern.Dir("./migrations")))
pool := postgres.Setup(t, testdb.WithMigrator(golangmigrate.Dir("./migrations")))
```

All three also accept an `fs.FS`, so migrations can be embedded with `embed.FS`:

```go
//go:embed migrations/*.sql
var migrations embed.FS

pool := postgres.Setup(t, testdb.WithMigrator(goose.FS(migrations, "migrations")))
```

### Your own code

Any function with the right shape is a migrator:

```go
pool := postgres.Setup(t, testdb.WithMigrator(testdb.MigratorFunc(
    func(ctx context.Context, dsn string) error {
        return myapp.Migrate(ctx, dsn)
    })))
```

A migrator's `Migrate` method is also a template setup function, so the same value builds a template once instead of migrating every test (see Templates).

## Templates

A template is a database you build once, from migrations, a SQL dump or any Go code, that each test then clones in a few milliseconds. Use one when migrations are slow, when you load large fixture files, or when many tests need the same starting data.

### Basic Template Usage

Ensure the template in `TestMain`, use it in tests, release it when the run ends:

```go
func TestMain(m *testing.M) {
    ctx := context.Background()

    tpl, err := postgres.EnsureTemplate(ctx, "with_schema",
        goose.Dir("./migrations").Migrate)
    if err != nil {
        log.Fatal(err)
    }

    code := m.Run()

    if err := tpl.Release(ctx); err != nil {
        log.Print(err)
    }
    os.Exit(code)
}

func TestUsers(t *testing.T) {
    // A clone of the template: schema already applied
    pool := postgres.Setup(t, testdb.WithTemplate("with_schema"))
    // Start testing immediately
}
```

### Safe to Share Between Packages

`go test ./...` runs every package as its own process, and CI often runs several jobs against one server. `EnsureTemplate` is built for that:

- The first process to arrive builds the template; the others wait for it and reuse it. Setup runs once, no matter how many packages share the name.
- A template is built under a temporary name and renamed into place, so no process ever sees a half-built one.
- `Release` drops the template only when the last process using it is done. A process that crashes drops out of the count on its own, because its claim on the template dies with its connection.
- The template is a real PostgreSQL template (`IS_TEMPLATE true`), so a stray `DROP DATABASE` is refused.

Everything sharing a template must use the same admin database; that is where the coordination happens.

### Keeping Templates Between Runs

By default a template lives for one run. To keep it, give it a fingerprint so it is rebuilt when its inputs change and reused when they don't:

```go
fp, err := postgres.HashFiles("./migrations", "fixtures/us.sql")
if err != nil {
    log.Fatal(err)
}
tpl, err := postgres.EnsureTemplate(ctx, "us_region", setup,
    postgres.WithFingerprint(fp),
    postgres.WithKeepTemplate())
```

If the fingerprint changed but another process is still using the old template, `EnsureTemplate` returns an error rather than pulling the template out from under it.

### Template with SQL Files

Load SQL dumps or fixture data:

```go
postgres.EnsureTemplate(ctx, "with_fixtures",
    postgres.LoadSQLFile("fixtures/seed_data.sql"))
```

### Template with Custom Setup

Any `func(ctx context.Context, dsn string) error` can build a template:

```go
setup := func(ctx context.Context, dsn string) error {
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil {
        return err
    }
    defer pool.Close()

    _, err = pool.Exec(ctx, `
        CREATE TABLE test_data (id SERIAL PRIMARY KEY, value INT);
    `)
    return err
}

postgres.EnsureTemplate(ctx, "custom", setup)
```

### Composite Templates

Combine migrations and fixtures with `Compose`:

```go
postgres.EnsureTemplate(ctx, "full",
    postgres.Compose(
        goose.Dir("./migrations").Migrate,
        postgres.LoadSQLFile("fixtures/data.sql"),
    ))
```

### Templates on MySQL

`mysql.EnsureTemplate`, `Release`, `WithFingerprint`, `WithKeepTemplate` and `testdb.WithTemplate` work exactly as above, with the same guarantees across processes. What differs is underneath, because MySQL has no way to clone a database:

- A clone is made object by object: tables (indexes, foreign keys, checks, `AUTO_INCREMENT` positions), rows, views, triggers, procedures and functions. Not copied: events and grants.
- That clone costs the server's per-table DDL, about 0.5–0.8 s for a 50-table schema on a laptop, no faster than running the migrations. So a MySQL template **prepares clones ahead of demand**: `WithReadyDatabases(n)` (default `GOMAXPROCS`) keeps `n` databases cloned and ready, refilled in the background by `WithReadyWorkers` parallel workers, and a test takes one instead of waiting. When the pool runs dry, the test clones on demand rather than wait.

Measured on MySQL 8.4 in Docker on a laptop, 50 tables with an index each, 5,000 rows:

| | per test |
|---|---|
| empty database | 8 ms |
| fresh database + migrations | 498 ms |
| clone on demand | 768 ms |
| prepared clone, tests of 200 ms or longer | **6 ms**, 100% from the pool |
| prepared clone, 50 ms tests back to back | 130–200 ms, 83–92% from the pool |

The pool keeps up as long as tests take clones no faster than the server makes them. For integration tests, which do real work, that holds comfortably and every test pays 6 ms. In a tight loop of trivial tests the floor is your server's clone throughput; more workers help until the server saturates.

Bookkeeping lives in `_testdb_*` tables inside the template (never copied into clones). A process that dies leaves nothing permanent: its hold on the template and its prepared databases are swept by the next `EnsureTemplate` or `Release` on that template.

### Multiple Templates

Different templates for different scenarios:

```go
func TestMain(m *testing.M) {
    ctx := context.Background()

    us, err := postgres.EnsureTemplate(ctx, "us_region", postgres.LoadSQLFile("fixtures/us.sql"))
    if err != nil {
        log.Fatal(err)
    }
    eu, err := postgres.EnsureTemplate(ctx, "eu_region", postgres.LoadSQLFile("fixtures/eu.sql"))
    if err != nil {
        log.Fatal(err)
    }

    code := m.Run()

    us.Release(ctx)
    eu.Release(ctx)
    os.Exit(code)
}

func TestUSCheckout(t *testing.T) {
    pool := postgres.Setup(t, testdb.WithTemplate("us_region"))
    // Test with US data
}

func TestEUVAT(t *testing.T) {
    pool := postgres.Setup(t, testdb.WithTemplate("eu_region"))
    // Test with EU data
}
```

## How It Works

testdb leverages PostgreSQL's `CREATE DATABASE` command for true isolation:

1. **Generates unique name** - Combines prefix, nanosecond timestamp, and random suffix: `test_1699564231_a1b2c3d4`
2. **Creates database** - Executes `CREATE DATABASE` via admin connection to `postgres` database
3. **Runs migrations** - If configured, executes the specified migration tool against the new database
4. **Returns connection** - Initializes a database connection using the specified initializer (e.g., *pgxpool.Pool, *sql.DB, or custom type)
5. **Registers cleanup** - Uses `t.Cleanup()` to ensure cleanup even if test panics
6. **Terminates connections** - On cleanup, forcefully closes all connections via `pg_terminate_backend`
7. **Drops database** - Executes `DROP DATABASE` to remove the test database

## Examples

See [postgres/example_test.go](postgres/example_test.go) for runnable examples demonstrating:

- Basic database creation and isolation
- Applying a schema from SQL files or your own migrator
- Running tests concurrently with `t.Parallel()`
- Custom prefixes and configuration options
- Using GORM and other ORMs
- Working with database/sql

All examples are also visible in the [package documentation](https://pkg.go.dev/github.com/bashhack/testdb/postgres).

## Requirements

- PostgreSQL server running locally or accessible
- Go 1.27+

## License

MIT License - see [LICENSE](LICENSE)
