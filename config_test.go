package testdb

import (
	"errors"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := map[string]struct {
		cfg     Config
		wantErr error
	}{
		"both set": {
			cfg: Config{
				MigrationDir:  "./migrations",
				MigrationTool: MigrationToolTern,
			},
			wantErr: nil,
		},
		"neither set": {
			cfg:     Config{},
			wantErr: nil,
		},
		"dir without tool": {
			cfg: Config{
				MigrationDir: "./migrations",
			},
			wantErr: ErrMigrationDirWithoutTool,
		},
		"tool without dir": {
			cfg: Config{
				MigrationTool: MigrationToolTern,
			},
			wantErr: ErrMigrationToolWithoutDir,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("validateConfig() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewWithMigrationDirWithoutTool(t *testing.T) {
	provider := &mockProvider{}

	_, err := New(t, provider, nil,
		WithMigrations("./migrations"),
	)

	if err == nil {
		t.Fatal("Expected error when migration directory specified without tool")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatalf("Expected *Error, got %T", err)
	}

	if !errors.Is(testErr.Err, ErrMigrationDirWithoutTool) {
		t.Errorf("Expected ErrMigrationDirWithoutTool, got %v", testErr.Err)
	}
}

func TestNewWithMigrationToolWithoutDir(t *testing.T) {
	provider := &mockProvider{}

	_, err := New(t, provider, nil,
		WithMigrationTool(MigrationToolTern),
	)

	if err == nil {
		t.Fatal("Expected error when migration tool specified without directory")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatalf("Expected *Error, got %T", err)
	}

	if !errors.Is(testErr.Err, ErrMigrationToolWithoutDir) {
		t.Errorf("Expected ErrMigrationToolWithoutDir, got %v", testErr.Err)
	}
}

func TestNewWithBothMigrationOptions(t *testing.T) {
	provider := &mockProvider{}

	db, err := New(t, provider, nil,
		WithMigrations("./migrations"),
		WithMigrationTool(MigrationToolTern),
	)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if db == nil {
		t.Fatal("Expected database to be created")
	}

	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("Failed to close database: %v", err)
		}
	}()

	if db.Config().MigrationDir != "./migrations" {
		t.Errorf("Expected MigrationDir './migrations', got %s", db.Config().MigrationDir)
	}

	if db.Config().MigrationTool != MigrationToolTern {
		t.Errorf("Expected MigrationTool 'tern', got %s", db.Config().MigrationTool)
	}
}
