package ui

import (
	"fmt"

	"github.com/simoncoombes/agentpane/internal/state"
)

// renderOverlay is the §5.1 `?` help overlay: the keymap, the dropped-event
// count, the source name, and the accent auto-pick reasoning. Any key closes.
func renderOverlay(w state.World, v *UIState, cols, rows int, pal Palette) [][]seg {
	width := cols
	if width > 64 {
		width = 64
	}
	var lines [][]seg
	add := func(l []seg) { lines = append(lines, truncSegs(l, width)) }
	key := func(k, what string) []seg {
		return line(seg{k, pal.Primary}, seg{"  " + what, pal.Settled})
	}

	add(line(seg{"KEYS", pal.Primary}))
	add(key("j/k ↑/↓", "select · g/G first/last · click a row to open it"))
	add(key("␣", "pin this agent's history onto the tree (⏎ inspects instead)"))
	add(key("⏎", "inspect the selected agent"))
	add(key("⇥", "jump to the session pane — where a request is answered"))
	add(key("esc/q", "close view — agent keeps running · dismiss digest"))
	add(key("f", "follow the most recent event"))
	add(key("o", "open the current file in $EDITOR"))
	add(key("y/Y", "yank error/command · yank the inspector log"))
	add(key("⇞/⇟", "page the inspector when it's open"))
	add(key("a", "returned agents: list at the foot ↔ rows on the tree"))
	add(key("t", "right column: since ↔ token rate"))
	add(key("w", "cycle fill / wide 64 / narrow 44"))
	// 1–9 is listed only when it does something: the picker is withheld on a
	// pinned pane and when there is a single candidate (sessionPicker).
	if v.sessionPicker() {
		add(key("1–9", "switch session (idle)"))
	}
	add(key("x", copyReadOnly))
	add(key("ctrl-c", "quit agentpane — agents keep running"))
	if v.DemoAvailable {
		add(key("p/.", "demo: pause/resume · step one event"))
	}
	add(nil)
	src := v.SourceName
	if src == "" {
		src = "unknown"
	}
	add(line(seg{"SOURCE ", pal.Deep}, seg{src, pal.Primary}))
	if v.PinnedSession != "" {
		add(line(seg{"SESSION ", pal.Deep},
			seg{shortSessionID(v.PinnedSession) + " · pinned to this pane", pal.Primary}))
	}
	add(line(seg{fmt.Sprintf("%d dropped events — see the debug log", w.DroppedEvents), pal.Settled}))
	if v.AccentReason != "" {
		add(line(seg{"ACCENT ", pal.Deep}, seg{v.AccentReason, pal.Settled}))
	}
	if len(v.Warnings) > 0 {
		add(line(seg{fmt.Sprintf("%d config warnings:", len(v.Warnings)), pal.Settled}))
		for _, warn := range v.Warnings {
			add(line(seg{"  " + warn.String(), pal.Deep}))
		}
	}
	if w.Session.State == state.SessionEnded {
		add(line(seg{"session ended", pal.Settled}))
	}
	add(nil)
	ver := v.Version
	if ver == "" {
		ver = "dev"
	}
	add(line(seg{"agentpane " + ver, pal.Deep}, seg{" · any key closes", pal.Deep}))
	return lines
}
