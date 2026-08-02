package ui

import (
	"fmt"
	"strings"

	"github.com/simoncoombes/agentpane/internal/event"
)

// logCap is the inspector's scrollback (PART 4: last 200 lines).
const logCap = 200

type lineKind uint8

const (
	lineNormal lineKind = iota
	lineDim
	lineNeedsYou // ⚑ / ! lines — needs-you colour in the inspector
	lineError    // error text — the ONLY ANSI 1/9 in the pane (§3.10.2a)
	lineOK       // inferred-verify and ✔ lines — ok colour
)

// LogLine is one inspector log entry.
type LogLine struct {
	Text string
	Kind lineKind
}

// EventLog builds the per-agent inspector log from the event stream. Every
// line quotes observed platform data or an explicitly-inferred derivation —
// never a verdict (C8, §3.14). It is owned by the UI goroutine.
type EventLog struct {
	m map[string][]LogLine
}

func NewEventLog() *EventLog {
	return &EventLog{m: make(map[string][]LogLine)}
}

// Lines returns the log for one agent, oldest first.
func (l *EventLog) Lines(agentID string) []LogLine {
	return l.m[agentID]
}

func (l *EventLog) add(agentID string, ln LogLine) {
	if agentID == "" || ln.Text == "" {
		return
	}
	lines := append(l.m[agentID], ln)
	if len(lines) > logCap {
		lines = lines[len(lines)-logCap:]
	}
	l.m[agentID] = lines
}

// isEditToolName mirrors the state machine's edit-tool set.
func isEditToolName(tool string) bool {
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

// addErr appends error text as indented error-coloured lines, one per line of
// the (possibly multi-line) quoted platform text (PART 4) — a raw newline
// inside a log line would break the frame's row geometry.
func (l *EventLog) addErr(agentID, err string) {
	if err == "" {
		return
	}
	for _, ln := range strings.Split(err, "\n") {
		if ln == "" {
			continue
		}
		l.add(agentID, LogLine{Text: "  " + ln, Kind: lineError})
	}
}

// Ingest consumes one event (source or derived) and appends any log lines it
// implies. Unknown kinds are silently skipped (C9).
func (l *EventLog) Ingest(ev event.Event) {
	id := ev.AgentID
	switch ev.Kind {
	case event.ToolEnd:
		if isEditToolName(ev.Tool) && ev.Err == "" && ev.Detail == "" &&
			(ev.ExitCode == nil || *ev.ExitCode == 0) {
			// A clean edit's ToolEnd always has a FileEdited twin carrying
			// the diff stat (both sources emit one) — that twin is the log
			// record, so one edit never logs as two lines (PART 4).
			return
		}
		text := ev.Tool
		if ev.Target != "" {
			text += " " + shortPath(ev.Target)
		}
		switch {
		case ev.Err != "":
			// PART 4: the failure headline is "→ 1 error"; the quoted error
			// text follows as indented lines in the error colour.
			text += " → 1 error"
		case ev.Detail != "":
			text += " → " + ev.Detail
		case ev.ExitCode != nil && *ev.ExitCode != 0:
			text += fmt.Sprintf(" → exit %d", *ev.ExitCode)
		}
		l.add(id, LogLine{Text: text})
		l.addErr(id, ev.Err)
	case event.FileEdited:
		text := "Edit " + shortPath(ev.Target)
		if ev.LinesAdd > 0 || ev.LinesDel > 0 {
			text = fmt.Sprintf("%s (+%d −%d)", text, ev.LinesAdd, ev.LinesDel)
		}
		l.add(id, LogLine{Text: text})
	case event.FileRead:
		l.add(id, LogLine{Text: "Read " + shortPath(ev.Target), Kind: lineDim})
	case event.ToolOutput:
		l.add(id, LogLine{Text: ev.Detail, Kind: lineDim})
	case event.PermissionRequested:
		cmd := ev.Target
		if ev.Ask != nil && ev.Ask.Command != "" {
			cmd = ev.Ask.Command
		}
		l.add(id, LogLine{Text: "⚑ needs permission: " + cmd, Kind: lineNeedsYou})
	case event.PermissionGranted:
		l.add(id, LogLine{Text: "permission granted", Kind: lineDim})
	case event.PermissionDenied:
		l.add(id, LogLine{Text: "permission denied", Kind: lineDim})
	case event.AgentReturned, event.AgentMerged:
		l.add(id, LogLine{Text: "returned to main"})
		if ev.Detail != "" {
			l.add(id, LogLine{Text: "last message: " + ev.Detail, Kind: lineDim})
		}
		l.addErr(id, ev.Err)
	case event.ErrorRaised:
		text := ev.Err
		if text == "" {
			text = ev.Detail
		}
		l.add(id, LogLine{Text: text, Kind: lineError})
	case event.NotificationRaised:
		l.add(id, LogLine{Text: ev.Detail})
	case event.ThinkingEnded:
		l.add(id, LogLine{Text: ev.Detail, Kind: lineDim})
	// Derived kinds (§3.14: everything below is marked inferred).
	case event.RetryInferred:
		l.add(id, LogLine{Text: "↺ " + ev.Detail, Kind: lineDim})
	case event.VerifyObserved:
		l.add(id, LogLine{Text: "✓ verified — " + ev.Target + " (inferred)", Kind: lineOK})
	case event.StallDetected:
		l.add(id, LogLine{Text: "⚑ " + ev.Detail, Kind: lineNeedsYou})
	case event.StallCleared:
		l.add(id, LogLine{Text: "stall cleared", Kind: lineDim})
	case event.CallOpened:
		text := "◐ call open: " + ev.Tool
		if ev.Target != "" {
			text += " " + shortPath(ev.Target)
		}
		l.add(id, LogLine{Text: text, Kind: lineDim})
	case event.AskDeduped:
		l.add(id, LogLine{Text: "⚑ ask repeated " + ev.Detail + " (deduped)", Kind: lineDim})
	case event.GhostAgentFolded:
		l.add(id, LogLine{Text: "retry agent folded into the ask (inferred)", Kind: lineDim})
	}
}
