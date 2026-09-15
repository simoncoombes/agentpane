package installer

import (
	"os/exec"
	"strings"
	"testing"
)

// The diff is what someone reads before answering [y/N], so it has to say
// what actually changed. difflib is the reference because install.sh's diff
// is the one people already see.
func TestUnifiedDiffMatchesPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; this is a parity check against install.sh's diff")
	}
	cases := []struct{ name, old, new string }{
		{"append one line", "a\nb\nc\n", "a\nb\nc\nd\n"},
		{"change in the middle", "a\nb\nc\nd\ne\n", "a\nb\nX\nd\ne\n"},
		{"from empty", "", "a\nb\n"},
		{"to empty", "a\nb\n", ""},
		{"no change", "a\nb\n", "a\nb\n"},
		{"two distant hunks", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", "X\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\nY\n"},
		{"delete a run", "a\nb\nc\nd\ne\n", "a\ne\n"},
		{"no trailing newline", "a\nb", "a\nc"},
		{"all new", "a\n", "b\n"},
	}
	for _, c := range cases {
		got := unifiedDiff("s.json", c.old, c.new)

		py := `import difflib,sys
old,new = sys.stdin.read().split("\x00")
sys.stdout.write("".join(difflib.unified_diff(
    old.splitlines(True), new.splitlines(True),
    fromfile="s.json", tofile="s.json (proposed)")))`
		cmd := exec.Command("python3", "-c", py)
		cmd.Stdin = strings.NewReader(c.old + "\x00" + c.new)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: python3: %v", c.name, err)
		}
		// sys.stdout.write translates \n to \r\n on Windows; see normalizeEOL.
		if want := normalizeEOL(string(out)); got != want {
			t.Errorf("%s:\n go:\n%s\n py:\n%s", c.name, got, want)
		}
	}
}

func TestUnifiedDiffEmptyWhenUnchanged(t *testing.T) {
	if d := unifiedDiff("s.json", "same\n", "same\n"); d != "" {
		t.Errorf("an unchanged file produced a diff:\n%s", d)
	}
}
