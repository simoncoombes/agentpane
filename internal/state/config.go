package state

import "time"

// Config carries every knob the machine needs from SPEC §2.5, §3.5 and
// §3.7.8. It is self-contained: internal/config maps TOML keys onto it.
//
// WatchNeverExempt is taken verbatim (false is a legal setting), so callers
// wanting the spec defaults must start from DefaultConfig rather than a zero
// Config.
type Config struct {
	// StuckAfter is the silence threshold when no exemption applies (§2.5).
	StuckAfter time.Duration
	// LongRunningAfter is the silence threshold while an allowlisted
	// command's call is open (§2.5).
	LongRunningAfter time.Duration
	// LongRunningAllow lists command words that mark a call long-running.
	LongRunningAllow []string
	// WatchNeverExempt keeps a quiet watch process (--watch, -w, nodemon,
	// tail -f) on the StuckAfter threshold regardless of the allowlist.
	WatchNeverExempt bool
	// DecayAfter collapses a done agent to one line (§3.5).
	DecayAfter time.Duration
	// UnknownAfter moves a silent unsettled agent to unknown (§2.4).
	UnknownAfter time.Duration
	// ResolveGrace clears a resolving ask that never got a confirming
	// event (§3.7.8: denial is silent).
	ResolveGrace time.Duration
}

// There is deliberately no quiet grace period. An agent that has shown nothing
// at all does not earn a tree row by outliving a timer: that rule rendered the
// ~90s phantoms of session 6d53f05d as unnamed rows and then deleted each row
// the moment it settled. Row eligibility is evidence-based and latched — see
// Machine.quiet.

// DefaultConfig returns the SPEC defaults.
func DefaultConfig() Config {
	return Config{
		StuckAfter:       45 * time.Second,
		LongRunningAfter: 3 * time.Minute,
		LongRunningAllow: []string{"test", "build", "install", "compile", "bundle", "migrate"},
		WatchNeverExempt: true,
		DecayAfter:       30 * time.Second,
		UnknownAfter:     10 * time.Minute,
		ResolveGrace:     5 * time.Second,
	}
}

// normalized fills non-positive durations and a nil allowlist with defaults
// so a partially-populated Config never produces a machine with zero
// thresholds. WatchNeverExempt is left as given.
func (c Config) normalized() Config {
	d := DefaultConfig()
	if c.StuckAfter <= 0 {
		c.StuckAfter = d.StuckAfter
	}
	if c.LongRunningAfter <= 0 {
		c.LongRunningAfter = d.LongRunningAfter
	}
	if c.LongRunningAllow == nil {
		c.LongRunningAllow = d.LongRunningAllow
	}
	if c.DecayAfter <= 0 {
		c.DecayAfter = d.DecayAfter
	}
	if c.UnknownAfter <= 0 {
		c.UnknownAfter = d.UnknownAfter
	}
	if c.ResolveGrace <= 0 {
		c.ResolveGrace = d.ResolveGrace
	}
	return c
}
