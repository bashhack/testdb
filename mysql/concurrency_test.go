package mysql

// These tests prove the MySQL template protocol with real, separate
// processes: the internal/concurrencytest package is compiled to a test
// binary and run several times at once, or killed while holding the
// template and prepared databases, against one template on the shared
// server.

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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

// concurrencyProcess prepares one run of the test binary, in the package's
// directory so the fixture's relative path resolves.
func concurrencyProcess(bin string, env ...string) *exec.Cmd {
	cmd := exec.Command(bin, "-test.count=1")
	cmd.Dir = filepath.Join("internal", "concurrencytest")
	cmd.Env = append(os.Environ(), env...)
	return cmd
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

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func schemaExists(t *testing.T, admin *sql.DB, name string) bool {
	t.Helper()
	return scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", name) == 1
}

func schemasLike(t *testing.T, admin *sql.DB, pattern string) int64 {
	t.Helper()
	return scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE ?", pattern)
}

func TestTemplateConcurrentProcesses(t *testing.T) {
	dsn := adminDSNOrSkip(t)
	admin := openAdmin(t, dsn)
	bin := buildConcurrencyBinary(t)
	name := uniqueName(t, "mysql_conc")
	readyPrefix := uniqueName(t, "conc_ready")
	setupLog := filepath.Join(t.TempDir(), "setup.log")

	// Each process keeps the template for a while after ensuring it, so
	// all of them overlap even if one is slow to start; without overlap a
	// late process would legitimately rebuild and "setup ran once" would be
	// down to timing.
	const procs = 4
	outs := make([][]byte, procs)
	errs := make([]error, procs)
	var wg sync.WaitGroup
	for i := range procs {
		wg.Go(func() {
			cmd := concurrencyProcess(bin,
				"TESTDB_CONC_TEMPLATE="+name,
				"TESTDB_CONC_SETUP_LOG="+setupLog,
				"TESTDB_CONC_FINGERPRINT=v1",
				"TESTDB_CONC_READY_PREFIX="+readyPrefix,
				"TESTDB_CONC_HOLD=2s")
			outs[i], errs[i] = cmd.CombinedOutput()
		})
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
	if schemaExists(t, admin, name) {
		t.Errorf("template %s was left behind", name)
	}
	if n := schemasLike(t, admin, strings.ReplaceAll(readyPrefix, "_", `\_`)+`\_%`); n != 0 {
		t.Errorf("%d prepared databases were left behind", n)
	}
}

func TestTemplateSurvivesKilledProcess(t *testing.T) {
	dsn := adminDSNOrSkip(t)
	admin := openAdmin(t, dsn)
	bin := buildConcurrencyBinary(t)
	name := uniqueName(t, "mysql_conc_kill")
	readyPrefix := uniqueName(t, "kill_ready")
	dir := t.TempDir()
	setupLog := filepath.Join(dir, "setup.log")
	ready := filepath.Join(dir, "ready")
	t.Cleanup(func() {
		// Whatever the test concludes, do not leave the server dirty
		if schemaExists(t, admin, name) {
			if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+quoteIdentifier(name)); err != nil {
				t.Error(err)
			}
		}
	})

	// A process that takes the template, fills its pool, and then just sits
	var holderOut bytes.Buffer
	holder := concurrencyProcess(bin,
		"TESTDB_CONC_TEMPLATE="+name,
		"TESTDB_CONC_SETUP_LOG="+setupLog,
		"TESTDB_CONC_FINGERPRINT=v1",
		"TESTDB_CONC_READY_PREFIX="+readyPrefix,
		"TESTDB_CONC_READY_FILE="+ready,
		"TESTDB_CONC_HOLD=5m")
	holder.Stdout = &holderOut
	holder.Stderr = &holderOut
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill() })

	waitUntil(t, 60*time.Second, "holder to ensure the template", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	users := qualify(name, bookkeepingPrefix+"users")
	readyTable := qualify(name, bookkeepingPrefix+"ready")
	waitUntil(t, 30*time.Second, "holder's pool to fill", func() bool {
		return scanInt(t, admin, "SELECT COUNT(*) FROM "+readyTable) == 2
	})
	if n := scanInt(t, admin, "SELECT COUNT(*) FROM "+users); n != 1 {
		t.Fatalf("%d template users registered while the holder runs, want 1\n%s", n, holderOut.String())
	}
	var holderConn uint64
	if err := admin.QueryRowContext(context.Background(), "SELECT connection_id FROM "+users).Scan(&holderConn); err != nil {
		t.Fatal(err)
	}

	// Kill it outright: no Release, prepared databases left on the server
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err == nil {
		t.Fatal("holder exited cleanly; the test needs it to die without releasing")
	}
	// The server notices the dead connection
	waitUntil(t, 10*time.Second, "the killed process's session to end", func() bool {
		return scanInt(t, admin, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE id = ?", holderConn) == 0
	})
	if n := schemasLike(t, admin, strings.ReplaceAll(readyPrefix, "_", `\_`)+`\_%`); n != 2 {
		t.Fatalf("expected the dead process's 2 prepared databases to still exist before the sweep, found %d", n)
	}

	// A later process reuses the template (setup does not run again),
	// sweeps the dead process's prepared databases, and, being the last
	// live user, drops the template when it is done
	out, err := concurrencyProcess(bin,
		"TESTDB_CONC_TEMPLATE="+name,
		"TESTDB_CONC_SETUP_LOG="+setupLog,
		"TESTDB_CONC_FINGERPRINT=v1",
		"TESTDB_CONC_READY_PREFIX="+readyPrefix).CombinedOutput()
	if err != nil {
		t.Fatalf("process after the kill failed: %v\n%s", err, out)
	}
	if n := countLines(t, setupLog); n != 1 {
		t.Errorf("setup ran %d times, want 1 (the survivor should reuse the template)", n)
	}
	if schemaExists(t, admin, name) {
		t.Errorf("template %s was left behind: a dead process kept it alive", name)
	}
	if n := schemasLike(t, admin, strings.ReplaceAll(readyPrefix, "_", `\_`)+`\_%`); n != 0 {
		t.Errorf("%d prepared databases of the dead process were not swept", n)
	}
}
