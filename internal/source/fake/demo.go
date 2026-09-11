package fake

import "time"

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
