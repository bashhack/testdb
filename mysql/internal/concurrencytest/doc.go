// Package concurrencytest is a test package that mysql's multi-process
// template tests compile and run as separate processes. Each process
// ensures the same template with a small ready pool, clones from it in
// parallel subtests, and releases it. It has no non-test code of its own.
//
// Run on its own without a reachable MySQL server it exits successfully
// without running anything, so a Postgres-only checkout stays green; in CI
// (CI set) a missing server is a failure.
//
// Environment variables set by the driver tests:
//
//	TESTDB_CONC_TEMPLATE      template name to share (default: a standalone name)
//	TESTDB_CONC_FINGERPRINT   fingerprint passed to WithFingerprint, if set
//	TESTDB_CONC_SETUP_LOG     file to append a line to each time setup runs
//	TESTDB_CONC_READY_FILE    file to create once EnsureTemplate has returned
//	TESTDB_CONC_READY_PREFIX  name prefix for prepared databases
//	TESTDB_CONC_HOLD          duration to sleep before running tests, so the
//	                          driver can kill the process while it holds the
//	                          template and prepared databases
package concurrencytest
