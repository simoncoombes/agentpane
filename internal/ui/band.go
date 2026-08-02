package ui

import (
	"fmt"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// Exact copy strings (§3.12).
const (
	copyBandNeedsYou   = "⚑ NEEDS YOU"
	copyBandAway       = "⚑ WHILE YOU WERE AWAY"
	copyAskAction      = "answer in the left pane — ⏎ jumps focus"
	copyStallAction    = "o opens the log · y yanks the command"
	copyResolving      = "resolving…"
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
)

// bandCap is the §3.7.6 alert cap: oldest first, then "+n more".
const bandCap = 3

// bandEntry is one alert-band item: a deduped permission ask or a stalled
// agent (§3.7).
type bandEntry struct {
	agentID   string
	slug      string
	kind      string // "permission" | "stalled"
	dupes     int
	waited    time.Duration
	what      string
	action    string
	ghosts    int
	resolving bool
}

// bandEntries builds the band content: asks oldest-first, then stalled
// agents (§3.7.1). Teammates never appear (§3.15).
func bandEntries(w state.World, v *UIState, now time.Time) []bandEntry {
	var out []bandEntry
	for _, ask := range w.Asks {
		out = append(out, bandEntry{
			agentID:   ask.AgentID,
			slug:      v.slugOf(w, ask.AgentID, ask.Description),
			kind:      "permission",
			dupes:     ask.DupeCount,
			waited:    now.Sub(ask.RaisedAt),
			what:      "run: " + ask.Command,
			action:    copyAskAction,
			ghosts:    ask.FoldedGhosts,
			resolving: ask.Resolving,
		})
	}
	for _, a := range v.orderedAgents(w) {
		if a.Status != state.StatusStuck {
			continue
		}
		silent := now.Sub(a.LastEventAt)
		what := fmt.Sprintf("no call open · silent %s", formatSince(silent))
		if a.OpenCall != nil {
			what = fmt.Sprintf("◐ in %s · quiet %s", a.OpenCall.Tool, formatSince(silent))
		}
		out = append(out, bandEntry{
			agentID: a.ID,
			slug:    v.slugFor(a),
			kind:    "stalled",
			waited:  silent,
			what:    what,
			action:  copyStallAction,
		})
	}
	return out
}

// renderBand renders the §3.7 alert band (and its §3.19 away-digest form) as
// styled lines of exactly width columns. selected reverses the chip line when
// the band pseudo-row is selected.
func renderBand(entries []bandEntry, v *UIState, width int, selected bool, pal Palette) [][]seg {
	dig := v.Digest
	if len(entries) == 0 && dig == nil {
		return nil
	}

	var lines [][]seg

	chip := copyBandNeedsYou
	var right []seg
	if dig != nil {
		chip = copyBandAway
		right = line(seg{"· " + formatSince(dig.Dur), pal.Deep})
	}
	head := composeLR(line(seg{chip, pal.Chip}), right, width)
	if selected {
		head = reverseLine(head)
	}
	lines = append(lines, head)

	shown := entries
	more := 0
	if len(shown) > bandCap {
		more = len(shown) - bandCap
		shown = shown[:bandCap]
	}
	for _, e := range shown {
		label := line(
			seg{e.slug, pal.NeedsYou},
			seg{"  " + e.kind, pal.Primary},
		)
		if e.dupes > 1 {
			label = append(label, seg{fmt.Sprintf(" ×%d", e.dupes), pal.NeedsYou})
		}
		lines = append(lines, composeLR(label,
			line(seg{formatSince(e.waited), pal.NeedsYou}), width))
		lines = append(lines, truncSegs(line(seg{e.what, pal.Primary}), width))
		action := e.action
		if e.resolving {
			action = copyResolving
		}
		lines = append(lines, truncSegs(line(seg{action, pal.Deep}), width))
		if e.ghosts > 0 {
			lines = append(lines, truncSegs(line(
				seg{fmt.Sprintf("+%d retry agents folded", e.ghosts), pal.Settled}), width))
		}
	}
	if more > 0 {
		lines = append(lines, line(seg{fmt.Sprintf("+%d more", more), pal.Settled}))
	}

	if dig != nil {
		for _, d := range dig.Entries {
			left := line(
				seg{d.Slug, pal.Primary},
				seg{"  " + d.Kind, pal.Settled},
			)
			lines = append(lines, composeLR(left, line(seg{d.When, pal.Deep}), width))
		}
		if dig.More > 0 {
			lines = append(lines, line(seg{fmt.Sprintf("+%d more", dig.More), pal.Settled}))
		}
		lines = append(lines, line(seg{"esc dismisses", pal.Deep}))
	}
	return lines
}
