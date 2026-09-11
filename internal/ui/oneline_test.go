package ui

import "testing"

// A pasted multi-line prompt must never break main's row into several
// physical lines (the frame below it would shift).
func TestOneLineFlattensMultilinePrompts(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"pasted spec", "# Build: `agentpane`\n\nA live TUI monitor.\nSecond paragraph.", "# Build: `agentpane`"},
		{"leading blank lines", "\n\n  real first line\nsecond", "real first line"},
		{"tabs become spaces", "a\tb", "a b"},
		{"escape stripped", "title\x1b[31m red", "title[31m red"},
		{"single line untouched", "Build agentpane — live TUI monitor", "Build agentpane — live TUI monitor"},
		{"empty", "", ""},
		{"only whitespace", "\n\t \n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := oneLine(c.in)
			if got != c.want {
				t.Errorf("oneLine(%q) = %q, want %q", c.in, got, c.want)
			}
			for _, r := range got {
				if r == '\n' || r == '\t' || r < 0x20 {
					t.Fatalf("control rune %q survived in %q", r, got)
				}
			}
		})
	}
}
