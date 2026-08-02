package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingFileGivesDefaults(t *testing.T) {
	cfg, warns := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if len(warns) != 0 {
		t.Errorf("warnings for missing file: %v", warns)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("missing file did not give defaults:\n got %+v\nwant %+v", cfg, Default())
	}
}

func TestDefaults(t *testing.T) {
	d := Default()
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"width", d.Width, "wide"},
		{"stuck_after", d.StuckAfter, 45 * time.Second},
		{"long_running_after", d.LongRunningAfter, 3 * time.Minute},
		{"long_running_allow", d.LongRunningAllow, []string{"test", "build", "install", "compile", "bundle", "migrate"}},
		{"watch_never_exempt", d.WatchNeverExempt, true},
		{"decay_after", d.DecayAfter, 30 * time.Second},
		{"bell", d.Bell, true},
		{"badge", d.Badge, true},
		{"title", d.Title, true},
		{"time_mode", d.TimeMode, "since"},
		{"editor", d.Editor, ""},
		{"spark", d.Spark, "tokens"},
		{"max_calls_shown", d.MaxCallsShown, 4},
		{"history_runs", d.HistoryRuns, 20},
		{"notify", d.Notify, true},
		{"notify_after", d.NotifyAfter, 60 * time.Second},
		{"attention", d.Attention, true},
		{"links", d.Links, true},
		{"focus_digest", d.FocusDigest, true},
		{"digest_cap", d.DigestCap, 3},
		{"slug_max", d.SlugMax, 18},
		{"accent", d.Accent, "auto"},
		{"color", d.Color, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("default %s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestValidFile(t *testing.T) {
	path := writeConfig(t, `
width = "narrow"
stuck_after = "1m30s"
long_running_after = "10m"
long_running_allow = ["deploy"]
watch_never_exempt = false
decay_after = "5s"
bell = false
badge = false
title = false
time_mode = "absolute"
editor = "hx"
spark = "calls"
max_calls_shown = 8
history_runs = 5
notify = false
notify_after = "2m"
attention = false
links = false
focus_digest = false
digest_cap = 7
slug_max = 24
accent = "3"
color = false
`)
	cfg, warns := Load(path)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	want := Config{
		Width:            "narrow",
		StuckAfter:       90 * time.Second,
		LongRunningAfter: 10 * time.Minute,
		LongRunningAllow: []string{"deploy"},
		WatchNeverExempt: false,
		DecayAfter:       5 * time.Second,
		Bell:             false,
		Badge:            false,
		Title:            false,
		TimeMode:         "absolute",
		Editor:           "hx",
		Spark:            "calls",
		MaxCallsShown:    8,
		HistoryRuns:      5,
		Notify:           false,
		NotifyAfter:      2 * time.Minute,
		Attention:        false,
		Links:            false,
		FocusDigest:      false,
		DigestCap:        7,
		SlugMax:          24,
		Accent:           "3",
		Color:            false,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load mismatch:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestPartialFileKeepsOtherDefaults(t *testing.T) {
	path := writeConfig(t, `width = "narrow"`)
	cfg, warns := Load(path)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	want := Default()
	want.Width = "narrow"
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("partial file mismatch:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestInvalidValuesFallBack(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		key   string
		check func(Config) bool
	}{
		{"width bad enum", `width = "huge"`, "width", func(c Config) bool { return c.Width == "wide" }},
		{"width wrong type", `width = 3`, "width", func(c Config) bool { return c.Width == "wide" }},
		{"stuck_after not a duration", `stuck_after = "soonish"`, "stuck_after", func(c Config) bool { return c.StuckAfter == 45*time.Second }},
		{"stuck_after wrong type", `stuck_after = 45`, "stuck_after", func(c Config) bool { return c.StuckAfter == 45*time.Second }},
		{"stuck_after negative", `stuck_after = "-5s"`, "stuck_after", func(c Config) bool { return c.StuckAfter == 45*time.Second }},
		{"long_running_after bad", `long_running_after = "3 mins"`, "long_running_after", func(c Config) bool { return c.LongRunningAfter == 3*time.Minute }},
		{"long_running_allow wrong type", `long_running_allow = "test"`, "long_running_allow", func(c Config) bool { return len(c.LongRunningAllow) == 6 }},
		{"watch_never_exempt wrong type", `watch_never_exempt = "yes"`, "watch_never_exempt", func(c Config) bool { return c.WatchNeverExempt }},
		{"decay_after zero", `decay_after = "0s"`, "decay_after", func(c Config) bool { return c.DecayAfter == 30*time.Second }},
		{"bell wrong type", `bell = 1`, "bell", func(c Config) bool { return c.Bell }},
		{"badge wrong type", `badge = "on"`, "badge", func(c Config) bool { return c.Badge }},
		{"title wrong type", `title = 0`, "title", func(c Config) bool { return c.Title }},
		{"time_mode bad enum", `time_mode = "until"`, "time_mode", func(c Config) bool { return c.TimeMode == "since" }},
		{"editor wrong type", `editor = 7`, "editor", func(c Config) bool { return c.Editor == "" }},
		{"spark bad enum", `spark = "sparkles"`, "spark", func(c Config) bool { return c.Spark == "tokens" }},
		{"max_calls_shown negative", `max_calls_shown = -1`, "max_calls_shown", func(c Config) bool { return c.MaxCallsShown == 4 }},
		{"max_calls_shown wrong type", `max_calls_shown = "four"`, "max_calls_shown", func(c Config) bool { return c.MaxCallsShown == 4 }},
		{"history_runs negative", `history_runs = -2`, "history_runs", func(c Config) bool { return c.HistoryRuns == 20 }},
		{"notify wrong type", `notify = "always"`, "notify", func(c Config) bool { return c.Notify }},
		{"notify_after bad", `notify_after = "1 minute"`, "notify_after", func(c Config) bool { return c.NotifyAfter == 60*time.Second }},
		{"attention wrong type", `attention = 2`, "attention", func(c Config) bool { return c.Attention }},
		{"links wrong type", `links = "hyper"`, "links", func(c Config) bool { return c.Links }},
		{"focus_digest wrong type", `focus_digest = "sure"`, "focus_digest", func(c Config) bool { return c.FocusDigest }},
		{"digest_cap negative", `digest_cap = -3`, "digest_cap", func(c Config) bool { return c.DigestCap == 3 }},
		{"slug_max too small", `slug_max = 2`, "slug_max", func(c Config) bool { return c.SlugMax == 18 }},
		{"slug_max wrong type", `slug_max = "big"`, "slug_max", func(c Config) bool { return c.SlugMax == 18 }},
		{"accent out of range", `accent = "9"`, "accent", func(c Config) bool { return c.Accent == "auto" }},
		{"accent bad word", `accent = "green"`, "accent", func(c Config) bool { return c.Accent == "auto" }},
		{"color wrong type", `color = "never"`, "color", func(c Config) bool { return c.Color }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, warns := Load(writeConfig(t, tt.toml))
			if !tt.check(cfg) {
				t.Errorf("field did not fall back to default for %q", tt.toml)
			}
			if len(warns) != 1 {
				t.Fatalf("want exactly 1 warning, got %d: %v", len(warns), warns)
			}
			if warns[0].Key != tt.key {
				t.Errorf("warning key = %q, want %q", warns[0].Key, tt.key)
			}
		})
	}
}

func TestAccentIntegerAccepted(t *testing.T) {
	cfg, warns := Load(writeConfig(t, `accent = 4`))
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if cfg.Accent != "4" {
		t.Errorf("accent = %q, want \"4\"", cfg.Accent)
	}
}

func TestUnknownKeysIgnored(t *testing.T) {
	cfg, warns := Load(writeConfig(t, `
width = "narrow"
totally_unknown = "value"
another = 42
[section]
nested = true
`))
	if len(warns) != 0 {
		t.Errorf("unknown keys produced warnings: %v", warns)
	}
	if cfg.Width != "narrow" {
		t.Errorf("known key lost among unknown keys: width = %q", cfg.Width)
	}
}

func TestMalformedTOMLGivesDefaultsAndWarning(t *testing.T) {
	cfg, warns := Load(writeConfig(t, `width = "narrow`))
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("malformed file did not give defaults")
	}
	if len(warns) != 1 || warns[0].Key != "" {
		t.Errorf("want one file-level warning, got %v", warns)
	}
}

func TestMultipleInvalidValuesWarnEach(t *testing.T) {
	cfg, warns := Load(writeConfig(t, `
width = "huge"
spark = "sparkles"
slug_max = 40
`))
	if len(warns) != 2 {
		t.Fatalf("want 2 warnings, got %d: %v", len(warns), warns)
	}
	if cfg.Width != "wide" || cfg.Spark != "tokens" {
		t.Errorf("invalid values not defaulted: width=%q spark=%q", cfg.Width, cfg.Spark)
	}
	if cfg.SlugMax != 40 {
		t.Errorf("valid slug_max=40 rejected: got %d", cfg.SlugMax)
	}
}

func TestWarningString(t *testing.T) {
	if got := (Warning{Key: "width", Msg: "bad"}).String(); got != "width: bad" {
		t.Errorf("Warning.String() = %q", got)
	}
	if got := (Warning{Msg: "file broken"}).String(); got != "file broken" {
		t.Errorf("file-level Warning.String() = %q", got)
	}
}
