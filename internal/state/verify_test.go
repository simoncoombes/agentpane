package state

import (
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

func withSays(s string) func(*event.Event) {
	return func(e *event.Event) { e.Says = s }
}

// TestVerifySplitsOnExitCode is §13.3 Q6 in the machine: nothing while the check
// is open, VerifyOK on a zero exit, VerifyFailed on a non-zero one, and the
// latest outcome wins — an agent whose suite passed and then broke is not
// verified.
func TestVerifySplitsOnExitCode(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")

	// Open: no outcome exists yet, so the marker says nothing.
	out := f.apply(time.Second, event.ToolStart, "a1", withTool("Bash", "pnpm test"))
	if hasKind(out, event.VerifyObserved) {
		t.Fatalf("VerifyObserved while the check is still open: %v", out)
	}
	if got := agentByID(f.m.Snapshot(), "a1").Verify; got != VerifyNone {
		t.Fatalf("Verify = %v on an open check, want none", got)
	}

	// Non-zero exit: failed, and no derived event claims otherwise.
	out = f.apply(2*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm test"), withExit(1))
	if hasKind(out, event.VerifyObserved) {
		t.Errorf("VerifyObserved for a FAILING check: %v", out)
	}
	a := agentByID(f.m.Snapshot(), "a1")
	if a.Verify != VerifyFailed {
		t.Errorf("Verify = %v after a non-zero exit, want failed", a.Verify)
	}
	if a.Verified {
		t.Error("Verified is true after a failing check")
	}

	// Zero exit: verified, announced once.
	out = f.apply(3*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm test"), withExit(0))
	if !hasKind(out, event.VerifyObserved) {
		t.Errorf("no VerifyObserved for a passing check: %v", out)
	}
	if a := agentByID(f.m.Snapshot(), "a1"); a.Verify != VerifyOK || !a.Verified {
		t.Errorf("Verify = %v, Verified = %v after a zero exit", a.Verify, a.Verified)
	}

	// Same outcome again: the state is unchanged and nothing is re-announced.
	out = f.apply(4*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm test"), withExit(0))
	if hasKind(out, event.VerifyObserved) {
		t.Error("VerifyObserved repeated for an unchanged outcome")
	}

	// It broke again: the marker follows the newest evidence, not the first.
	f.apply(5*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm test"), withErr("2 failed"))
	if got := agentByID(f.m.Snapshot(), "a1").Verify; got != VerifyFailed {
		t.Errorf("Verify = %v after the suite broke again, want failed", got)
	}
}

// The tool's description improves WHICH commands count (§13.3 Q6), but intent is
// not evidence: a description that merely mentions tests earns nothing.
func TestVerifyUsesTheDescriptionButNotIntent(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		cmd    string
		says   string
		want   VerifyState
		reason string
	}{
		{
			name: "opaque command, description says it ran the suite",
			tool: "Bash", cmd: "pnpm -s ci:all", says: "Run the auth suite once",
			want:   VerifyOK,
			reason: "the description is the only thing naming this a check",
		},
		{
			name: "description is about tests, command is not a check",
			tool: "Bash", cmd: "git commit -m wip", says: "Fix the failing tests",
			want:   VerifyNone,
			reason: "intent is not evidence — nothing was verified here",
		},
		{
			name: "writing a test is not running one",
			tool: "Bash", cmd: "cp t.ts t2.ts", says: "Write a test for the parser",
			want:   VerifyNone,
			reason: "work about tests is not a check",
		},
		{
			name: "command alone still counts",
			tool: "Bash", cmd: "pnpm typecheck", says: "",
			want:   VerifyOK,
			reason: "the §2.2 command heuristic is unchanged",
		},
		{
			name: "only a shell command can verify",
			tool: "Read", cmd: "src/auth/session.test.ts", says: "Run the auth suite",
			want:   VerifyNone,
			reason: "reading a test file verifies nothing",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFix()
			f.spawnRun(0, "a1")
			f.apply(time.Second, event.ToolStart, "a1", withTool(c.tool, c.cmd), withSays(c.says))
			f.apply(2*time.Second, event.ToolEnd, "a1", withTool(c.tool, c.cmd), withSays(c.says), withExit(0))
			if got := agentByID(f.m.Snapshot(), "a1").Verify; got != c.want {
				t.Errorf("Verify = %v, want %v — %s", got, c.want, c.reason)
			}
		})
	}
}
