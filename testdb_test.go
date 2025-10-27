package testdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.DBPrefix != "test" {
		t.Errorf("Expected default DBPrefix to be 'test', got '%s'", cfg.DBPrefix)
	}

	if cfg.MigrationTool != "" {
		t.Errorf("Expected default MigrationTool to be empty, got '%s'", cfg.MigrationTool)
	}

	if cfg.AdminDSNOverride != "" {
		t.Errorf("Expected default AdminDSN to be empty, got '%s'", cfg.AdminDSNOverride)
	}

	if cfg.MigrationDir != "" {
		t.Errorf("Expected default MigrationDir to be empty, got '%s'", cfg.MigrationDir)
	}

	if cfg.MigrationToolPath != "" {
		t.Errorf("Expected default MigrationToolPath to be empty, got '%s'", cfg.MigrationToolPath)
	}
}

func TestWithMigrations(t *testing.T) {
	cfg := DefaultConfig()
	opt := WithMigrations("./migrations")
	opt(&cfg)

	if cfg.MigrationDir != "./migrations" {
		t.Errorf("Expected MigrationDir to be './migrations', got '%s'", cfg.MigrationDir)
	}
}

func TestWithAdminDSN(t *testing.T) {
	cfg := DefaultConfig()
	dsn := "postgres://user:pass@localhost:5432/postgres"
	opt := WithAdminDSN(dsn)
	opt(&cfg)

	if cfg.AdminDSNOverride != dsn {
		t.Errorf("Expected AdminDSN to be '%s', got '%s'", dsn, cfg.AdminDSNOverride)
	}
}

func TestWithMigrationTool(t *testing.T) {
	cfg := DefaultConfig()
	opt := WithMigrationTool(MigrationToolGoose)
	opt(&cfg)

	if cfg.MigrationTool != MigrationToolGoose {
		t.Errorf("Expected MigrationTool to be 'goose', got '%s'", cfg.MigrationTool)
	}
}

func TestWithMigrationToolPath(t *testing.T) {
	cfg := DefaultConfig()
	path := "/usr/local/bin/tern"
	opt := WithMigrationToolPath(path)
	opt(&cfg)

	if cfg.MigrationToolPath != path {
		t.Errorf("Expected MigrationToolPath to be '%s', got '%s'", path, cfg.MigrationToolPath)
	}
}

func TestWithDBPrefix(t *testing.T) {
	cfg := DefaultConfig()
	prefix := "myapp_test"
	opt := WithDBPrefix(prefix)
	opt(&cfg)

	if cfg.DBPrefix != prefix {
		t.Errorf("Expected DBPrefix to be '%s', got '%s'", prefix, cfg.DBPrefix)
	}
}

func TestWithVerbose(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Verbose {
		t.Error("Expected default Verbose to be false")
	}

	opt := WithVerbose()
	opt(&cfg)

	if !cfg.Verbose {
		t.Error("Expected Verbose to be true after WithVerbose()")
	}
}

func TestVerboseLogging(t *testing.T) {
	spy := &verboseSpyTB{TB: t}
	provider := &mockProvider{}
	initializer := &mockInitializer{}

	db, err := New(spy, provider, initializer, WithVerbose())
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	if len(spy.logs) == 0 {
		t.Error("Expected log output with Verbose=true, got none")
	}

	foundCreationLog := false
	for _, log := range spy.logs {
		if strings.Contains(log, "creating database") {
			foundCreationLog = true
			break
		}
	}
	if !foundCreationLog {
		t.Error("Expected to find 'creating database' log message")
	}

	spy.logs = nil
	if err := db.Close(); err != nil {
		t.Errorf("Failed to close database: %v", err)
	}

	foundCleanupLog := false
	for _, log := range spy.logs {
		if strings.Contains(log, "cleaning up database") {
			foundCleanupLog = true
			break
		}
	}
	if !foundCleanupLog {
		t.Error("Expected to find 'cleaning up database' log message")
	}
}

func TestVerboseLoggingDisabled(t *testing.T) {
	spy := &verboseSpyTB{TB: t}
	provider := &mockProvider{}
	initializer := &mockInitializer{}

	// Test with Verbose disabled (default)
	db, err := New(spy, provider, initializer)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	if len(spy.logs) > 0 {
		t.Errorf("Expected no log output with Verbose=false, got %d logs: %v", len(spy.logs), spy.logs)
	}

	if err := db.Close(); err != nil {
		t.Errorf("Failed to close database: %v", err)
	}

	if len(spy.logs) > 0 {
		t.Errorf("Expected no log output with Verbose=false, got %d logs: %v", len(spy.logs), spy.logs)
	}
}

func TestMultipleOptions(t *testing.T) {
	cfg := DefaultConfig()

	opts := []Option{
		WithMigrations("./migrations"),
		WithAdminDSN("postgres://localhost/db"),
		WithMigrationTool(MigrationToolGoose),
		WithMigrationToolPath("/usr/bin/goose"),
		WithDBPrefix("custom"),
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.MigrationDir != "./migrations" {
		t.Errorf("Expected MigrationDir to be './migrations', got '%s'", cfg.MigrationDir)
	}
	if cfg.AdminDSNOverride != "postgres://localhost/db" {
		t.Errorf("Expected AdminDSN to be 'postgres://localhost/db', got '%s'", cfg.AdminDSNOverride)
	}
	if cfg.MigrationTool != MigrationToolGoose {
		t.Errorf("Expected MigrationTool to be 'goose', got '%s'", cfg.MigrationTool)
	}
	if cfg.MigrationToolPath != "/usr/bin/goose" {
		t.Errorf("Expected MigrationToolPath to be '/usr/bin/goose', got '%s'", cfg.MigrationToolPath)
	}
	if cfg.DBPrefix != "custom" {
		t.Errorf("Expected DBPrefix to be 'custom', got '%s'", cfg.DBPrefix)
	}
}

func TestDiscoverAdminDSN(t *testing.T) {
	origTestDB := os.Getenv("TEST_DATABASE_URL")
	origDB := os.Getenv("DATABASE_URL")
	defer func() {
		if origTestDB != "" {
			_ = os.Setenv("TEST_DATABASE_URL", origTestDB)
		} else {
			_ = os.Unsetenv("TEST_DATABASE_URL")
		}
		if origDB != "" {
			_ = os.Setenv("DATABASE_URL", origDB)
		} else {
			_ = os.Unsetenv("DATABASE_URL")
		}
	}()

	_ = os.Unsetenv("TEST_DATABASE_URL")
	_ = os.Unsetenv("DATABASE_URL")
	if dsn := discoverAdminDSN(); dsn != "" {
		t.Errorf("Expected empty DSN with no env vars, got '%s'", dsn)
	}

	_ = os.Setenv("DATABASE_URL", "postgres://db/url")
	if dsn := discoverAdminDSN(); dsn != "postgres://db/url" {
		t.Errorf("Expected 'postgres://db/url', got '%s'", dsn)
	}

	_ = os.Setenv("TEST_DATABASE_URL", "postgres://test/url")
	if dsn := discoverAdminDSN(); dsn != "postgres://test/url" {
		t.Errorf("Expected 'postgres://test/url', got '%s'", dsn)
	}
}

func TestGenerateDatabaseName(t *testing.T) {
	name1, err := generateDatabaseName("test")
	if err != nil {
		t.Fatalf("Failed to generate database name: %v", err)
	}

	if !strings.HasPrefix(name1, "test_") {
		t.Errorf("Expected name to start with 'test_', got '%s'", name1)
	}

	parts := strings.Split(name1, "_")
	if len(parts) != 3 {
		t.Errorf("Expected 3 parts in name, got %d: %s", len(parts), name1)
	}

	name2, err := generateDatabaseName("custom")
	if err != nil {
		t.Fatalf("Failed to generate database name: %v", err)
	}

	if !strings.HasPrefix(name2, "custom_") {
		t.Errorf("Expected name to start with 'custom_', got '%s'", name2)
	}

	name3, err := generateDatabaseName("")
	if err != nil {
		t.Fatalf("Failed to generate database name: %v", err)
	}

	if !strings.HasPrefix(name3, "test_") {
		t.Errorf("Expected name to start with 'test_', got '%s'", name3)
	}

	names := make(map[string]bool)
	for range 10 {
		name, err := generateDatabaseName("test")
		if err != nil {
			t.Fatalf("Failed to generate database name: %v", err)
		}
		if names[name] {
			t.Errorf("Generated duplicate name: %s", name)
		}
		names[name] = true
	}
}

func TestErrorTypes(t *testing.T) {
	err := &Error{
		Op:  "test.Operation",
		Err: errors.New("something went wrong"),
	}

	expected := "testdb: test.Operation: something went wrong"
	if err.Error() != expected {
		t.Errorf("Expected error message '%s', got '%s'", expected, err.Error())
	}

	err2 := &Error{
		Err: errors.New("basic error"),
	}

	expected2 := "testdb: basic error"
	if err2.Error() != expected2 {
		t.Errorf("Expected error message '%s', got '%s'", expected2, err2.Error())
	}

	underlying := errors.New("underlying error")
	wrapped := &Error{
		Op:  "test.Op",
		Err: underlying,
	}

	if unwrapped := wrapped.Unwrap(); unwrapped != underlying {
		t.Error("Expected Unwrap to return underlying error")
	}
}

func TestDriverFromDSN(t *testing.T) {
	tests := map[string]struct {
		dsn      string
		expected string
		wantErr  bool
	}{
		"postgres scheme":   {"postgres://localhost/db", "postgres", false},
		"postgresql scheme": {"postgresql://localhost/db", "postgres", false},
		"mysql scheme":      {"mysql://localhost/db", "mysql", false},
		"sqlite3 scheme":    {"sqlite3://path/to/db", "sqlite3", false},
		"sqlite scheme":     {"sqlite://path/to/db", "sqlite3", false},
		"unknown scheme":    {"unknown://localhost/db", "", true},
		"empty dsn":         {"", "", true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			driver, err := driverFromDSN(tc.dsn)
			if tc.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if driver != tc.expected {
				t.Errorf("Expected driver '%s', got '%s'", tc.expected, driver)
			}
		})
	}
}

func TestNewWithNilProvider(t *testing.T) {
	_, err := New(t, nil, nil)
	if err == nil {
		t.Fatal("Expected error when provider is nil")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if !errors.Is(testErr.Err, ErrNilProvider) {
		t.Errorf("Expected ErrNilProvider, got %v", testErr.Err)
	}
}

func TestEntityNil(t *testing.T) {
	provider := &mockProvider{}
	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	if entity := db.Entity(); entity != nil {
		t.Error("Expected Entity to be nil when no initializer provided")
	}
}

func TestNewWithMockProvider(t *testing.T) {
	provider := &mockProvider{}
	initializer := &mockInitializer{}

	db, err := New(t, provider, initializer,
		WithDBPrefix("test"),
		WithAdminDSN("mock://admin"))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	if db.Name() == "" {
		t.Error("Expected database name to be set")
	}

	if !strings.HasPrefix(db.Name(), "test_") {
		t.Errorf("Expected database name to start with 'test_', got '%s'", db.Name())
	}

	if db.DSN() == "" {
		t.Error("Expected DSN to be set")
	}

	entity := db.Entity()
	if entity == nil {
		t.Fatal("Expected Entity to be set when initializer provided")
	}

	mockEntity, ok := entity.(*mockDB)
	if !ok {
		t.Fatal("Expected entity to be *mockDB")
	}

	if mockEntity.dsn != db.DSN() {
		t.Errorf("Expected entity DSN to be '%s', got '%s'", db.DSN(), mockEntity.dsn)
	}
}

func TestLowLevelNewDoesNotRegisterCleanup(t *testing.T) {
	spy := &spyTB{TB: t}
	provider := &mockProvider{}
	initializer := &mockInitializer{}

	db, err := New(spy, provider, initializer, WithDBPrefix("test"))
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	// Verify that NO cleanup functions were registered
	// This is the key difference from postgres.Setup() and postgres.New()
	if len(spy.cleanups) != 0 {
		t.Errorf("Expected 0 cleanup functions to be registered, got %d", len(spy.cleanups))
	}

	// Manual cleanup is required when using low-level testdb.New()
	if err := db.Close(); err != nil {
		t.Errorf("Failed to close database: %v", err)
	}
}

func TestCloseWithoutInitializer(t *testing.T) {
	provider := &mockProvider{}
	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Errorf("Expected Close to succeed, got error: %v", err)
	}
}

func TestRunMigrationsNoDir(t *testing.T) {
	provider := &mockProvider{}
	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error when migration directory is not set")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if !errors.Is(testErr.Err, ErrNoMigrationDir) {
		t.Errorf("Expected ErrNoMigrationDir, got %v", testErr.Err)
	}
}

func TestRunMigrationsUnknownTool(t *testing.T) {
	provider := &mockProvider{}
	db, err := New(t, provider, nil,
		WithMigrations("./migrations"),
		WithMigrationTool("unknown"))

	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	err = db.RunMigrations()
	if err == nil {
		t.Fatal("Expected error with unknown migration tool")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if !errors.Is(testErr.Err, ErrUnknownMigrationTool) {
		t.Errorf("Expected ErrUnknownMigrationTool, got %v", testErr.Err)
	}
}

func TestNewProviderInitializeError(t *testing.T) {
	provider := &mockErrorProvider{failInitialize: true}

	_, err := New(t, provider, nil)
	if err == nil {
		t.Fatal("Expected error when provider.Initialize fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "provider.Initialize" {
		t.Errorf("Expected Op to be 'provider.Initialize', got '%s'", testErr.Op)
	}
}

func TestNewCreateDatabaseError(t *testing.T) {
	provider := &mockErrorProvider{failCreate: true}

	_, err := New(t, provider, nil)
	if err == nil {
		t.Fatal("Expected error when CreateDatabase fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "provider.CreateDatabase" {
		t.Errorf("Expected Op to be 'provider.CreateDatabase', got '%s'", testErr.Op)
	}
}

func TestNewBuildDSNError(t *testing.T) {
	provider := &mockErrorProvider{failBuildDSN: true}

	_, err := New(t, provider, nil)
	if err == nil {
		t.Fatal("Expected error when BuildDSN fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "provider.BuildDSN" {
		t.Errorf("Expected Op to be 'provider.BuildDSN', got '%s'", testErr.Op)
	}
}

func TestNewInitializerError(t *testing.T) {
	provider := &mockProvider{}
	initializer := &mockErrorInitializer{}

	_, err := New(t, provider, initializer)
	if err == nil {
		t.Fatal("Expected error when initializer fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "initializer.InitializeTestDatabase" {
		t.Errorf("Expected Op to be 'initializer.InitializeTestDatabase', got '%s'", testErr.Op)
	}
}

func TestCloseTerminateConnectionsError(t *testing.T) {
	provider := &mockErrorProvider{failTerminate: true}

	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	err = db.Close()
	if err == nil {
		t.Fatal("Expected error when TerminateConnections fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "provider.TerminateConnections" {
		t.Errorf("Expected Op to be 'provider.TerminateConnections', got '%s'", testErr.Op)
	}
}

func TestCloseDropDatabaseError(t *testing.T) {
	provider := &mockErrorProvider{failDrop: true}

	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	err = db.Close()
	if err == nil {
		t.Fatal("Expected error when DropDatabase fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "provider.DropDatabase" {
		t.Errorf("Expected Op to be 'provider.DropDatabase', got '%s'", testErr.Op)
	}
}

func TestCloseCleanupError(t *testing.T) {
	provider := &mockErrorProvider{failCleanup: true}

	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	err = db.Close()
	if err == nil {
		t.Fatal("Expected error when Cleanup fails")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatal("Expected error to be *testdb.Error")
	}

	if testErr.Op != "provider.Cleanup" {
		t.Errorf("Expected Op to be 'provider.Cleanup', got '%s'", testErr.Op)
	}
}

func TestCloseWithNilCleanup(t *testing.T) {
	provider := &mockProvider{}
	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	v := reflect.ValueOf(db).Elem()
	cleanupField := v.FieldByName("cleanup")
	if cleanupField.IsValid() && cleanupField.CanAddr() {
		cleanupPtr := (*func() error)(unsafe.Pointer(cleanupField.UnsafeAddr()))
		*cleanupPtr = nil

		err := db.Close()
		if err != nil {
			t.Errorf("Expected Close to return nil when cleanup is nil, got: %v", err)
		}
	} else {
		t.Skip("Cannot access cleanup field")
	}
}

// mockErrorInitializer fails to initialize
type mockErrorInitializer struct{}

func (m *mockErrorInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	return nil, errors.New("initializer failed")
}

// mockErrorProvider is a provider that fails at specific operations
type mockErrorProvider struct {
	failInitialize bool
	failCreate     bool
	failWait       bool
	failBuildDSN   bool
	failTerminate  bool
	failDrop       bool
	failCleanup    bool
	adminDSN       string
}

func (m *mockErrorProvider) Initialize(ctx context.Context, cfg Config) error {
	if m.failInitialize {
		return errors.New("initialize failed")
	}
	m.adminDSN = cfg.AdminDSNOverride
	if m.adminDSN == "" {
		m.adminDSN = "mock://admin"
	}
	return nil
}

func (m *mockErrorProvider) CreateDatabase(ctx context.Context, name string) error {
	if m.failCreate {
		return errors.New("create database failed")
	}
	return nil
}

func (m *mockErrorProvider) DropDatabase(ctx context.Context, name string) error {
	if m.failDrop {
		return errors.New("drop database failed")
	}
	return nil
}

func (m *mockErrorProvider) WaitForDatabase(ctx context.Context, name string) error {
	if m.failWait {
		return errors.New("wait for database failed")
	}
	return nil
}

func (m *mockErrorProvider) TerminateConnections(ctx context.Context, name string) error {
	if m.failTerminate {
		return errors.New("terminate connections failed")
	}
	return nil
}

func (m *mockErrorProvider) BuildDSN(dbName string) (string, error) {
	if m.failBuildDSN {
		return "", errors.New("build DSN failed")
	}
	return "mock://" + dbName, nil
}

func (m *mockErrorProvider) ResolvedAdminDSN() string {
	return m.adminDSN
}

func (m *mockErrorProvider) Cleanup(ctx context.Context) error {
	if m.failCleanup {
		return errors.New("cleanup failed")
	}
	return nil
}

// spyTB is a testing.TB implementation that captures Cleanup calls
type spyTB struct {
	testing.TB
	cleanups []func()
}

func (s *spyTB) Cleanup(f func()) {
	s.cleanups = append(s.cleanups, f)
}

func (s *spyTB) Helper() {}

// mockInitializer implements DBInitializer for testing
type mockInitializer struct{}

type mockDB struct {
	dsn string
}

func (m *mockInitializer) InitializeTestDatabase(ctx context.Context, dsn string) (any, error) {
	return &mockDB{dsn: dsn}, nil
}

// mockProvider is a minimal provider implementation for testing
type mockProvider struct {
	adminDSN string
}

func (m *mockProvider) Initialize(ctx context.Context, cfg Config) error {
	m.adminDSN = cfg.AdminDSNOverride
	if m.adminDSN == "" {
		m.adminDSN = "mock://admin"
	}
	return nil
}

func (m *mockProvider) CreateDatabase(ctx context.Context, name string) error {
	return nil
}

func (m *mockProvider) DropDatabase(ctx context.Context, name string) error {
	return nil
}

func (m *mockProvider) WaitForDatabase(ctx context.Context, name string) error {
	return nil
}

func (m *mockProvider) TerminateConnections(ctx context.Context, name string) error {
	return nil
}

func (m *mockProvider) BuildDSN(dbName string) (string, error) {
	return "mock://" + dbName, nil
}

func (m *mockProvider) ResolvedAdminDSN() string {
	return m.adminDSN
}

func (m *mockProvider) Cleanup(ctx context.Context) error {
	return nil
}

type verboseSpyTB struct {
	testing.TB
	logs []string
}

func (v *verboseSpyTB) Logf(format string, args ...any) {
	v.logs = append(v.logs, fmt.Sprintf(format, args...))
}

func (v *verboseSpyTB) Helper() {}
