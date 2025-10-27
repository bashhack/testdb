package postgres

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
)

// SqlDbInitializer creates a standard *sql.DB connection using pgx's database/sql driver.
//
// This uses pgx/v5/stdlib, which implements the database/sql driver interface.
// You get the standard library's *sql.DB type with pgx's PostgreSQL implementation
// underneath, providing better PostgreSQL support than lib/pq while maintaining
// database/sql compatibility.
//
// Use SqlDbInitializer when:
//   - Your application code uses database/sql interfaces (*sql.DB, *sql.Tx, *sql.Rows)
//   - You're working with libraries that expect *sql.DB (some ORMs, query builders)
//   - You need compatibility with existing sql.DB-based code
//   - You want standard database/sql semantics (connection pooling, transactions, prepared statements)
//
// For PostgreSQL-specific features (arrays, JSON types, COPY, LISTEN/NOTIFY),
// use PoolInitializer instead which provides *pgxpool.Pool with full pgx capabilities.
//
// Example:
//
//	db := postgres.New(t, &postgres.SqlDbInitializer{})
//	sqlDB := db.Entity().(*sql.DB)
//
//	// Use standard database/sql operations
//	_, err := sqlDB.Exec("INSERT INTO users (name) VALUES ($1)", "Alice")
//	var name string
//	err = sqlDB.QueryRow("SELECT name FROM users WHERE id = $1", 1).Scan(&name)
type SqlDbInitializer struct{}

// InitializeTestDatabase creates a *sql.DB using the "pgx" driver (pgx/v5/stdlib).
// The connection is verified via Ping before being returned.
//
// Returns an error if the connection cannot be established or verified.
// On error, the database connection is automatically closed.
func (si *SqlDbInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // Best effort cleanup
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}
