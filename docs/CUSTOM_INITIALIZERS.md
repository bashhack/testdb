# Custom Initializers Guide

This guide explains when and how to use custom database initializers with testdb.

> **Note:** All examples in this guide use `postgres.New()` which automatically registers cleanup via `t.Cleanup()`. No manual cleanup code (`defer db.Close()`) is needed.

## Quick Decision Tree

```
Are you using pgx/pgxpool directly?
├─ Yes → Use postgres.Setup() - no custom initializer needed
└─ No → Do you use an ORM or custom DB wrapper?
    ├─ GORM/ent/SQLBoiler → Use postgres.New() with custom initializer
    ├─ sqlx → Use postgres.New() with custom initializer
    ├─ Custom wrapper type → Use postgres.New() with custom initializer
    └─ Custom pool config → Use postgres.New() with custom initializer
```

## Why Use a Custom Initializer?

**The golden rule:** Your tests should use the **same database type** as your application code.

- If your app functions expect `*gorm.DB`, your tests should use `*gorm.DB`
- If your app uses a custom `AppDB` wrapper, your tests should use that wrapper
- If your app configures connection pools a specific way, your tests should match

This ensures your tests accurately reflect real-world usage.

## Common Use Cases

### 1. Using GORM

**When:** Your application code uses GORM for all database operations.

**Application code:**
```go
func GetUser(db *gorm.DB, id int) (*User, error) {
    var user User
    result := db.First(&user, id)
    return &user, result.Error
}

func CreateUser(db *gorm.DB, email string) error {
    user := User{Email: email}
    return db.Create(&user).Error
}
```

**Custom initializer:**
```go
type GormInitializer struct{}

func (g *GormInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
    return gorm.Open(postgres.Open(dsn), &gorm.Config{
        // Add your GORM config here
        NowFunc: func() time.Time {
            return time.Now().UTC()
        },
    })
}
```

**Test code:**
```go
func TestGetUser(t *testing.T) {
    db := postgres.New(t, &GormInitializer{},
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolGoose))

    gormDB := db.Entity().(*gorm.DB)

    // Auto-migrate for test
    gormDB.AutoMigrate(&User{})

    // Call your actual application functions
    err := CreateUser(gormDB, "test@example.com")
    require.NoError(t, err)

    user, err := GetUser(gormDB, 1)
    require.NoError(t, err)
    assert.Equal(t, "test@example.com", user.Email)
}
```

### 2. Using sqlx

**When:** Your application uses sqlx for convenient struct scanning.

**Application code:**
```go
func ListUsers(db *sqlx.DB) ([]User, error) {
    var users []User
    err := db.Select(&users, "SELECT * FROM users ORDER BY created_at DESC")
    return users, err
}

func GetUserByEmail(db *sqlx.DB, email string) (*User, error) {
    var user User
    err := db.Get(&user, "SELECT * FROM users WHERE email = $1", email)
    return &user, err
}
```

**Custom initializer:**
```go
type SqlxInitializer struct{}

func (s *SqlxInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
    db, err := sqlx.Connect("postgres", dsn)
    if err != nil {
        return nil, err
    }

    // Configure sqlx
    db.SetMaxOpenConns(10)
    db.SetMaxIdleConns(5)

    return db, nil
}
```

**Test code:**
```go
func TestListUsers(t *testing.T) {
    db := postgres.New(t, &SqlxInitializer{},
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolMigrate))

    sqlxDB := db.Entity().(*sqlx.DB)

    // Call your actual application functions
    users, err := ListUsers(sqlxDB)
    require.NoError(t, err)
    assert.Len(t, users, 0)
}
```

### 3. Custom Application Wrapper

**When:** Your application wraps database connections in a custom type.

**Application code:**
```go
// Your app's database wrapper
type AppDB struct {
    Pool             *pgxpool.Pool
    StatementTimeout time.Duration
    DefaultSchema    string
}

func (a *AppDB) QueryWithTimeout(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
    ctx, cancel := context.WithTimeout(ctx, a.StatementTimeout)
    defer cancel()
    return a.Pool.Query(ctx, query, args...)
}

// Your app functions expect AppDB
func GetActiveUsers(db *AppDB) ([]User, error) {
    rows, err := db.QueryWithTimeout(context.Background(),
        "SELECT id, email FROM users WHERE active = true")
    // ... process rows
}
```

**Custom initializer:**
```go
type AppDBInitializer struct {
    Timeout time.Duration
}

func (a *AppDBInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil {
        return nil, err
    }

    timeout := a.Timeout
    if timeout == 0 {
        timeout = 30 * time.Second
    }

    return &AppDB{
        Pool:             pool,
        StatementTimeout: timeout,
        DefaultSchema:    "public",
    }, nil
}
```

**Test code:**
```go
func TestGetActiveUsers(t *testing.T) {
    db := postgres.New(t, &AppDBInitializer{Timeout: 10 * time.Second},
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern))

    appDB := db.Entity().(*AppDB)

    // Call your actual application functions
    users, err := GetActiveUsers(appDB)
    require.NoError(t, err)
}
```

### 4. Custom Connection Pool Settings

**When:** Your application requires specific connection pool configuration.

**Application code:**
```go
// Your app initializes pgxpool with specific settings
func NewProductionPool(dsn string) (*pgxpool.Pool, error) {
    config, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, err
    }

    config.MaxConns = 50
    config.MinConns = 10
    config.MaxConnLifetime = 1 * time.Hour
    config.MaxConnIdleTime = 30 * time.Minute
    config.HealthCheckPeriod = 1 * time.Minute

    return pgxpool.NewWithConfig(context.Background(), config)
}
```

**Custom initializer:**
```go
type ProductionPoolInitializer struct{}

func (p *ProductionPoolInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
    config, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, err
    }

    // Match production settings (or scale down for tests)
    config.MaxConns = 10        // Scaled down from 50
    config.MinConns = 2         // Scaled down from 10
    config.MaxConnLifetime = 1 * time.Hour
    config.MaxConnIdleTime = 30 * time.Minute
    config.HealthCheckPeriod = 1 * time.Minute

    return pgxpool.NewWithConfig(ctx, config)
}
```

**Test code:**
```go
func TestHighConcurrency(t *testing.T) {
    db := postgres.New(t, &ProductionPoolInitializer{})
    pool := db.Entity().(*pgxpool.Pool)

    // Test with realistic pool settings
    var wg sync.WaitGroup
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            // Run concurrent queries
        }()
    }
    wg.Wait()
}
```

### 5. Tracing and Instrumentation

**When:** Your application adds query tracing, logging, or metrics.

**Application code:**
```go
type QueryTracer struct {
    Logger *log.Logger
}

func (q *QueryTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
    q.Logger.Printf("Executing query: %s", data.SQL)
    return ctx
}

func (q *QueryTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
    q.Logger.Printf("Query completed in %v", data.EndTime.Sub(data.StartTime))
}
```

**Custom initializer:**
```go
type TracedInitializer struct {
    Logger *log.Logger
}

func (t *TracedInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
    config, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, err
    }

    // Add query tracer
    config.ConnConfig.Tracer = &QueryTracer{Logger: t.Logger}

    // Add connection lifecycle hooks
    config.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
        t.Logger.Println("Acquiring connection from pool")
        return true
    }

    config.AfterRelease = func(conn *pgx.Conn) bool {
        t.Logger.Println("Releasing connection to pool")
        return true
    }

    return pgxpool.NewWithConfig(ctx, config)
}
```

**Test code:**
```go
func TestWithTracing(t *testing.T) {
    logger := log.New(os.Stdout, "TEST: ", log.LstdFlags)

    db := postgres.New(t, &TracedInitializer{Logger: logger})
    pool := db.Entity().(*pgxpool.Pool)

    // All queries will be traced
    var result int
    err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
    require.NoError(t, err)
}
```

## Helper Function Pattern

For tests that all need the same custom initializer, create a helper:

```go
// testutil/database.go
func SetupAppDB(t testing.TB) *AppDB {
    t.Helper()

    db := postgres.New(t, &AppDBInitializer{},
        testdb.WithMigrations("../migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern))

    return db.Entity().(*AppDB)
}

// In your tests
func TestSomething(t *testing.T) {
    db := testutil.SetupAppDB(t)
    // Use db
}
```

## When NOT to Use a Custom Initializer

If you're using standard pgx/pgxpool with default settings, just use `postgres.Setup()`:

```go
func TestSimple(t *testing.T) {
    // This is simpler and sufficient for standard pgx usage
    pool := postgres.Setup(t,
        testdb.WithMigrations("./migrations"),
        testdb.WithMigrationTool(testdb.MigrationToolTern))

    // Use pool
}
```

## Summary

- **Use `postgres.Setup()`** for standard pgx usage
- **Use `postgres.New()` with custom initializer** when your app uses a different DB type or wrapper
- **Match your test DB type to your app's DB type** for accurate testing
- **Create helper functions** for repeated initializer usage across tests
