package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/narrate"
	"github.com/simoncoombes/agentpane/internal/state"
)

// The alert band (§3.7) and the away-digest (§3.19).
//
// **There is no band region.** §13.2 allocates exactly three regions — standup,
// commentary, tree — and the standup IS the band, grown up: its sticky chip
// carries the band's identity and its body opens with the band's content. This
// file owns that content; narration.go owns the region it is drawn into.
//
// Drawing both was the bug: the ask was stated twice (once as `run: …`, once
// inside the standup's first sentence) and two stacked fixed-height bands above
// and below the tree defeated the geometry guarantee §13.2 exists for.

// Exact copy strings (§3.12).
const (
	copyBandNeedsYou = "⚑ NEEDS YOU"
	copyBandAway     = "⚑ WHILE YOU WERE AWAY"
	copyAskAction    = "answer in the left pane — ⏎ jumps focus"
	copyStallAction  = "o opens the log · y yanks the command"
	copyResolving    = "resolving…"
	// copyResolvingAction is the action for an ask that has already been answered
	// (§3.7.8): there is nothing to press, and the entry clears late because a
	// denial emits no event at all. It is the narrator's own words for the same
	// state, aliased rather than retyped so the region and the standup cannot
	// disagree about it.
	copyResolvingAction = narrate.ActionResolving
	copyLimitedTele     = "limited telemetry"
	copyReadOnly        = "read-only — subagents cannot be killed"
	copyInspectorClose  = "esc close view — agent keeps running"
	copyWatching        = "watching for activity"
	copyWaitingPinned   = "waiting for session "
	copyNoSubagents     = "no subagents"
	copyQueuedTail      = "queued"
	copySinceHint       = "since last event →"
	copyDisconnected    = "⚠ disconnected"
	copyFirstRun        = "no previous runs — waiting for your first prompt"
	copyWiden           = "widen for detail"
	copyPinnedNoRoom    = "␣ pinned (no room)"
	copyDigestDismiss   = "esc dismisses"
)

// bandCap is the §3.7.6 alert cap: oldest first, then "+n more".
const bandCap = 3

// The two §3.7 alert kinds. They are the strings the row prints, so they are
// named rather than spelled at each comparison.
const (
	kindPermission = "permission"
	kindStalled    = "stalled"
)

// bandEntry is one alert-band item: a deduped permission ask or a stalled
// agent (§3.7).
type bandEntry struct {
	agentID   string
	slug      string
	kind      string // kindPermission | kindStalled
	dupes     int
	waited    time.Duration
	what      string // the exact command for an ask; the call state for a stall
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
			agentID: ask.AgentID,
			slug:    v.slugOf(w, ask.AgentID, ask.Description),
			kind:    kindPermission,
			dupes:   ask.DupeCount,
			waited:  now.Sub(ask.RaisedAt),
			// §13.1(b): the DISPLAY copy is flattened — whitespace runs collapsed,
			// control characters gone — because a heredoc's indentation otherwise
			// eats the region's columns and a `python3 -c` newline breaks the
			// frame. `y` still yanks the original bytes (keys.go reads Ask.Command).
			what: "run: " + flattenDisplay(ask.Command),
			// §3.7.8: an answered ask needs nothing from the reader, so its action
			// may not be "answer in the left pane" — that is the region telling
			// them to answer a prompt they have already answered.
			action:    askEntryAction(ask.Resolving),
			ghosts:    ask.FoldedGhosts,
			resolving: ask.Resolving,
		})
	}
	for _, a := range v.orderedAgents(w) {
		if a.Status != state.StatusStuck {
			continue
		}
		// The silence timer is the entry's right-hand column, so the `what` says
		// what is DIFFERENT about this stall — whether a call is still open —
		// rather than repeating the duration a second time on its own row. That
		// is what lets a stall cost one row instead of two, which is the whole
		// difference between the alert fitting the region and not.
		what := "no call open"
		if a.OpenCall != nil {
			what = "◐ in " + a.OpenCall.Tool
		}
		out = append(out, bandEntry{
			agentID: a.ID,
			slug:    v.slugFor(a),
			kind:    kindStalled,
			waited:  now.Sub(a.LastEventAt),
			what:    what,
			action:  copyStallAction,
		})
	}
	return out
}

// askEntryAction is the §3.12 action copy for one permission entry.
func askEntryAction(resolving bool) string {
	if resolving {
		return copyResolvingAction
	}
	return copyAskAction
}

// flattenDisplay is §13.1(b)'s display flattening: strip control characters,
// then collapse every run of whitespace to a single space.
//
// flatten() alone is not enough. It maps a newline to a space, which stops the
// frame from breaking, but a heredoc or an indented multi-line command then
// arrives as one line full of runs of spaces — twenty columns of nothing in a
// region that has sixty-four. Fields+Join is the collapse.
func flattenDisplay(s string) string {
	return strings.Join(strings.Fields(flatten(s)), " ")
}

// bandAlerting reports whether the band has anything to say: a live alert or an
// undismissed digest. It is the one predicate that decides whether the standup
// is carrying the band this frame, so every caller asks the same question.
func bandAlerting(entries []bandEntry, dig *digest) bool {
	return len(entries) > 0 || dig != nil
}

// bandBlocked reports whether the band holds a permission ask the reader is
// still the blocker for — the one state the chip inverts for.
//
// A RESOLVING ask does not count (§3.7.8). Its answer is already in; only the
// outcome is unconfirmed, so there is nothing for the reader to do and §3.10.2's
// reverse bar is spent on "you are the blocker". Ignoring `resolving` here put an
// inverted `⚑ NEEDS YOU` chip directly above `▸ nothing needs you — waiting on
// the event that confirms it`, which is the exact contradiction the stall rule
// below already refuses: a chip contradicting the sentence beneath it is worse
// than either wording on its own. The entry keeps its row, its `resolving…` mark
// and its wait timer; the chip falls to `⚠ WORTH WATCHING`.
func bandBlocked(entries []bandEntry) bool {
	return bandNeedsYou(entries) > 0
}

// bandNeedsYou counts the entries that are genuinely waiting on the reader: a
// permission ask whose answer has not been seen. It is what the header's
// `⚑ n NEEDS YOU` counts, so the header, the chip and the action row cannot
// disagree about whether anything needs doing.
func bandNeedsYou(entries []bandEntry) int {
	n := 0
	for _, e := range entries {
		if e.kind == kindPermission && !e.resolving {
			n++
		}
	}
	return n
}

// bandAlertCount is the number the header's `⚑ n NEEDS YOU` states: every band
// entry except an ask whose answer is already in (§3.7.8).
//
// Counting a resolving ask made the header lead with `⚑ 1 NEEDS YOU` on a frame
// whose every other surface said the opposite, and the header is the first thing
// read. A stall still counts — it is a live band entry, and §3.7 draws it in the
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

// bandChip is the sticky chip: the band's identity, and the clock on its right.
//
// §3.10.2 alternates `⚑ NEEDS YOU` and `⚑ WHILE YOU WERE AWAY`; with neither
// outstanding the chip is the narrator's own run chip, which is what makes this
// one chip rather than two regions' worth.
//
// A stall-only band takes `⚠ WORTH WATCHING` and not `⚑ NEEDS YOU`, which is
// the mock's own rule (`asks ? ⚑ NEEDS YOU : stucks ? ⚠ WORTH WATCHING : …`).
// The reason it matters is that the narrator's first sentence in that scene is
// literally "Nothing needs you right now", and a chip contradicting the sentence
// directly beneath it is worse than either wording on its own. The stall is still
// a band entry, still drawn in the needs-you role, and still closes on §3.12's
// stall action.
func bandChip(st narrate.Standup, entries []bandEntry, dig *digest) (chip, right string) {
	switch {
	case dig != nil:
		return copyBandAway, "· " + formatSince(dig.Dur)
	case bandBlocked(entries):
		return copyBandNeedsYou, st.Clock
	case len(entries) > 0:
		return narrate.ChipWatch, st.Clock
	default:
		return st.Chip, st.Clock
	}
}

// bandChipStyle styles the chip. §3.10.2 permits a reversed background on
// exactly two chips — `⚑ NEEDS YOU` and `⚑ WHILE YOU WERE AWAY` — and on the
// selected row, and nowhere else in the pane. So those two invert and the
// narrator's own chips (a stall's `⚠ WORTH WATCHING`, `✔ ALL CLEAR`) stay bright
// or grey, which is what makes the inversion mean something when it happens.
func bandChipStyle(st narrate.Standup, entries []bandEntry, dig *digest, pal Palette) Style {
	switch {
	case dig != nil, bandBlocked(entries):
		return pal.Chip
	case len(entries) > 0:
		return pal.NeedsYou
	default:
		return chipStyle(st, pal)
	}
}

// bandAction is the §3.12 action copy for the entry that owns the region's one
// action row — permission → `answer in the left pane — ⏎ jumps focus`, stall →
// `o opens the log · y yanks the command`. It replaces the narrator's closing
// line whenever there is an alert, because the narrator writes for the scene and
// §3.12 fixes the wording for the alert.
//
// Empty means "no alert": the standup keeps its own action.
// bandRowsPerEntry is what one band entry costs in rows: its label row plus the
// command row §3.7 requires beneath it. It turns a row budget into a count of
// entries the region can actually seat.
const bandRowsPerEntry = 2

func bandAction(entries []bandEntry) string {
	return bandActionN(entries, bandCap)
}

// bandActionN is bandAction for a region that will draw fewer rows than the
// §3.7.6 cap. renderAlertOnly clips the body to as little as two rows at tight
// heights, so bounding the hand-off by bandCap was still one number too many:
// the row could carry a stall's keys while the stall had no row anywhere on the
// frame. The caller passes what it will actually draw.
func bandActionN(entries []bandEntry, drawn int) string {
	i := bandActionOwnerN(entries, drawn)
	if i < 0 {
		return ""
	}
	return "▸ " + entries[i].action
}

// bandActionOwner is which entry's copy the single action row carries.
//
// The primary (oldest) entry owns it, with one exception: an ask that has already
// been answered (§3.7.8) asks nothing of the reader, so it hands the row to the
// next entry that does. Without that, one answered ask at the top of the band put
// `answer in the left pane` over a band whose only outstanding item was a stall.
// When every entry is an answered ask the primary keeps the row and says so —
// copyResolvingAction is its own action.
//
// The hand-off may only reach an entry the §3.7.6 cap actually DREW. Searching the
// whole slice let three answered asks hand the row to a fourth entry that was
// behind `+n more`: the region showed three `resolving…` rows and then a stall's
// `o opens the log · y yanks the command` for a stall with no row on screen, so
// the keys referred to something the reader could not see. The search is bounded
// by the window, and when nothing inside it wants the row the primary keeps it and
// states its own state instead.
func bandActionOwner(entries []bandEntry) int {
	return bandActionOwnerN(entries, bandCap)
}

func bandActionOwnerN(entries []bandEntry, cap int) int {
	if len(entries) == 0 {
		return -1
	}
	if cap < 1 {
		cap = 1
	}
	drawn := len(entries)
	if drawn > cap {
		drawn = cap
	}
	for i := 0; i < drawn; i++ {
		if e := entries[i]; e.kind == kindPermission && e.resolving {
			continue
		}
		return i
	}
	return 0
}

// bandCoreRows is how many body rows the oldest entry needs to say the two
// things §3.7 puts first: who is blocked, and the exact command they are blocked
// on. Nothing in the band is given up before these — not the separator, not the
// action line, not the folded-ghost count.
func bandCoreRows(entries []bandEntry) int {
	if len(entries) == 0 {
		return 1 // a digest-only band: its first ranked change
	}
	if e := entries[0]; e.kind != kindStalled && e.what != "" {
		return 2 // the label and the command on its own row
	}
	return 1
}

// bandBody renders the band's content as standup body lines (§3.7.1–3.7.8 and
// the §3.19 digest list).
//
// Per entry: the agent, the kind, the `×n` dedupe count, `resolving…` while the
// outcome is unconfirmed, and the counting-up wait timer on the right; then the
// exact command for an ask, or §3.7.2's keys for a stall that is not carrying the
// region's action row; then `+n retry agents folded`. Oldest first, capped at
// bandCap with `+n more` (§3.7.6).
//
// Everything is PartMust: this is "what you must do", which is where §13.2 puts
// it and what makes the clip hint name it honestly when the region overflows.
func bandBody(entries []bandEntry, dig *digest, width int, pal Palette) []bodyLine {
	if !bandAlerting(entries, dig) {
		return nil
	}
	var out []bodyLine
	add := func(segs []seg) {
		out = append(out, bodyLine{truncSegs(segs, width), narrate.PartMust, true})
	}

	shown, more := entries, 0
	if len(shown) > bandCap {
		more = len(shown) - bandCap
		shown = shown[:bandCap]
	}
	// A stall's §3.12 keys are stated exactly once per frame: on the action row
	// when a stall owns it, and otherwise on the first stall entry that needs
	// them — never in both places, and never twice for two stalls.
	owner := bandActionOwner(entries)
	stallKeysStated := owner >= 0 && entries[owner].action == copyStallAction
	for _, e := range shown {
		right := line(seg{formatSince(e.waited), pal.NeedsYou})
		// §3.7.8's mark and the kind compete for the same columns, and at 44 — a
		// shipped width — the kind won by position alone: composeLR truncates the
		// tail, and the tail was the mark, so every narrow frame printed
		// `permission · resolvi…`. The kind is recoverable from the frame (the band
		// has two of them and the action row names which one owns it); the
		// resolution state is recoverable from nothing else on screen. So when the
		// row overflows, the mark moves AHEAD of the kind and the kind takes the
		// truncation instead.
		//
		// An answer was seen, the outcome was not. Denying emits nothing at all, so
		// this clears late and must never read as a verdict. The mark sits on the
		// entry it belongs to; the entry's own action (askEntryAction) is what stops
		// the region still asking for an answer.
		build := func(markFirst bool) []seg {
			out := line(seg{e.slug, pal.NeedsYou})
			if markFirst {
				out = append(out, seg{"  " + copyResolving, pal.NeedsYou})
				out = append(out, seg{" · " + e.kind, pal.Primary})
			} else {
				out = append(out, seg{"  " + e.kind, pal.Primary})
			}
			if e.dupes > 1 {
				out = append(out, seg{fmt.Sprintf(" ×%d", e.dupes), pal.NeedsYou})
			}
			if e.resolving && !markFirst {
				out = append(out, seg{" · " + copyResolving, pal.NeedsYou})
			}
			if e.kind == kindStalled && e.what != "" {
				out = append(out, seg{" · " + e.what, pal.Settled})
			}
			return out
		}
		label := build(false)
		if e.resolving && overflow(label, right, width) > 0 {
			label = build(true)
		}
		add(composeLR(label, right, width))

		if e.kind != kindStalled && e.what != "" {
			// The exact command, on its own full-width row: approving it is the
			// reader's next action, so it is never abbreviated into a sentence.
			add(line(seg{e.what, pal.Primary}))
		}
		if e.kind == kindStalled && !stallKeysStated && e.action != "" {
			// §3.7.2's keys for a stall that is NOT carrying the region's action
			// row. The region has one action row (§13.2) and whatever leads the band
			// owns it, so a stall behind an ask had no way to say what `o` and `y`
			// do — the copy went missing from the frame the moment an ask arrived.
			// It rides its own entry instead, where it survives any lead.
			add(line(seg{e.action, pal.Settled}))
			stallKeysStated = true
		}
		if e.ghosts > 0 {
			add(line(seg{fmt.Sprintf("+%d retry agents folded", e.ghosts), pal.Settled}))
		}
	}
	if more > 0 {
		add(line(seg{fmt.Sprintf("+%d more", more), pal.Settled}))
	}

	if dig != nil {
		for _, d := range dig.Entries {
			add(composeLR(line(
				seg{d.Slug, pal.Primary},
				seg{"  " + d.Kind, pal.Settled},
			), line(seg{d.When, pal.Deep}), width))
		}
		if dig.More > 0 {
			add(line(seg{fmt.Sprintf("+%d more", dig.More), pal.Settled}))
		}
		add(line(seg{copyDigestDismiss, pal.Deep}))
	}
	return out
}
