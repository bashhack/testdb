package postgres

// These tests prove the template lock protocol with real, separate
// processes: the internal/concurrencytest package is compiled to a test
// binary and run several times at once, or killed mid-use, against one
// template on the shared server.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashhack/testdb"
)

func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func adminProvider(t *testing.T) *PostgresProvider {
	t.Helper()
	ctx := context.Background()
	p := &PostgresProvider{}
	if err := p.Initialize(ctx, testdb.Config{}); err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}
	t.Cleanup(func() { _ = p.Cleanup(ctx) })
	return p
}

// buildConcurrencyBinary compiles internal/concurrencytest to a test binary
// that can be run as independent processes.
func buildConcurrencyBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("compiles and runs separate test processes")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "concurrencytest.test")
	cmd := exec.Command(goBin, "test", "-c", "-o", bin, "./internal/concurrencytest")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile concurrencytest: %v\n%s", err, out)
	}
	return bin
}

// concurrencyProcess prepares one run of the test binary. It runs in the
// package's directory so the fixture's relative path resolves.
func concurrencyProcess(bin string, env ...string) *exec.Cmd {
	cmd := exec.Command(bin, "-test.count=1")
	cmd.Dir = filepath.Join("internal", "concurrencytest")
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

// useLockHolders counts sessions holding the template's use lock, shared or
// exclusive, straight from pg_locks.
func useLockHolders(t *testing.T, p *PostgresProvider, name string) int {
	t.Helper()
	var n int
	err := p.conn.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1::oid AND objid = $2::oid`,
		lockClassUse, templateLockKey(name)).Scan(&n)
	if err != nil {
		t.Fatalf("query pg_locks: %v", err)
	}
	return n
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func assertTemplateGone(t *testing.T, p *PostgresProvider, name string) {
	t.Helper()
	exists, err := p.TemplateExists(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("template %q was left behind", name)
	}
}

func TestTemplateConcurrentProcesses(t *testing.T) {
	p := adminProvider(t)
	bin := buildConcurrencyBinary(t)
	name := uniqueName(t, "testdb_conc")
	setupLog := filepath.Join(t.TempDir(), "setup.log")

	// Each process keeps the template for a while after ensuring it, so
	// all of them overlap even if one is slow to start. Without overlap a
	// late process would legitimately find the template dropped and build
	// it again, and "setup ran once" would be down to timing.
	const procs = 4
	outs := make([][]byte, procs)
	errs := make([]error, procs)
	var wg sync.WaitGroup
	for i := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := concurrencyProcess(bin,
				"TESTDB_CONC_TEMPLATE="+name,
				"TESTDB_CONC_SETUP_LOG="+setupLog,
				"TESTDB_CONC_FINGERPRINT=v1",
				"TESTDB_CONC_HOLD=2s")
			outs[i], errs[i] = cmd.CombinedOutput()
		}()
	}
	wg.Wait()

	for i := range procs {
		if errs[i] != nil {
			t.Errorf("process %d failed: %v\n%s", i, errs[i], outs[i])
		}
	}
	if n := countLines(t, setupLog); n != 1 {
		t.Errorf("setup ran %d times across %d processes, want 1", n, procs)
	}
	assertTemplateGone(t, p, name)
	if n := useLockHolders(t, p, name); n != 0 {
		t.Errorf("%d sessions still hold the use lock", n)
	}
}

// TestTemplateTwoLastOutProcesses pins the interleaving where two
// processes both end up as "last out": both have let go of their shared
// lock, one drops the template and closes, and the other, which was waiting
// for the build lock, must notice the template is gone rather than fail.
func TestTemplateTwoLastOutProcesses(t *testing.T) {
	ctx := context.Background()
	p := adminProvider(t)
	name := uniqueName(t, "testdb_lastout")

	first, err := EnsureTemplate(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureTemplate(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Both handles' connections are closed by the calls below; this
		// only matters if the test fails early
		if err := errors.Join(first.provider.Cleanup(ctx), second.provider.Cleanup(ctx)); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	// Both let go of the template before either checks whether it is last
	if err := first.provider.unlockUseShared(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := second.provider.unlockUseShared(ctx, name); err != nil {
		t.Fatal(err)
	}

	if err := first.provider.dropIfLastOut(ctx, name); err != nil {
		t.Fatalf("first last-out: %v", err)
	}
	assertTemplateGone(t, p, name)

	if err := second.provider.dropIfLastOut(ctx, name); err != nil {
		t.Fatalf("second last-out must tolerate the template being gone: %v", err)
	}
	if n := useLockHolders(t, p, name); n != 0 {
		t.Errorf("%d sessions still hold the use lock", n)
	}
}

func TestTemplateReleasedByKilledProcess(t *testing.T) {
	p := adminProvider(t)
	bin := buildConcurrencyBinary(t)
	name := uniqueName(t, "testdb_conc_kill")
	dir := t.TempDir()
	setupLog := filepath.Join(dir, "setup.log")
	ready := filepath.Join(dir, "ready")

	// A process that takes the template and then just sits on it
	var holderOut bytes.Buffer
	holder := concurrencyProcess(bin,
		"TESTDB_CONC_TEMPLATE="+name,
		"TESTDB_CONC_SETUP_LOG="+setupLog,
		"TESTDB_CONC_FINGERPRINT=v1",
		"TESTDB_CONC_READY_FILE="+ready,
		"TESTDB_CONC_HOLD=5m")
	holder.Stdout = &holderOut
	holder.Stderr = &holderOut
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill() })

	waitFor(t, 60*time.Second, "holder to ensure the template", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	if n := useLockHolders(t, p, name); n != 1 {
		t.Fatalf("%d sessions hold the use lock while the holder runs, want 1\n%s", n, holderOut.String())
	}

	// Kill it outright: no Release, no cleanup of any kind
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err == nil {
		t.Fatal("holder exited cleanly; the test needs it to die without releasing")
	}

	// Postgres notices the dead connection and lets go of its lock
	waitFor(t, 10*time.Second, "the killed process's lock to be released", func() bool {
		return useLockHolders(t, p, name) == 0
	})

	// Another process can still use the template, reusing rather than
	// rebuilding it, and cleans it up because it is now the last one out
	out, err := concurrencyProcess(bin,
		"TESTDB_CONC_TEMPLATE="+name,
		"TESTDB_CONC_SETUP_LOG="+setupLog,
		"TESTDB_CONC_FINGERPRINT=v1").CombinedOutput()
	if err != nil {
		t.Fatalf("process after the kill failed: %v\n%s", err, out)
	}
	if n := countLines(t, setupLog); n != 1 {
		t.Errorf("setup ran %d times, want 1 (the survivor should reuse the template)", n)
	}
	assertTemplateGone(t, p, name)
}
