package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/runstore"
)

// cmdRuns implements `agentpane runs [--session <id>]` and
// `agentpane runs show <id>`. Without --session the verb lists the WHOLE
// store — every session's runs plus any flat files predating session scoping
// — which is what a CLI asked for "the runs" should show; --session narrows
// it to one session, the same slice a pane pinned to that session sees.
func cmdRuns(stdout, stderr io.Writer, args []string) int {
	session, args, err := takeSessionFlag(args)
	if err != nil {
		fmt.Fprintf(stderr, "agentpane: %v\n\nusage:\n%s", err, runsUsage)
		return 2
	}
	cfg, _ := config.Load(configPath())
	dir, derr := runstore.DefaultDir()
	if derr != nil {
		fmt.Fprintf(stderr, "agentpane: cannot resolve the run store: %v\n", derr)
		return 1
	}
	store := &runstore.Store{Dir: dir, HistoryRuns: cfg.HistoryRuns, Session: session}

	switch {
	case len(args) == 0:
		return runsList(stdout, store)
	case args[0] == "show" && len(args) == 2:
		return runsShow(stdout, stderr, store, args[1])
	default:
		fmt.Fprintf(stderr, "usage:\n%s", runsUsage)
		return 2
	}
}

const runsUsage = "  agentpane runs [--session <id>]\n  agentpane runs show <id>\n"

// takeSessionFlag pulls an optional --session <id> (or --session=<id>, and
// the single-dash spellings flag accepts) out of the verb's arguments and
// returns the remainder.
func takeSessionFlag(args []string) (string, []string, error) {
	session := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		if name != "--session" && name != "-session" {
			rest = append(rest, a)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--session needs a session id")
			}
			i++
			value = args[i]
		}
		if value == "" {
			return "", nil, fmt.Errorf("--session needs a session id")
		}
		session = value
	}
	return session, rest, nil
}

func runsList(w io.Writer, store *runstore.Store) int {
	runs, skipped, err := store.List()
	if err != nil {
		fmt.Fprintf(w, "no persisted runs (%v)\n", err)
		return 0
	}
	if len(runs) == 0 {
		fmt.Fprintln(w, "no persisted runs")
		return 0
	}
	for _, r := range runs {
		fmt.Fprintln(w, runLine(r))
	}
	if skipped > 0 {
		fmt.Fprintf(w, "(%d unreadable run file(s) skipped)\n", skipped)
	}
	return 0
}

func runsShow(stdout, stderr io.Writer, store *runstore.Store, id string) int {
	r, found, err := store.Load(id)
	if err != nil {
		fmt.Fprintf(stderr, "agentpane: %v\n", err)
		return 1
	}
	if !found {
		fmt.Fprintf(stderr, "agentpane: no run %q — `agentpane runs` lists ids\n", id)
		return 1
	}
	fmt.Fprintln(stdout, runLine(r))
	nameW := 0
	for _, a := range r.Agents {
		if len(a.Name) > nameW {
			nameW = len(a.Name)
		}
	}
	for _, a := range r.Agents {
		fmt.Fprintf(stdout, "  %-*s  %6s  %-8s %s\n",
			nameW, a.Name, fmtRunDur(a.Duration), a.Status, fmtRunTokens(a.Tokens))
	}
	return 0
}

// runLine is one run summary row: id, when, duration, agents, tokens,
// outcome, and — because the unscoped list merges every session — which
// session produced it. Files written before session scoping carry no session
// and get no invented one (C8).
func runLine(r runstore.Run) string {
	tokens := 0
	for _, a := range r.Agents {
		tokens += a.Tokens
	}
	line := fmt.Sprintf("RUN %s  %s  %s  %d agents  %s  %s",
		r.ID,
		r.Start.Local().Format("2006-01-02 15:04"),
		fmtRunDur(r.End.Sub(r.Start)),
		len(r.Agents),
		fmtRunTokens(tokens),
		r.Outcome)
	if r.Session != "" {
		line += "  session " + shortID(r.Session)
	}
	return line
}

// fmtRunDur renders 4m12s / 58s / 1h04m.
func fmtRunDur(d time.Duration) string {
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

// fmtRunTokens renders 611k tok / 1.2M tok; zero is honest-empty (C8).
func fmtRunTokens(n int) string {
	switch {
	case n <= 0:
		return "- tok"
	case n < 1000:
		return fmt.Sprintf("%d tok", n)
	case n < 1000000:
		return fmt.Sprintf("%dk tok", n/1000)
	default:
		return fmt.Sprintf("%.1fM tok", float64(n)/1e6)
	}
}
