package testdb

import (
	"context"
	"testing"
)

// renamingProvider hands over a database under its own name, as a provider
// with clones prepared in advance does.
type renamingProvider struct {
	mockProvider
	dropped []string
}

func (p *renamingProvider) CreateDatabaseFromTemplate(ctx context.Context, requested, template string) (string, error) {
	return "ready_" + requested, nil
}

func (p *renamingProvider) DropDatabase(ctx context.Context, name string) error {
	p.dropped = append(p.dropped, name)
	return nil
}

func TestNewUsesTheNameTheProviderReturns(t *testing.T) {
	provider := &renamingProvider{}
	db, err := New(t, provider, nil, WithTemplate("tpl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Name(); len(got) < 6 || got[:6] != "ready_" {
		t.Errorf("Name() = %q, want the provider's name", got)
	}
	if dsn := db.DSN(); dsn == "" || !containsName(dsn, db.Name()) {
		t.Errorf("DSN %q should be for database %q", dsn, db.Name())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(provider.dropped) != 1 || provider.dropped[0] != db.Name() {
		t.Errorf("Close dropped %v, want exactly [%s]", provider.dropped, db.Name())
	}
}

func containsName(dsn, name string) bool {
	for i := 0; i+len(name) <= len(dsn); i++ {
		if dsn[i:i+len(name)] == name {
			return true
		}
	}
	return false
}
