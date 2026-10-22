package postgres_test

import (
	"context"
	"fmt"
	"testing"
)

// exampleT stands in for the *testing.T a runnable example does not have.
// A zero testing.T would accept t.Cleanup and never run it, so every
// database an example created stayed on the server; exampleT runs the
// cleanups when the example is done. It implements only what the library
// calls; anything else falls through to the nil embedded TB and panics,
// which is the right outcome for an example that strays outside that set.
type exampleT struct {
	testing.TB
	cleanups []func()
}

func newExampleT() *exampleT { return &exampleT{} }

func (t *exampleT) Helper()                  {}
func (t *exampleT) Cleanup(f func())         { t.cleanups = append(t.cleanups, f) }
func (t *exampleT) Context() context.Context { return context.Background() }
func (t *exampleT) Failed() bool             { return false }

// Logf drops the library's verbose logging, which is not part of an
// example's output.
func (t *exampleT) Logf(string, ...any) {}

// Errorf and Fatalf surface failures: printing to stdout breaks the
// example's expected output, so the example fails visibly.
func (t *exampleT) Errorf(format string, args ...any) { fmt.Printf("error: "+format+"\n", args...) }
func (t *exampleT) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
func (t *exampleT) Fatal(args ...any)                 { panic(fmt.Sprint(args...)) }

// done runs the cleanups in reverse order, as testing does for a real T.
func (t *exampleT) done() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}
