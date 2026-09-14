//go:build !windows

package registry

// pidalive_unix.go — liveness on every platform with signals.
//
// The registry's PID files outlive the processes that wrote them (a crashed
// CLI never cleans up), so every row is checked against the OS before it
// reaches the UI. Splitting this one function per platform is what keeps the
// rest of the package portable: nothing else here touches syscall.

import "syscall"

// pidAlive reports process existence: kill(pid,0) succeeds or fails with
// EPERM for a live pid, ESRCH for a dead one.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
