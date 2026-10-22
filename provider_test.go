package testdb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// plainProvider implements Provider but not TemplateProvider, standing in
// for a database that cannot clone one database from another. It delegates
// to a mockProvider by hand so no template method gets promoted.
type plainProvider struct {
	inner       mockProvider
	initialized bool
}

func (p *plainProvider) Initialize(ctx context.Context, cfg Config) error {
	p.initialized = true
	return p.inner.Initialize(ctx, cfg)
}
func (p *plainProvider) CreateDatabase(ctx context.Context, name string) error {
	return p.inner.CreateDatabase(ctx, name)
}
func (p *plainProvider) DropDatabase(ctx context.Context, name string) error {
	return p.inner.DropDatabase(ctx, name)
}
func (p *plainProvider) TerminateConnections(ctx context.Context, name string) error {
	return p.inner.TerminateConnections(ctx, name)
}
func (p *plainProvider) BuildDSN(dbName string) (string, error) { return p.inner.BuildDSN(dbName) }
func (p *plainProvider) ResolvedAdminDSN() string               { return p.inner.ResolvedAdminDSN() }
func (p *plainProvider) Cleanup(ctx context.Context) error      { return p.inner.Cleanup(ctx) }

// Compile-time statements of which interfaces the test doubles satisfy.
var (
	_ Provider         = (*plainProvider)(nil)
	_ TemplateProvider = (*mockProvider)(nil)
)

func TestNewWithTemplateNeedsTemplateProvider(t *testing.T) {
	provider := &plainProvider{}

	_, err := New(t, provider, nil, WithTemplate("us_region"))
	if !errors.Is(err, ErrTemplatesNotSupported) {
		t.Fatalf("expected ErrTemplatesNotSupported, got %v", err)
	}
	var testErr *Error
	if !errors.As(err, &testErr) || testErr.Op != "testdb.New" {
		t.Errorf("expected *Error with Op testdb.New, got %#v", err)
	}
	if !strings.Contains(err.Error(), "plainProvider") {
		t.Errorf("error should name the provider type, got: %v", err)
	}
	if provider.initialized {
		t.Error("New should refuse before initializing the provider")
	}
}

func TestNewWithoutTemplateAcceptsPlainProvider(t *testing.T) {
	provider := &plainProvider{}

	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatalf("plain provider without a template should work, got %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAdminDSN(t *testing.T) {
	const (
		override  = "postgres://override/db"
		specific  = "postgres://specific/db"
		specific2 = "postgres://specific2/db"
		shared    = "postgres://shared/db"
		generic   = "postgres://generic/db"
		fallback  = "postgres://default/db"
	)

	tests := map[string]struct {
		cfg  Config
		env  map[string]string
		args []string
		want string
	}{
		"override beats everything": {
			cfg:  Config{AdminDSNOverride: override},
			env:  map[string]string{"TEST_X_URL": specific, "TEST_DATABASE_URL": shared, "DATABASE_URL": generic},
			args: []string{"TEST_X_URL"},
			want: override,
		},
		"provider variable beats the shared ones": {
			env:  map[string]string{"TEST_X_URL": specific, "TEST_DATABASE_URL": shared, "DATABASE_URL": generic},
			args: []string{"TEST_X_URL"},
			want: specific,
		},
		"provider variables are tried in order": {
			env:  map[string]string{"TEST_Y_URL": specific2, "TEST_DATABASE_URL": shared},
			args: []string{"TEST_X_URL", "TEST_Y_URL"},
			want: specific2,
		},
		"TEST_DATABASE_URL beats DATABASE_URL": {
			env:  map[string]string{"TEST_DATABASE_URL": shared, "DATABASE_URL": generic},
			args: []string{"TEST_X_URL"},
			want: shared,
		},
		"DATABASE_URL when nothing else": {
			env:  map[string]string{"DATABASE_URL": generic},
			args: []string{"TEST_X_URL"},
			want: generic,
		},
		"default when no variable is set": {
			args: []string{"TEST_X_URL"},
			want: fallback,
		},
		"no provider variables given": {
			env:  map[string]string{"TEST_DATABASE_URL": shared},
			want: shared,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			for _, v := range []string{"TEST_X_URL", "TEST_Y_URL", "TEST_DATABASE_URL", "DATABASE_URL"} {
				t.Setenv(v, tc.env[v])
			}
			if got := ResolveAdminDSN(tc.cfg, fallback, tc.args...); got != tc.want {
				t.Errorf("ResolveAdminDSN = %q, want %q", got, tc.want)
			}
		})
	}
}
