// Package config loads ~/.config/agentpane/config.toml per SPEC.md rev 3.1
// PART 7. Every key is optional; invalid values produce a Warning and fall
// back to the default - loading never fails and never panics (C9).
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Config mirrors the PART 7 key set.
type Config struct {
	Width            string        `toml:"width"` // wide | narrow
	StuckAfter       time.Duration `toml:"stuck_after"`
	LongRunningAfter time.Duration `toml:"long_running_after"`
	LongRunningAllow []string      `toml:"long_running_allow"`
	WatchNeverExempt bool          `toml:"watch_never_exempt"`
	DecayAfter       time.Duration `toml:"decay_after"`
	Bell             bool          `toml:"bell"`
	Badge            bool          `toml:"badge"`
	Title            bool          `toml:"title"`
	TimeMode         string        `toml:"time_mode"` // since | absolute
	Editor           string        `toml:"editor"`    // "" falls back to $EDITOR at use site
	Spark            string        `toml:"spark"`     // tokens | calls
	MaxCallsShown    int           `toml:"max_calls_shown"`
	HistoryRuns      int           `toml:"history_runs"`
	Notify           bool          `toml:"notify"`
	NotifyAfter      time.Duration `toml:"notify_after"`
	Attention        bool          `toml:"attention"`
	Links            bool          `toml:"links"`
	FocusDigest      bool          `toml:"focus_digest"`
	DigestCap        int           `toml:"digest_cap"`
	SlugMax          int           `toml:"slug_max"`
	Accent           string        `toml:"accent"` // auto | 2..6
	Color            bool          `toml:"color"`  // false == NO_COLOR
}

// Warning describes one config problem the footer should surface once.
type Warning struct {
	Key string // config key at fault; empty for file-level problems
	Msg string
}

func (w Warning) String() string {
	if w.Key == "" {
		return w.Msg
	}
	return w.Key + ": " + w.Msg
}

// Default returns the PART 7 defaults.
func Default() Config {
	return Config{
		Width:            "wide",
		StuckAfter:       45 * time.Second,
		LongRunningAfter: 3 * time.Minute,
		LongRunningAllow: []string{"test", "build", "install", "compile", "bundle", "migrate"},
		WatchNeverExempt: true,
		DecayAfter:       30 * time.Second,
		Bell:             true,
		Badge:            true,
		Title:            true,
		TimeMode:         "since",
		Editor:           "",
		Spark:            "tokens",
		MaxCallsShown:    4,
		HistoryRuns:      20,
		Notify:           true,
		NotifyAfter:      60 * time.Second,
		Attention:        true,
		Links:            true,
		FocusDigest:      true,
		DigestCap:        3,
		SlugMax:          18,
		Color:            true,
		Accent:           "auto",
	}
}

// Load reads the file at path. A missing file yields the defaults with no
// warnings. Unparseable TOML yields the defaults plus one file-level
// warning. Per-key problems (wrong type, bad duration, out-of-range value)
// each yield one warning and keep that key's default. Unknown keys are
// ignored.
func Load(path string) (Config, []Warning) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, []Warning{{Msg: fmt.Sprintf("cannot read %s: %v - using defaults", path, err)}}
	}

	var raw map[string]toml.Primitive
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return cfg, []Warning{{Msg: fmt.Sprintf("cannot parse %s: %v - using defaults", path, err)}}
	}

	var warns []Warning
	l := loader{md: md, raw: raw, warns: &warns}

	l.enum("width", &cfg.Width, "wide", "narrow")
	l.duration("stuck_after", &cfg.StuckAfter)
	l.duration("long_running_after", &cfg.LongRunningAfter)
	l.strings("long_running_allow", &cfg.LongRunningAllow)
	l.boolean("watch_never_exempt", &cfg.WatchNeverExempt)
	l.duration("decay_after", &cfg.DecayAfter)
	l.boolean("bell", &cfg.Bell)
	l.boolean("badge", &cfg.Badge)
	l.boolean("title", &cfg.Title)
	l.enum("time_mode", &cfg.TimeMode, "since", "absolute")
	l.text("editor", &cfg.Editor)
	l.enum("spark", &cfg.Spark, "tokens", "calls")
	l.count("max_calls_shown", &cfg.MaxCallsShown, 0)
	l.count("history_runs", &cfg.HistoryRuns, 0)
	l.boolean("notify", &cfg.Notify)
	l.duration("notify_after", &cfg.NotifyAfter)
	l.boolean("attention", &cfg.Attention)
	l.boolean("links", &cfg.Links)
	l.boolean("focus_digest", &cfg.FocusDigest)
	l.count("digest_cap", &cfg.DigestCap, 0)
	// A slug budget below 4 bytes cannot hold one rune plus the ellipsis.
	l.count("slug_max", &cfg.SlugMax, 4)
	l.accent("accent", &cfg.Accent)
	l.boolean("color", &cfg.Color)

	return cfg, warns
}

type loader struct {
	md    toml.MetaData
	raw   map[string]toml.Primitive
	warns *[]Warning
}

func (l loader) warn(key, format string, args ...any) {
	*l.warns = append(*l.warns, Warning{Key: key, Msg: fmt.Sprintf(format, args...)})
}

func (l loader) prim(key string) (toml.Primitive, bool) {
	p, ok := l.raw[key]
	return p, ok
}

func (l loader) text(key string, dst *string) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var s string
	if err := l.md.PrimitiveDecode(p, &s); err != nil {
		l.warn(key, "expected a string - using default %q", *dst)
		return
	}
	*dst = s
}

func (l loader) enum(key string, dst *string, allowed ...string) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var s string
	if err := l.md.PrimitiveDecode(p, &s); err != nil {
		l.warn(key, "expected a string - using default %q", *dst)
		return
	}
	for _, a := range allowed {
		if s == a {
			*dst = s
			return
		}
	}
	l.warn(key, "invalid value %q - using default %q", s, *dst)
}

func (l loader) boolean(key string, dst *bool) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var b bool
	if err := l.md.PrimitiveDecode(p, &b); err != nil {
		l.warn(key, "expected true or false - using default %v", *dst)
		return
	}
	*dst = b
}

func (l loader) duration(key string, dst *time.Duration) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var s string
	if err := l.md.PrimitiveDecode(p, &s); err != nil {
		l.warn(key, "expected a duration string like %q - using default %s", "45s", *dst)
		return
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		l.warn(key, "invalid duration %q - using default %s", s, *dst)
		return
	}
	if d <= 0 {
		l.warn(key, "duration must be positive, got %q - using default %s", s, *dst)
		return
	}
	*dst = d
}

func (l loader) count(key string, dst *int, min int) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var n int64
	if err := l.md.PrimitiveDecode(p, &n); err != nil {
		l.warn(key, "expected an integer - using default %d", *dst)
		return
	}
	if n < int64(min) {
		l.warn(key, "must be at least %d, got %d - using default %d", min, n, *dst)
		return
	}
	*dst = int(n)
}

func (l loader) strings(key string, dst *[]string) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var xs []string
	if err := l.md.PrimitiveDecode(p, &xs); err != nil {
		l.warn(key, "expected an array of strings - using default %v", *dst)
		return
	}
	*dst = xs
}

// accent accepts "auto" or an ANSI index 2-6, written as either a string
// or a bare integer.
func (l loader) accent(key string, dst *string) {
	p, ok := l.prim(key)
	if !ok {
		return
	}
	var s string
	if err := l.md.PrimitiveDecode(p, &s); err != nil {
		var n int64
		if err := l.md.PrimitiveDecode(p, &n); err != nil {
			l.warn(key, "expected auto or 2-6 - using default %q", *dst)
			return
		}
		s = fmt.Sprintf("%d", n)
	}
	switch s {
	case "auto", "2", "3", "4", "5", "6":
		*dst = s
	default:
		l.warn(key, "invalid value %q (want auto or 2-6) - using default %q", s, *dst)
	}
}
