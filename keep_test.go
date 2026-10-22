package testdb

import (
	"context"
	"strings"
	"testing"
)

// countingProvider records drops so tests can tell whether Close dropped
// the database.
type countingProvider struct {
	mockProvider
	drops      int
	terminates int
	cleanups   int
}

func (p *countingProvider) DropDatabase(ctx context.Context, name string) error {
	p.drops++
	return p.mockProvider.DropDatabase(ctx, name)
}

func (p *countingProvider) TerminateConnections(ctx context.Context, name string) error {
	p.terminates++
	return p.mockProvider.TerminateConnections(ctx, name)
}

func (p *countingProvider) Cleanup(ctx context.Context) error {
	p.cleanups++
	return p.mockProvider.Cleanup(ctx)
}

// InspectCommand stands in for a database client invocation.
func (p *countingProvider) InspectCommand(dsn string) string {
	return "connect-with " + dsn
}

func TestCloseDropsByDefault(t *testing.T) {
	provider := &countingProvider{}
	db, err := New(t, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if provider.drops != 1 || provider.terminates != 1 || provider.cleanups != 1 {
		t.Errorf("drops=%d terminates=%d cleanups=%d, want 1 each",
			provider.drops, provider.terminates, provider.cleanups)
	}
}

func TestKeepLeavesDatabaseAndLogsHowToConnect(t *testing.T) {
	provider := &countingProvider{}
	spy := &verboseSpyTB{TB: t}
	db, err := New(spy, provider, nil)
	if err != nil {
		t.Fatal(err)
	}

	db.Keep()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if provider.drops != 0 || provider.terminates != 0 {
		t.Errorf("kept database was dropped: drops=%d terminates=%d", provider.drops, provider.terminates)
	}
	if provider.cleanups != 1 {
		t.Errorf("provider resources not released: cleanups=%d, want 1", provider.cleanups)
	}

	var kept string
	for _, msg := range spy.logs {
		if strings.Contains(msg, "kept database") {
			kept = msg
		}
	}
	if kept == "" {
		t.Fatalf("expected a 'kept database' log line, got %v", spy.logs)
	}
	if !strings.Contains(kept, db.Name()) || !strings.Contains(kept, "connect-with "+db.DSN()) {
		t.Errorf("kept-database log should name the database and give the provider's connect command, got: %s", kept)
	}
}

func TestKeepLogsDSNWhenProviderHasNoInspectCommand(t *testing.T) {
	spy := &verboseSpyTB{TB: t}
	db, err := New(spy, &mockProvider{}, nil) // mockProvider has no InspectCommand
	if err != nil {
		t.Fatal(err)
	}
	db.Keep()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var kept string
	for _, msg := range spy.logs {
		if strings.Contains(msg, "kept database") {
			kept = msg
		}
	}
	if !strings.Contains(kept, db.DSN()) {
		t.Errorf("without an InspectCommander the log should show the DSN, got: %s", kept)
	}
}

func TestDefaultConfigKeepOnFailureFromEnv(t *testing.T) {
	tests := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"no":    false,
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"yes":   true,
		"on":    true,
		" 1 ":   true,
	}
	for value, want := range tests {
		t.Run("value="+strings.TrimSpace(value), func(t *testing.T) {
			t.Setenv(KeepOnFailureEnv, value)
			if got := DefaultConfig().KeepOnFailure; got != want {
				t.Errorf("%s=%q: KeepOnFailure = %v, want %v", KeepOnFailureEnv, value, got, want)
			}
		})
	}
}

func TestWithKeepOnFailure(t *testing.T) {
	t.Setenv(KeepOnFailureEnv, "")
	cfg := DefaultConfig()
	if cfg.KeepOnFailure {
		t.Fatal("KeepOnFailure should be off by default")
	}
	WithKeepOnFailure()(&cfg)
	if !cfg.KeepOnFailure {
		t.Fatal("WithKeepOnFailure should turn KeepOnFailure on")
	}
}
