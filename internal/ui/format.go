package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// formatSince renders the §2.6 liveness clock: `3s`, `2m50s`, `1h04m`.
func formatSince(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
	}
}

// formatClock renders m:ss (inspector elapsed, decayed tails, run header).
func formatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// formatTokens renders a token count: 112k, 44k, 611k. Zero renders empty
// (honest absence, C8); sub-1000 renders the raw count.
func formatTokens(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
}

// formatRate renders the §3.18 burn rate behind `t`: `2.1k/s`.
func formatRate(tokensPerMinute int) string {
	return fmt.Sprintf("%.1fk/s", float64(tokensPerMinute)/60/1000)
}

// shortPath applies the §2.7 shortening: strip a leading src/ (the repo root
// is already stripped by the source).
// shortPath is the display form of a file path: its base name.
//
// The pane is 64 columns wide and Claude Code hands it absolute paths. Four
// history lines reading `/Users/alex/Dev/acme-web/rust/src/…` are
// four lines that say the same thing and none of them says WHICH file — the
// only distinguishing part is the end, and the end is what the row's budget
// cut off. The full path is never lost: the inspector footer prints it whole
// and `o` opens it.
//
// A target carrying spaces is a command line, not a path (shortTarget routes
// those), and a glob is left alone — `src/**` means nothing as `**`.
func shortPath(p string) string {
	if p == "" || strings.ContainsAny(p, " \t") {
		return p
	}
	trimmed := strings.TrimSuffix(p, "/")
	i := strings.LastIndexByte(trimmed, '/')
	if i < 0 || i+1 >= len(trimmed) {
		return p
	}
	base := trimmed[i+1:]
	if strings.ContainsAny(base, "*?[") {
		return p
	}
	return base
}

// shortTarget is the display form of a tool's target: a base name for a path,
// and for a command line the command with its `cd <dir> &&` preamble removed
// and its absolute paths reduced to base names.
func shortTarget(target string) string {
	if strings.ContainsAny(target, " \t") {
		return shortCommand(stripCD(target))
	}
	return shortPath(target)
}

// shortCommand reduces the absolute paths inside a command line to base names:
// `cat /Users/you/Dev/project/pyproject.toml` is `cat pyproject.toml`, and the
// forty columns it gives back are what the row needs to show the arguments that
// differ between one call and the next. Relative paths and globs are left alone
// — they are already short, and `src/**` means nothing as `**`.
func shortCommand(s string) string {
	if !strings.Contains(s, "/") {
		return s
	}
	fields := strings.Split(s, " ")
	for i, f := range fields {
		trimmed := strings.Trim(f, "\"'")
		if !strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "~/") {
			continue
		}
		if short := shortPath(trimmed); short != trimmed {
			fields[i] = strings.Replace(f, trimmed, short, 1)
		}
	}
	return strings.Join(fields, " ")
}

// promptLabel is the user-visible part of a run's prompt.
//
// A "user prompt" is not always the user typing. Claude Code's own harness
// submits synthetic turns — a <task-notification> when a background job lands,
// a <system-reminder>, a slash command's expansion — and the UserPromptSubmit
// hook reports them exactly like anything else. The pane then labelled a run
// "LAST RUN  <task-notification>", which is the pane reading out its host's
// plumbing.
//
// So a leading tag block is skipped and the first line of real text wins. When
// there is none, this returns "" and the caller falls back (main's row to the
// session title, the idle screen to the bare id) rather than inventing a label.
func promptLabel(s string) string {
	skipUntil := ""
	for _, raw := range strings.Split(s, "\n") {
		ln := strings.TrimSpace(raw)
		if skipUntil != "" {
			if strings.Contains(ln, skipUntil) {
				skipUntil = ""
			}
			continue
		}
		if ln == "" {
			continue
		}
		if name, ok := openTag(ln); ok {
			if closing := "</" + name + ">"; !strings.Contains(ln, closing) {
				skipUntil = closing
			}
			continue
		}
		return oneLine(ln)
	}
	return ""
}

// openTag reads `<name …>` at the head of a line, returning the tag name. It is
// deliberately strict — lowercase, digits and dashes only — so a prompt that
// happens to start with `<` (a snippet of markup the user pasted) is text, not
// a tag, unless it really looks like one of the harness's blocks.
func openTag(ln string) (string, bool) {
	if !strings.HasPrefix(ln, "<") {
		return "", false
	}
	end := strings.IndexAny(ln, " >")
	if end < 2 {
		return "", false
	}
	name := ln[1:end]
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return "", false
		}
	}
	return name, true
}

// shortModel is a model id at row width: "claude-opus-5" reads as "opus-5",
// "claude-haiku-4-5-20251001" as "haiku-4-5". The vendor prefix is the same on
// every row and the training date is not a thing anyone is comparing at a
// glance; the full id is in the inspector, verbatim, because that is where a
// reader goes to be sure.
func shortModel(id string) string {
	s := strings.TrimPrefix(id, "claude-")
	// A trailing release date (8 digits) is noise on a row.
	if i := strings.LastIndexByte(s, '-'); i > 0 && len(s)-i == 9 {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			s = s[:i]
		}
	}
	return s
}

// callLabel is what a tool call is called on a row: the tool's own description
// of what it was doing when there is one, and the tool plus its budgeted target
// when there is not.
//
// The raw form was the honest one and it was unreadable: four Bash calls whose
// first sixty columns are the same `find`/`grep` boilerplate say nothing about
// which is which, and the platform was already carrying a written summary of
// each — the same text the row's own activity line uses. `y` still yanks the
// exact command (§13.1).
func callLabel(c state.Call) string {
	if c.Says != "" {
		return c.Says
	}
	return strings.TrimSpace(c.Tool + " " + shortTarget(c.Target))
}

// stripCD drops the `cd <dir> &&` preamble a Bash call carries so the row shows
// the command that was actually run. Claude Code prefixes nearly every Bash
// call with a cd into the repo root, which on an absolute path is 40 columns of
// identical text in front of the only part that differs. An optional leading
// tool word is preserved ("Bash cd /x && find ." → "Bash find ."), and nested
// preambles are stripped in turn.
func stripCD(s string) string {
	const sep = " && "
	i := strings.Index(s, sep)
	if i < 0 {
		return s
	}
	head, rest := s[:i], s[i+len(sep):]
	fields := strings.Fields(head)
	switch {
	case len(fields) == 2 && fields[0] == "cd":
		return stripCD(rest)
	case len(fields) == 3 && fields[1] == "cd":
		return stripCD(fields[0] + " " + rest)
	}
	return s
}

// shortActivity is the display form of the machine's activity line, which is
// "<Tool> <target>" (or a tool's own summary, left untouched).
func shortActivity(s string) string {
	s = stripCD(s)
	if i := strings.IndexByte(s, ' '); i > 0 {
		if head, rest := s[:i], s[i+1:]; !strings.ContainsAny(rest, " \t") {
			return head + " " + shortPath(rest)
		}
	}
	return s
}

// shortSessionID is the 4-char session id the header and session list use
// (§3.8a), applied here to an id the ui was handed directly (--session).
func shortSessionID(id string) string {
	if len(id) > 4 {
		return id[:4]
	}
	return id
}

// stationNames are the §2.2 observed stations.
var stationNames = [4]string{"spawn", "first tool", "first edit", "return"}

// pips renders the four §2.2 station pips: ▪ reached · ▫ current · · not
// reached. done means the return station itself completed (all four filled).
func pips(station int, done bool) string {
	if station < 0 {
		station = 0
	}
	if station > 3 {
		station = 3
	}
	reached := station
	if done {
		reached = 4
	}
	var b strings.Builder
	for i := 0; i < 4; i++ {
		switch {
		case i < reached:
			b.WriteString("▪")
		case i == reached:
			b.WriteString("▫")
		default:
			b.WriteString("·")
		}
	}
	return b.String()
}

// stationLabel names the current station; a returned agent reads "returned".
func stationLabel(station int, done bool) string {
	if done {
		return "returned"
	}
	if station < 0 {
		station = 0
	}
	if station > 3 {
		station = 3
	}
	return stationNames[station]
}

// spinnerFrames is the §3.11 braille spinner, stepped on the 1s tick.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}

func spinner(now time.Time) string {
	i := int(now.Unix() % int64(len(spinnerFrames)))
	if i < 0 {
		i += len(spinnerFrames)
	}
	return spinnerFrames[i]
}
