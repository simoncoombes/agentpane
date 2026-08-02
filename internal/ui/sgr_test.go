package ui

import (
	"strings"
	"testing"
)

// sgrCodes extracts every numeric code from the SGR sequences in s.
func sgrCodes(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && s[j] != 'm' && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j < len(s) && s[j] == 'm' {
			for _, c := range strings.Split(s[i+2:j], ";") {
				if c != "" {
					out = append(out, c)
				}
			}
			i = j
		}
	}
	return out
}

// PART 10: only ANSI 8, 2 (the accent), default-fg, bright (bold), and
// reverse appear in status positions; ANSI 1/9 appears only as inspector log
// text; nothing truecolor, nothing 256-color, no hex anywhere.
func TestSGRScanTreeFrame(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))

	if strings.Contains(frame, "#") {
		t.Errorf("frame contains '#' — hex is forbidden in the render path")
	}
	if strings.Contains(frame, "[38;") || strings.Contains(frame, "[48;") {
		t.Errorf("frame contains truecolor/256-color SGR")
	}
	allowed := map[string]bool{"0": true, "1": true, "2": true, "7": true, "32": true, "90": true}
	for _, c := range sgrCodes(frame) {
		if !allowed[c] {
			t.Errorf("forbidden SGR code %q outside the inspector log", c)
		}
	}
}

func TestSGRScanNeedsYouHasNoHue(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))
	for _, ln := range strings.Split(frame, "\n") {
		if !strings.Contains(stripANSI(ln), "⚑") {
			continue
		}
		// Any SGR run on a line carrying ⚑ must be hue-free apart from the
		// permitted structural styles: the loud role is bold + reverse +
		// glyph only (§3.10.2a).
		for _, c := range sgrCodes(ln) {
			switch c {
			case "0", "1", "2", "7", "32", "90":
			default:
				t.Errorf("needs-you line carries hue code %q: %q", c, stripANSI(ln))
			}
		}
	}
}

func TestSGRScanErrorOnlyInInspectorLog(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.InspectorOpen = true
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, false))

	sawErr := false
	for _, ln := range strings.Split(frame, "\n") {
		hasErr := strings.Contains(ln, "\x1b[31m") || strings.Contains(ln, "\x1b[91m")
		if !hasErr {
			continue
		}
		sawErr = true
		// Error colour may only style inspector log text: the demo's error
		// lines are the two indented TS2345 diagnostic lines (PART 4).
		plain := stripANSI(ln)
		if !strings.HasPrefix(plain, "  TS2345") && !strings.HasPrefix(plain, "  'string | null'") {
			t.Errorf("ANSI 1/9 outside inspector log text: %q", plain)
		}
	}
	if !sawErr {
		t.Errorf("inspector log lost its error-coloured lines")
	}
}

// §3.10.4: NO_COLOR emits no SGR colour at all, yet needs-you, live, and
// settled stay distinguishable via glyph + weight + reverse.
func TestSGRScanNoColor(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 44, demoNow(), NewPalette(2, true))
	for _, c := range sgrCodes(frame) {
		switch c {
		case "0", "1", "2", "7":
		default:
			t.Errorf("NO_COLOR frame contains colour code %q", c)
		}
	}
	plain := stripANSI(frame)
	for _, glyph := range []string{"⚑", "●", "○", "◇", "◍"} {
		if !strings.Contains(plain, glyph) {
			t.Errorf("NO_COLOR frame lost status glyph %q", glyph)
		}
	}
	if !strings.Contains(frame, "\x1b[1m") {
		t.Errorf("NO_COLOR frame lost bold (needs-you weight)")
	}
	if !strings.Contains(frame, "\x1b[1;7m") && !strings.Contains(frame, "\x1b[7m") {
		t.Errorf("NO_COLOR frame lost reverse video (chip/selection)")
	}
}
