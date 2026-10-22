package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/bashhack/testdb"
	"github.com/bashhack/testdb/mysql"
)

// These examples show the MySQL API, which mirrors postgres's. They are
// compile-checked but not run: a runnable example cannot skip when no
// MySQL server is reachable, and the MySQL tests in this module skip
// rather than fail on a machine that only runs PostgreSQL.

// Example_setup is the common case: an isolated database with the schema
// applied, cleaned up when the test ends.
func Example_setup() {
	var t testing.TB // in a test this is your *testing.T

	db := mysql.Setup(t, testdb.WithMigrator(mysql.SQLFiles("./schema")))

	_, err := db.ExecContext(context.Background(),
		"INSERT INTO users (email) VALUES (?)", "test@example.com")
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	// No cleanup to write: the database is dropped via t.Cleanup
}

// Example_customInitializer hands the database to your own type, for
// sqlx, GORM, or an application wrapper.
func Example_customInitializer() {
	var t testing.TB

	db := mysql.New(t, &appDBInitializer{},
		testdb.WithMigrator(mysql.LoadSQLFile("./schema.sql")))

	appDB := db.Entity().(*appDB)
	fmt.Println(appDB.Name)
}

// appDB stands in for an application's own database type.
type appDB struct {
	*sql.DB
	Name string
}

type appDBInitializer struct{}

func (appDBInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	return &appDB{DB: db, Name: "app"}, nil
}

// Example_template builds the schema once in TestMain and gives every
// test a prepared clone, which is where MySQL's speed comes from.
func Example_template() {
	// func TestMain(m *testing.M) {
	ctx := context.Background()

	tpl, err := mysql.EnsureTemplate(ctx, "app_schema",
		mysql.SQLFiles("./schema"),
		mysql.WithReadyDatabases(8)) // clones kept prepared ahead of tests
	if err != nil {
		log.Fatal(err)
	}

	var m *testing.M
	code := m.Run()

	if err := tpl.Release(ctx); err != nil {
		log.Print(err)
	}
	os.Exit(code)
	// }
	//
	// func TestOrders(t *testing.T) {
	//     db := mysql.Setup(t, testdb.WithTemplate("app_schema")) // ~6 ms
	// }
}

// Example_keepOnFailure keeps a failed test's database and logs a mysql
// command line to open it.
func Example_keepOnFailure() {
	var t testing.TB

	db := mysql.Setup(t, testdb.WithKeepOnFailure())
	_ = db
	// On failure the test log shows:
	//   testdb: kept database test_1761100000_a1b2c3d4 for inspection:
	//       mysql --host='localhost' --port=3306 --user='root' --password='root' 'test_1761100000_a1b2c3d4'
	// The same switch for a whole run: TESTDB_KEEP_ON_FAILURE=1 go test ./...
}
