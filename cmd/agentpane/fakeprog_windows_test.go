package main

import "testing"

// fakeprog_windows_test.go — there is no Windows stand-in, and the tests that
// want one skip rather than pretend.
//
// They work by pointing a command chain at a shell script and reading back
// what it was passed. A .bat equivalent would have to carry a forty-line
// AppleScript through batch's quoting rules as a single argument, which is a
// great deal of machinery for testing the macOS path on a machine that does
// not have macOS. Every one of these tests runs on the ubuntu and macos
// runners; this job exists for the code that only compiles here.

func writeFakeProgram(t *testing.T, path, body string) {
	t.Helper()
	t.Skip("drives a fake program through /bin/sh; covered on the unix runners")
}

func alwaysFails() string { return "" }
