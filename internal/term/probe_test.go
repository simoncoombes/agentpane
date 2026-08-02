package term

import (
	"strings"
	"testing"
)

func TestParseReplies(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantFG  *RGB
		wantPal map[int]RGB
	}{
		{
			name:    "OSC 4 ST-terminated",
			input:   "\x1b]4;2;rgb:0000/aaaa/0000\x1b\\",
			wantPal: map[int]RGB{2: {0x0000, 0xaaaa, 0x0000}},
		},
		{
			name:    "OSC 4 BEL-terminated",
			input:   "\x1b]4;6;rgb:0000/aaaa/aaaa\a",
			wantPal: map[int]RGB{6: {0x0000, 0xaaaa, 0xaaaa}},
		},
		{
			name:   "OSC 10 default fg",
			input:  "\x1b]10;rgb:55ff/ffff/55ff\x1b\\",
			wantFG: &RGB{0x55ff, 0xffff, 0x55ff},
		},
		{
			name:   "two-digit channels scale to 16 bits",
			input:  "\x1b]10;rgb:c0/c0/c0\a",
			wantFG: &RGB{0xc0c0, 0xc0c0, 0xc0c0},
		},
		{
			name:   "one-digit channels scale to 16 bits",
			input:  "\x1b]10;rgb:f/0/f\a",
			wantFG: &RGB{0xffff, 0x0000, 0xffff},
		},
		{
			name:    "rgba alpha ignored",
			input:   "\x1b]4;5;rgba:aaaa/0000/aaaa/ffff\a",
			wantPal: map[int]RGB{5: {0xaaaa, 0x0000, 0xaaaa}},
		},
		{
			name: "full probe reply stream with DA1 tail",
			input: "\x1b]4;1;rgb:aaaa/0000/0000\x1b\\" +
				"\x1b]4;2;rgb:0000/aaaa/0000\x1b\\" +
				"\x1b]10;rgb:ffff/ffff/ffff\x1b\\" +
				"\x1b[?62;22c",
			wantFG: &RGB{0xffff, 0xffff, 0xffff},
			wantPal: map[int]RGB{
				1: {0xaaaa, 0x0000, 0x0000},
				2: {0x0000, 0xaaaa, 0x0000},
			},
		},
		{name: "empty buffer", input: ""},
		{name: "garbage", input: "not an escape at all"},
		{name: "truncated OSC", input: "\x1b]4;2;rgb:0000/aa"},
		{name: "malformed color spec", input: "\x1b]4;2;bogus\a"},
		{name: "malformed index", input: "\x1b]4;x;rgb:0/0/0\a"},
		{name: "channel too wide", input: "\x1b]10;rgb:00000/0/0\a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fg, hasFG, pal := parseReplies([]byte(tt.input))
			if tt.wantFG != nil {
				if !hasFG || fg != *tt.wantFG {
					t.Errorf("fg = %v (has=%v), want %v", fg, hasFG, *tt.wantFG)
				}
			} else if hasFG {
				t.Errorf("unexpected fg %v", fg)
			}
			if len(pal) != len(tt.wantPal) {
				t.Fatalf("palette = %v, want %v", pal, tt.wantPal)
			}
			for idx, want := range tt.wantPal {
				if got, ok := pal[idx]; !ok || got != want {
					t.Errorf("palette[%d] = %v (ok=%v), want %v", idx, got, ok, want)
				}
			}
		})
	}
}

func TestHasDA1Reply(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"\x1b[?62;22c", true},
		{"\x1b[?1;2c", true},
		{"\x1b]4;2;rgb:0/0/0\a\x1b[?62c", true},
		{"\x1b[?62;22", false}, // unterminated
		{"\x1b[62c", false},    // not a DA1 reply (no ?)
		{"", false},
		{"plain text with c", false},
	}
	for _, tt := range tests {
		if got := hasDA1Reply([]byte(tt.input)); got != tt.want {
			t.Errorf("hasDA1Reply(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// ANSI values used by the accent truth table.
var (
	ansiGreen   = RGB{0x0000, 0xaaaa, 0x0000}
	ansiCyan    = RGB{0x0000, 0xaaaa, 0xaaaa}
	ansiBlue    = RGB{0x0000, 0x0000, 0xaaaa}
	brightGreen = RGB{0x5555, 0xffff, 0x5555}
	greyFG      = RGB{0xc0c0, 0xc0c0, 0xc0c0}
)

func TestAccentPick(t *testing.T) {
	tests := []struct {
		name       string
		caps       Caps
		wantAccent int
		wantIn     string // substring the doctor reasoning must contain
	}{
		{
			name: "bright-green fg distinguishable from ANSI 2 green → 2",
			caps: Caps{
				SupportsPaletteQuery: true, HasFG: true, FG: brightGreen,
				Palette: map[int]RGB{2: ansiGreen, 4: ansiBlue, 6: ansiCyan},
			},
			wantAccent: 2,
			wantIn:     "user's hue",
		},
		{
			name: "fg identical to ANSI 2 → 6",
			caps: Caps{
				SupportsPaletteQuery: true, HasFG: true, FG: ansiGreen,
				Palette: map[int]RGB{2: ansiGreen, 4: ansiBlue, 6: ansiCyan},
			},
			wantAccent: 6,
			wantIn:     "index 2 (green) indistinguishable",
		},
		{
			name: "grey-on-dark fg → 2 (green reads fine)",
			caps: Caps{
				SupportsPaletteQuery: true, HasFG: true, FG: greyFG,
				Palette: map[int]RGB{2: ansiGreen, 4: ansiBlue, 6: ansiCyan},
			},
			wantAccent: 2,
		},
		{
			name: "2 and 6 collide, 4 distinguishable → 4",
			caps: Caps{
				SupportsPaletteQuery: true, HasFG: true, FG: ansiGreen,
				Palette: map[int]RGB{2: ansiGreen, 6: ansiGreen, 4: ansiBlue},
			},
			wantAccent: 4,
		},
		{
			name: "everything collides → weight-only",
			caps: Caps{
				SupportsPaletteQuery: true, HasFG: true, FG: ansiGreen,
				Palette: map[int]RGB{2: ansiGreen, 4: ansiGreen, 6: ansiGreen},
			},
			wantAccent: AccentWeightOnly,
			wantIn:     "dim",
		},
		{
			name:       "no palette query support → default 6",
			caps:       Caps{},
			wantAccent: AccentDefault,
			wantIn:     "unanswered",
		},
		{
			name: "OSC 4 answered but no fg → default 6",
			caps: Caps{
				SupportsPaletteQuery: true,
				Palette:              map[int]RGB{2: ansiGreen},
			},
			wantAccent: AccentDefault,
			wantIn:     "OSC 10",
		},
		{
			name: "candidate indices unanswered → weight-only, reasoning says so",
			caps: Caps{
				SupportsPaletteQuery: true, HasFG: true, FG: greyFG,
				Palette: map[int]RGB{1: {0xaaaa, 0, 0}},
			},
			wantAccent: AccentWeightOnly,
			wantIn:     "index 2 unanswered",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accent, reasoning := AccentPick(tt.caps)
			if accent != tt.wantAccent {
				t.Errorf("accent = %d, want %d (reasoning: %s)", accent, tt.wantAccent, reasoning)
			}
			if reasoning == "" {
				t.Error("empty reasoning — doctor has nothing to print")
			}
			if tt.wantIn != "" && !strings.Contains(reasoning, tt.wantIn) {
				t.Errorf("reasoning %q does not contain %q", reasoning, tt.wantIn)
			}
		})
	}
}

func TestDistance(t *testing.T) {
	if d := Distance(ansiGreen, ansiGreen); d != 0 {
		t.Errorf("identical colors: d = %v, want 0", d)
	}
	if d := Distance(RGB{0, 0, 0}, RGB{0xffff, 0xffff, 0xffff}); d < 0.99 || d > 1.0 {
		t.Errorf("black vs white: d = %v, want ~1", d)
	}
	// The threshold cases the truth table relies on.
	if d := Distance(brightGreen, ansiGreen); d < DistinguishThreshold {
		t.Errorf("bright green vs ANSI 2: d = %v, want ≥ %v", d, DistinguishThreshold)
	}
	if d := Distance(ansiGreen, ansiCyan); d < DistinguishThreshold {
		t.Errorf("green vs cyan: d = %v, want ≥ %v", d, DistinguishThreshold)
	}
}

func TestProbeNilTTY(t *testing.T) {
	caps := Probe(nil)
	if caps.SupportsPaletteQuery || caps.HasFG || len(caps.Palette) != 0 {
		t.Errorf("nil tty must return zero Caps, got %+v", caps)
	}
}
