package registry

// pidalive_windows.go — liveness without signals.
//
// Windows has no kill(pid,0), so the same question is asked the long way
// round: open a handle to the process and read its exit code. A live process
// reports STILL_ACTIVE; a terminated one whose handle is still open reports
// the code it exited with, which is the case a bare "did OpenProcess succeed"
// check gets wrong.

import "syscall"

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION. It is
// the narrowest right that answers this question, and unlike
// PROCESS_QUERY_INFORMATION it is granted across integrity levels, so an
// elevated `claude` still reads as alive from an unelevated pane. Not in the
// standard library's syscall package, hence the literal.
const (
	processQueryLimitedInformation = 0x1000

	// stillActive is STILL_ACTIVE, the exit code a process that has not
	// exited reports. Also absent from the standard library's syscall
	// package.
	stillActive = 259
)

// pidAlive reports process existence.
//
// Access-denied is treated as alive, mirroring the unix EPERM case: the
// process is real enough for the kernel to refuse us, which is the only thing
// the caller is asking. Every other open failure means no such process.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer syscall.CloseHandle(h) //nolint:errcheck // nothing to do with a close failure here

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		// The handle opened, so the process object exists; a failure to read
		// the code is not evidence that it is gone.
		return true
	}
	return code == stillActive
}
