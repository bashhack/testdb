// Package concurrencytest is a test package that postgres's multi-process
// template tests compile and run as separate processes. Each process ensures
// the same template, clones from it in parallel subtests, and releases it.
// It has no non-test code of its own.
//
// Environment variables set by the driver tests:
//
//	TESTDB_CONC_TEMPLATE     template name to share (default: a standalone name)
//	TESTDB_CONC_FINGERPRINT  fingerprint passed to WithFingerprint, if set
//	TESTDB_CONC_SETUP_LOG    file to append a line to each time setup runs
//	TESTDB_CONC_READY_FILE   file to create once EnsureTemplate has returned
//	TESTDB_CONC_HOLD         duration to sleep before running tests, so the
//	                         driver can kill the process while it holds the
//	                         template
package concurrencytest
