# testdb

> True database isolation for Go tests using PostgreSQL's `CREATE DATABASE`. No Docker required.

[![Go Reference](https://pkg.go.dev/badge/github.com/bashhack/testdb.svg)](https://pkg.go.dev/github.com/bashhack/testdb)

## Features

- **True Isolation** - Each test gets its own database
- **Parallel Tests** - Run tests concurrently with `t.Parallel()`
- **Zero Docker** - Uses native PostgreSQL CREATE DATABASE
- **Auto Cleanup** - Databases are dropped automatically
- **Migration Support** - Works with Tern, Goose, and golang-migrate
- **PostgreSQL Support** - Production-ready with pgxpool

## Quick Start

### Prerequisites

A running PostgreSQL server (local installation, managed instance, or remote). testdb connects to PostgreSQL - it doesn't manage the server itself.

### Install

```bash
go get github.com/bashhack/testdb/postgres
```

### Basic Usage

```go
package myapp_test

import (
    "context"
    "testing"

    "github.com/bashhack/testdb"
    "github.com/bashhack/testdb/postgres"
)

func TestUsers(t *testing.T) {
    // Create isolated test database with your migrations
    pool := postgres.Setup(t,
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern),
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

**Standard tooling** - Integrates with existing migration tools (Tern, Goose, golang-migrate). Use the migrations you already have.

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
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern),
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

### Options

```go
pool := postgres.Setup(t,
    testdb.WithMigrations("./migrations"),
    testdb.WithMigrationTool(testdb.MigrationToolTern),
    testdb.WithAdminDSN("postgres://custom:5432/postgres"),
    testdb.WithDBPrefix("myapp_test"),
)
```

Available options:
- `WithMigrations(dir)` - Set migration directory (requires WithMigrationTool)
- `WithMigrationTool(tool)` - Use tern, goose, or migrate (requires WithMigrations)
- `WithAdminDSN(dsn)` - Override admin connection string
- `WithMigrationToolPath(path)` - Path to migration binary
- `WithDBPrefix(prefix)` - Database name prefix (default: "test")
- `WithVerbose()` - Enable verbose logging for debugging

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
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern),
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
    testdb.WithMigrations("./migrations"),
    testdb.WithMigrationTool(testdb.MigrationToolTern),
)
if err != nil {
    t.Fatalf("Failed to create database: %v", err)
}

// Run migrations manually if needed
if err := db.RunMigrations(); err != nil {
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
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern),
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
    testdb.WithMigrations("./migrations"),
    testdb.WithMigrationTool(testdb.MigrationToolTern),
)
```

Currently, only PostgreSQL is supported. Additional database support can be added by implementing the `testdb.Provider` interface.

## Migration Tools

testdb supports three migration tools. Both `WithMigrations()` and `WithMigrationTool()` must be specified together.

### Tern (PostgreSQL-only)

Install: `go install github.com/jackc/tern/v2@latest`

```go
postgres.Setup(t,
    testdb.WithMigrations("./migrations"),
    testdb.WithMigrationTool(testdb.MigrationToolTern),
)
```

### Goose (Multi-database)

Install: `go install github.com/pressly/goose/v3/cmd/goose@latest`

```go
postgres.Setup(t,
    testdb.WithMigrations("./migrations"),
    testdb.WithMigrationTool(testdb.MigrationToolGoose),
)
```

### golang-migrate (Multi-database)

Install: `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`

```go
postgres.Setup(t,
    testdb.WithMigrations("./migrations"),
    testdb.WithMigrationTool(testdb.MigrationToolMigrate),
)
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
- Using Tern, Goose, and golang-migrate
- Running tests concurrently with `t.Parallel()`
- Custom prefixes and configuration options
- Using GORM and other ORMs
- Working with database/sql

All examples are also visible in the [package documentation](https://pkg.go.dev/github.com/bashhack/testdb/postgres).

## Requirements

- PostgreSQL server running locally or accessible
- Migration tool (tern, goose, or migrate) in PATH if using migrations
- Go 1.24+

## License

MIT License - see [LICENSE](LICENSE)
