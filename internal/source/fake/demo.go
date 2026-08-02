package fake

import "time"

// RunAgent is one subagent line in a persisted run summary (§2.8).
type RunAgent struct {
	Name     string
	Duration time.Duration
	Status   string
	Tokens   int
}

// RunSummary is one persisted run for the idle screen and history browsing
// (§2.8, §3.8). Plain data; the UI renders it verbatim.
type RunSummary struct {
	ID          string
	Start       time.Time
	End         time.Time
	Agents      []RunAgent
	Tokens      int
	Outcome     string
	Contentions int
	Stalls      int
}

// SessionInfo is one row of the idle screen's session list (§3.8a).
type SessionInfo struct {
	ID       string
	ShortID  string
	Dir      string
	Live     bool
	Idle     bool
	Attached bool
	Agents   int
	EndedAgo time.Duration
}

// DemoRunHistory returns the three runs of history the demo browses with
// [ and ] (§2.10), newest first. The newest run is the §3.8 idle mock:
// 8 subagents, 4m12s, 611k tokens, 1 stalled. Fresh slices per call.
func DemoRunHistory() []RunSummary {
	run1End := epoch.Add(-14 * time.Minute)
	run2End := epoch.Add(-62 * time.Minute)
	run3End := epoch.Add(-3 * time.Hour)
	return []RunSummary{
		{
			ID:    "3f9c",
			Start: run1End.Add(-(4*time.Minute + 12*time.Second)),
			End:   run1End,
			Agents: []RunAgent{
				{Name: "migrate-writer", Duration: 2*time.Minute + 32*time.Second, Status: "done", Tokens: 118000},
				{Name: "api-types", Duration: 2 * time.Minute, Status: "done", Tokens: 84000},
				{Name: "explore-schema", Duration: 1*time.Minute + 28*time.Second, Status: "done", Tokens: 61000},
				{Name: "test-runner", Duration: 3*time.Minute + 4*time.Second, Status: "done", Tokens: 97000},
				{Name: "typecheck", Duration: 36 * time.Second, Status: "done", Tokens: 22000},
				{Name: "docs-writer", Duration: 1*time.Minute + 12*time.Second, Status: "done", Tokens: 45000},
				{Name: "lint-runner", Duration: 58 * time.Second, Status: "done", Tokens: 39000},
				{Name: "schema-audit", Duration: 2*time.Minute + 20*time.Second, Status: "done", Tokens: 145000},
			},
			Tokens:      611000,
			Outcome:     "all merged",
			Contentions: 1,
			Stalls:      1,
		},
		{
			ID:    "b7e2",
			Start: run2End.Add(-(2*time.Minute + 40*time.Second)),
			End:   run2End,
			Agents: []RunAgent{
				{Name: "port-auth-routes", Duration: 1*time.Minute + 50*time.Second, Status: "done", Tokens: 88000},
				{Name: "port-user-routes", Duration: 1*time.Minute + 42*time.Second, Status: "done", Tokens: 74000},
				{Name: "fix-flaky-login", Duration: 2*time.Minute + 28*time.Second, Status: "done", Tokens: 96000},
				{Name: "update-fixtures", Duration: 44 * time.Second, Status: "done", Tokens: 26000},
			},
			Tokens:  284000,
			Outcome: "all merged",
		},
		{
			ID:    "9d04",
			Start: run3End.Add(-(1*time.Minute + 5*time.Second)),
			End:   run3End,
			Agents: []RunAgent{
				{Name: "explore-repo", Duration: 58 * time.Second, Status: "done", Tokens: 64000},
				{Name: "write-readme", Duration: 1*time.Minute + 2*time.Second, Status: "done", Tokens: 41000},
				{Name: "check-ci-config", Duration: 31 * time.Second, Status: "done", Tokens: 16000},
			},
			Tokens:  121000,
			Outcome: "session ended",
		},
	}
}

// DemoSessions returns the three sessions the idle screen lists (§3.8a):
// the attached live session, a second live-but-idle session in the same
// directory, and an ended one elsewhere. Fresh slice per call.
func DemoSessions() []SessionInfo {
	return []SessionInfo{
		{
			ID:       sessionID,
			ShortID:  "3f9c",
			Dir:      "~/src/api",
			Live:     true,
			Attached: true,
			Agents:   8,
		},
		{
			ID:      "2a71d0f3-98b6-4c2e-a1d4-56789abcdef0",
			ShortID: "2a71",
			Dir:     "~/src/api",
			Live:    true,
			Idle:    true,
		},
		{
			ID:       "1c08e5a7-23b4-4f6d-8c9e-0123456789ab",
			ShortID:  "1c08",
			Dir:      "~/src/web",
			EndedAgo: 12 * time.Minute,
		},
	}
}
