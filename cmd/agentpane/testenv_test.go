package main

import "testing"

// testenv_test.go — redirecting a test away from the developer's real home
// and temp directory, on every platform.
//
// t.Setenv("HOME", …) does nothing on Windows: os.UserHomeDir reads
// %USERPROFILE% there, and os.TempDir reads %TMP%/%TEMP%. A test that sets
// only the unix spelling does not fail loudly — it quietly reads the real
// home of whoever is running it, which is both a wrong answer and, for the
// tests that write, a thing no test should be doing.

// setFakeHome points the home-directory lookup at dir for the test's
// duration, in both spellings.
func setFakeHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// setFakeTemp points os.TempDir at dir, in every spelling it consults. The
// value is passed through verbatim, so a caller that deliberately appends a
// trailing separator still gets to test what that does.
func setFakeTemp(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("TMPDIR", dir) // unix
	t.Setenv("TMP", dir)    // windows, first choice
	t.Setenv("TEMP", dir)   // windows, second
}
