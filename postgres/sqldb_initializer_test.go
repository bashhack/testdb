package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/bashhack/testdb/postgres"
)

func TestSqlDbInitializer_Basic(t *testing.T) {
	db := postgres.New(t, &postgres.SqlDbInitializer{})

	sqlDB, ok := db.Entity().(*sql.DB)
	if !ok {
		t.Fatalf("expected *sql.DB, got %T", db.Entity())
	}

	var result int
	err := sqlDB.QueryRow("SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("failed to execute query: %v", err)
	}

	if result != 1 {
		t.Errorf("expected 1, got %d", result)
	}
}

func TestSqlDbInitializer_MultipleQueries(t *testing.T) {
	db := postgres.New(t, &postgres.SqlDbInitializer{})
	sqlDB := db.Entity().(*sql.DB)

	_, err := sqlDB.Exec(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	_, err = sqlDB.Exec(`INSERT INTO users (name) VALUES ($1), ($2)`, "Alice", "Bob")
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	rows, err := sqlDB.Query(`SELECT name FROM users ORDER BY id`)
	if err != nil {
		t.Fatalf("failed to query data: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("failed to close rows: %v", err)
		}
	}()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}
		names = append(names, name)
	}

	if len(names) != 2 || names[0] != "Alice" || names[1] != "Bob" {
		t.Errorf("expected [Alice Bob], got %v", names)
	}
}

func TestSqlDbInitializer_Transactions(t *testing.T) {
	db := postgres.New(t, &postgres.SqlDbInitializer{})
	sqlDB := db.Entity().(*sql.DB)

	_, err := sqlDB.Exec(`CREATE TABLE accounts (id SERIAL PRIMARY KEY, balance INT)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	_, err = sqlDB.Exec(`INSERT INTO accounts (balance) VALUES (100)`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	tx, err := sqlDB.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	_, err = tx.Exec(`UPDATE accounts SET balance = balance - 50 WHERE id = 1`)
	if err != nil {
		t.Fatalf("failed to update in transaction: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}

	var balance int
	err = sqlDB.QueryRow(`SELECT balance FROM accounts WHERE id = 1`).Scan(&balance)
	if err != nil {
		t.Fatalf("failed to query balance: %v", err)
	}

	if balance != 50 {
		t.Errorf("expected balance 50, got %d", balance)
	}

	tx, err = sqlDB.Begin()
	if err != nil {
		t.Fatalf("failed to begin second transaction: %v", err)
	}

	_, err = tx.Exec(`UPDATE accounts SET balance = 0 WHERE id = 1`)
	if err != nil {
		t.Fatalf("failed to update in transaction: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("failed to rollback transaction: %v", err)
	}

	err = sqlDB.QueryRow(`SELECT balance FROM accounts WHERE id = 1`).Scan(&balance)
	if err != nil {
		t.Fatalf("failed to query balance after rollback: %v", err)
	}

	if balance != 50 {
		t.Errorf("expected balance 50 after rollback, got %d", balance)
	}
}

func TestSqlDbInitializer_PreparedStatements(t *testing.T) {
	db := postgres.New(t, &postgres.SqlDbInitializer{})
	sqlDB := db.Entity().(*sql.DB)

	_, err := sqlDB.Exec(`CREATE TABLE products (id SERIAL PRIMARY KEY, name TEXT, price INT)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	stmt, err := sqlDB.Prepare(`INSERT INTO products (name, price) VALUES ($1, $2)`)
	if err != nil {
		t.Fatalf("failed to prepare statement: %v", err)
	}
	defer func() {
		if err := stmt.Close(); err != nil {
			t.Errorf("failed to close statement: %v", err)
		}
	}()

	products := []struct {
		name  string
		price int
	}{
		{"Widget", 100},
		{"Gadget", 200},
		{"Doohickey", 150},
	}

	for _, p := range products {
		_, err := stmt.Exec(p.name, p.price)
		if err != nil {
			t.Fatalf("failed to execute prepared statement: %v", err)
		}
	}

	rows, err := sqlDB.Query(`SELECT name, price FROM products ORDER BY id`)
	if err != nil {
		t.Fatalf("failed to query products: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("failed to close rows: %v", err)
		}
	}()

	i := 0
	for rows.Next() {
		var name string
		var price int
		if err := rows.Scan(&name, &price); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}

		if name != products[i].name || price != products[i].price {
			t.Errorf("row %d: expected %v, got {%s %d}", i, products[i], name, price)
		}
		i++
	}

	if i != len(products) {
		t.Errorf("expected %d rows, got %d", len(products), i)
	}
}

func TestSqlDbInitializer_Isolation(t *testing.T) {
	t.Run("test1", func(t *testing.T) {
		t.Parallel()
		db := postgres.New(t, &postgres.SqlDbInitializer{})
		sqlDB := db.Entity().(*sql.DB)

		_, err := sqlDB.Exec(`CREATE TABLE test1 (id INT)`)
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}

		_, err = sqlDB.Exec(`INSERT INTO test1 VALUES (1)`)
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}
	})

	t.Run("test2", func(t *testing.T) {
		t.Parallel()
		db := postgres.New(t, &postgres.SqlDbInitializer{})
		sqlDB := db.Entity().(*sql.DB)

		// This should succeed - we're in a fresh database
		_, err := sqlDB.Exec(`CREATE TABLE test1 (id INT)`)
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}

		// Should be empty (no data from test1)
		var count int
		err = sqlDB.QueryRow(`SELECT COUNT(*) FROM test1`).Scan(&count)
		if err != nil {
			t.Fatalf("failed to count: %v", err)
		}

		if count != 0 {
			t.Errorf("expected empty table, got %d rows", count)
		}
	})
}

func TestSqlDbInitializer_InvalidDSN(t *testing.T) {
	initializer := &postgres.SqlDbInitializer{}
	ctx := context.Background()

	// Invalid DSN should fail on Ping
	_, err := initializer.InitializeTestDatabase(ctx, "postgres://invalid:5432/nonexistent")
	if err == nil {
		t.Error("expected error for invalid DSN, got nil")
	}
}

func TestSqlDbInitializer_ConnectionPooling(t *testing.T) {
	db := postgres.New(t, &postgres.SqlDbInitializer{})
	sqlDB := db.Entity().(*sql.DB)

	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)

	_, err := sqlDB.Exec(`CREATE TABLE pool_test (id SERIAL PRIMARY KEY, value INT)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	done := make(chan bool)
	for i := range 10 {
		go func(val int) {
			_, err := sqlDB.Exec(`INSERT INTO pool_test (value) VALUES ($1)`, val)
			if err != nil {
				t.Errorf("failed to insert: %v", err)
			}
			done <- true
		}(i)
	}

	for range 10 {
		<-done
	}

	var count int
	err = sqlDB.QueryRow(`SELECT COUNT(*) FROM pool_test`).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}

	if count != 10 {
		t.Errorf("expected 10 rows, got %d", count)
	}
}
