package testdb_test

import (
	"context"
	"fmt"
	"testing"
)

// exampleT stands in for the *testing.T a runnable example does not have;
// see postgres/example_helpers_test.go for the full story. The examples
// here close their databases by hand, but a real stand-in keeps them honest
// about what they call on t.
type exampleT struct {
	testing.TB
	cleanups []func()
}

func newExampleT() *exampleT { return &exampleT{} }

func (t *exampleT) Helper()                  {}
func (t *exampleT) Cleanup(f func())         { t.cleanups = append(t.cleanups, f) }
func (t *exampleT) Context() context.Context { return context.Background() }
func (t *exampleT) Failed() bool             { return false }
func (t *exampleT) Logf(string, ...any)      {}

func (t *exampleT) Errorf(format string, args ...any) { fmt.Printf("error: "+format+"\n", args...) }
func (t *exampleT) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
func (t *exampleT) Fatal(args ...any)                 { panic(fmt.Sprint(args...)) }

func (t *exampleT) done() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}
