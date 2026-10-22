package testdb

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := map[string]struct {
		cfg     Config
		wantErr error
	}{
		"defaults": {
			cfg:     DefaultConfig(),
			wantErr: nil,
		},
		"empty": {
			cfg:     Config{},
			wantErr: nil,
		},
		"prefix at the limit": {
			cfg:     Config{DBPrefix: strings.Repeat("p", MaxDBPrefixLength)},
			wantErr: nil,
		},
		"prefix too long": {
			cfg:     Config{DBPrefix: strings.Repeat("p", MaxDBPrefixLength+1)},
			wantErr: ErrPrefixTooLong,
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

func TestNewRejectsInvalidConfig(t *testing.T) {
	provider := &mockProvider{}

	_, err := New(t, provider, nil,
		WithDBPrefix(strings.Repeat("p", MaxDBPrefixLength+1)),
	)
	if err == nil {
		t.Fatal("Expected error for a prefix over the limit")
	}

	var testErr *Error
	if !errors.As(err, &testErr) {
		t.Fatalf("Expected *Error, got %T", err)
	}
	if testErr.Op != "testdb.New" {
		t.Errorf("Expected Op testdb.New, got %q", testErr.Op)
	}
	if !errors.Is(testErr.Err, ErrPrefixTooLong) {
		t.Errorf("Expected ErrPrefixTooLong, got %v", testErr.Err)
	}
}
