package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// idleBarCells is the widest run-history bar (§3.8).
const idleBarCells = 12

// renderIdle renders the §3.8 idle screen body (everything between the header
// rule and the bottom watching line, which render.go owns as the footer row). ended freezes the summary per §3.9 "session ended". It returns
// the selection/hit-test id of each line alongside it, because the suppressed
// -agents notice is a selectable row here exactly as it is on the tree.
func renderIdle(w state.World, v *UIState, width int, now time.Time, pal Palette) ([][]seg, []string) {
	var lines [][]seg
	var ids []string
	add := func(id string, ln []seg) {
		lines = append(lines, ln)
		ids = append(ids, id)
	}

	cwd := attachedDir(v.Sessions)
	ready := line(seg{"◍", pal.Live}, seg{" claude", pal.Primary})
	tail := "ready"
	if w.Session.State == state.SessionEnded {
		tail = "session ended"
	}
	if cwd != "" {
		tail += " · " + cwd
	}
	ready = append(ready, seg{" " + tail, pal.Settled})
	add("", truncSegs(ready, width))
	// §1.5: "no subagents" is a claim about the world, and it is false whenever
	// the machine is holding agents it suppressed. The idle screen is reachable
	// with a full Quiet set (the session goes idle, or nothing has earned a row
	// yet), so it states the count there instead — selectable and expandable,
	// like the tree's line.
	if n := len(w.Quiet); n > 0 {
		head := truncSegs(line(seg{"╰─ ", pal.Struct}, seg{quietCountText(n), pal.Settled}), width)
		if v.SelID == selQuiet {
			add(selQuiet, reverseLine(head))
			for _, ln := range quietDetail(w, width, pal) {
				add(selQuiet, ln)
			}
		} else {
			add(selQuiet, head)
		}
	} else {
		add("", line(seg{"╰─ ", pal.Struct}, seg{copyNoSubagents, pal.Settled}))
	}

	// The session list exists only when the attachment is changeable and
	// ambiguous (sessionPicker). On a pinned pane the whole block —
	// separator, header and rows — is omitted, not blanked.
	if v.sessionPicker() {
		add("", nil)
		// The switch hint is scoped to the sessions actually listed
		// ("1–3 switch" with three rows — mock), capped at the 9 slots §5.1
		// binds.
		slots := len(v.Sessions)
		if slots > 9 {
			slots = 9
		}
		add("", composeLR(
			line(seg{"SESSIONS", pal.Primary}, seg{fmt.Sprintf("  1–%d switch", slots), pal.Deep}),
			line(seg{"attach: newest live", pal.Deep}), width))
		for _, s := range v.Sessions {
			mark := " "
			idStyle := pal.Settled
			if s.Attached {
				mark = "▸"
				idStyle = pal.Primary
			}
			st := sessionStateText(s)
			if s.Attached {
				st += " (attached)"
			}
			add("", composeLR(
				line(seg{mark + " " + s.ShortID, idStyle}, seg{"  " + s.Dir, pal.Settled}),
				line(seg{st, pal.Deep}), width))
		}
	}

	add("", nil)
	// The run this pane WATCHED, and only that one. History browsing is gone:
	// the earlier runs are on disk and `agentpane runs` prints them, and a pane
	// that pages through other sessions' runs is answering a question nobody
	// asked it while a live session is one keystroke away.
	if w.LastRun == nil {
		add("", truncSegs(line(seg{copyFirstRun, pal.Settled}), width))
		return lines, ids
	}
	run := lastRunRow(w.LastRun, v)
	// The run named by what it was FOR, not by its record id. "RUN f3c7-r2" is
	// the filename; nobody recognises a run by it, and the thing anyone wants
	// from an idle pane is "what was the last thing I asked for, and how did it
	// go". The id keeps a place on the row — dim, right — because it is the key
	// `agentpane runs show` takes and must never be the part that is elided.
	head := line(seg{"LAST RUN", pal.Primary})
	idSeg := line(seg{run.ID, pal.Deep})
	if run.Label != "" {
		budget := width - segsWidth(idSeg) - segsWidth(head) - 3
		if label := budgetLabel(run.Label, budget); label != "" {
			head = append(head, seg{"  " + label, pal.Settled})
		}
	}
	add("", composeLR(head, idSeg, width))

	summary := line(seg{runSummary(run), pal.Settled})
	var when []seg
	if !run.End.IsZero() {
		when = line(seg{"ended " + shortAgo(now.Sub(run.End)) + " ago", pal.Deep})
	}
	add("", composeLR(summary, when, width))

	maxDur := time.Duration(0)
	for _, a := range run.Agents {
		if a.Duration > maxDur {
			maxDur = a.Duration
		}
	}
	for _, a := range run.Agents {
		cells := 1
		if maxDur > 0 {
			cells = int(a.Duration * idleBarCells / maxDur)
			if cells < 1 {
				cells = 1
			}
		}
		// Name left, bar + duration right-aligned to the pane edge (mock;
		// §3.10.5 makes the mock authoritative for layout).
		add("", composeLR(
			line(seg{a.Name, pal.Settled}),
			line(
				seg{strings.Repeat("█", cells), pal.Live},
				seg{" " + formatSince(a.Duration), pal.Deep},
			), width))
	}
	return lines, ids
}

// lastRunRow converts the machine's LastRun into an idle-screen row, slugging
// agent names the same way the tree does.
func lastRunRow(r *state.Run, v *UIState) RunRow {
	row := RunRow{
		ID: r.ID, Label: promptLabel(r.Prompt), Start: r.StartedAt, End: r.EndedAt,
		Tokens: r.Tokens, Stalls: r.Stalls, Contentions: r.Contentions,
		Outcome: runOutcome(*r),
	}
	for _, a := range r.Agents {
		row.Agents = append(row.Agents, RunAgentRow{
			Name:     v.slugByID(a.ID, a.Name),
			Duration: a.Duration,
			Status:   a.Status.String(),
			Tokens:   a.Tokens,
		})
	}
	return row
}

func attachedDir(sessions []SessionRow) string {
	for _, s := range sessions {
		if s.Attached {
			return s.Dir
		}
	}
	return ""
}

func sessionStateText(s SessionRow) string {
	switch {
	case s.Live && s.Idle:
		return "live · idle"
	case s.Live && s.Agents > 0:
		return fmt.Sprintf("live · %d agents", s.Agents)
	case s.Live:
		return "live"
	case s.EndedAgo > 0:
		return "ended " + shortAgo(s.EndedAgo)
	default:
		return "ended"
	}
}

func shortAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

// runSummary is the §3.8 line: "8 subagents · 4m12s · 611k tok · 1 stalled".
func runSummary(run RunRow) string {
	parts := []string{fmt.Sprintf("%d subagents", len(run.Agents))}
	if d := run.End.Sub(run.Start); d > 0 {
		parts = append(parts, formatSince(d))
	}
	if t := formatTokens(run.Tokens); t != "" {
		parts = append(parts, t+" tok")
	}
	if run.Stalls > 0 {
		parts = append(parts, fmt.Sprintf("%d stalled", run.Stalls))
	} else if run.Outcome != "" {
		parts = append(parts, run.Outcome)
	}
	return strings.Join(parts, " · ")
}
