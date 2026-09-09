package ui

import (
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// Alert entries (§3.7).
//
// **There is no alert region.** The band, the standup and the commentary that
// used to sit below the tree are gone; what survives is the computation behind
// them, because two surfaces still need to know who is blocked: the header's
// `⚑ n NEEDS YOU` count and the §3.9 condensed screen's single alert line.
//
// Nothing here renders a region. bandEntries answers "which agents are waiting
// on the reader, and for how long"; the callers decide what to do with that.

// Exact copy strings (§3.12).
const (
	copyLimitedTele    = "limited telemetry"
	copyReadOnly       = "read-only — subagents cannot be killed"
	copyInspectorClose = "esc close view — agent keeps running"
	copyWatching       = "watching for activity"
	copyWaitingPinned  = "waiting for session "
	copyNoSubagents    = "no subagents"
	copyQueuedTail     = "queued"
	copySinceHint      = "since last event →"
	copyDisconnected   = "⚠ disconnected"
	copyFirstRun       = "no previous runs — waiting for your first prompt"
	copyWiden          = "widen for detail"
	copyPinnedNoRoom   = "␣ pinned (no room)"
	copyDigestDismiss  = "esc dismisses"
)

// The two §3.7 alert kinds. They are the strings the row prints, so they are
// named rather than spelled at each comparison.
const (
	kindPermission = "permission"
	kindStalled    = "stalled"
)

// bandEntry is one alert item: a deduped permission ask or a stalled agent
// (§3.7).
type bandEntry struct {
	agentID   string
	slug      string
	kind      string // kindPermission | kindStalled
	dupes     int
	waited    time.Duration
	resolving bool
}

// bandEntries builds the alert list: asks oldest-first, then stalled agents
// (§3.7.1). Teammates never appear (§3.15).
func bandEntries(w state.World, v *UIState, now time.Time) []bandEntry {
	var out []bandEntry
	for _, ask := range w.Asks {
		out = append(out, bandEntry{
			agentID:   ask.AgentID,
			slug:      v.slugOf(w, ask.AgentID, ask.Description),
			kind:      kindPermission,
			dupes:     ask.DupeCount,
			waited:    now.Sub(ask.RaisedAt),
			resolving: ask.Resolving,
		})
	}
	for _, a := range v.orderedAgents(w) {
		if a.Status != state.StatusStuck {
			continue
		}
		out = append(out, bandEntry{
			agentID: a.ID,
			slug:    v.slugFor(a),
			kind:    kindStalled,
			waited:  now.Sub(a.LastEventAt),
		})
	}
	return out
}

// bandAlertCount is the number the header's `⚑ n NEEDS YOU` states: every alert
// entry except an ask whose answer is already in (§3.7.8).
//
// Counting a resolving ask made the header lead with `⚑ 1 NEEDS YOU` on a frame
// whose every other surface said the opposite, and the header is the first thing
// read. A stall still counts — it is a live entry, and §3.7 draws it in the
// needs-you role — but an answered ask is not an outstanding anything.
func bandAlertCount(entries []bandEntry) int {
	n := 0
	for _, e := range entries {
		if e.kind == kindPermission && e.resolving {
			continue
		}
		n++
	}
	return n
}
