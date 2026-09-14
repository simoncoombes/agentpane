package installer

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRenderMatchesPython is the parity test that matters: the Windows
// installer and the bash one edit the same file, often the same file on the
// same machine via a synced dotfiles repo, and a formatting disagreement
// between them would show up as a whole-file diff every time the other one
// ran. json.dumps(indent=...) is the reference because install.sh is the
// implementation that already exists.
func TestRenderMatchesPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; this is a parity check against install.sh's serializer")
	}
	docs := []string{
		`{"b":1,"a":2}`,
		`{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"x","timeout":5}]}]}}`,
		`{"empty":{},"none":[],"nested":{"deep":{"deeper":[1,2,{"k":"v"}]}}}`,
		`{"t":true,"f":false,"n":null,"s":"a \"quoted\" string","esc":"tab\there"}`,
		`{"html":"a<b>c&d","unicode":"café ✓"}`,
		`{"big":12345678901234567890,"float":1.5,"neg":-0.25}`,
		`[]`,
		`{}`,
		`{"only":"one"}`,
	}
	for _, unit := range []string{"  ", "    ", "\t"} {
		for _, doc := range docs {
			v, err := parseJSON([]byte(doc))
			if err != nil {
				t.Fatalf("parse %s: %v", doc, err)
			}
			got := v.render(unit)

			py := `import json,sys
print(json.dumps(json.loads(sys.stdin.read()), indent=INDENT, ensure_ascii=False))`
			py = strings.Replace(py, "INDENT", pyIndent(unit), 1)
			cmd := exec.Command("python3", "-c", py)
			cmd.Stdin = strings.NewReader(doc)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("python3 on %s: %v", doc, err)
			}
			if got != string(out) {
				t.Errorf("indent %q, doc %s:\n go: %q\n py: %q", unit, doc, got, string(out))
			}
		}
	}
}

// TestRenderKeepsNumberText pins the one place the two serializers disagree,
// deliberately. Python parses a number and prints it back, so 1e3 in the
// user's file becomes 1000.0 in the rewrite; keeping the source text is
// strictly closer to "only the diff the change makes", which is the whole
// point, so the Go path does not reproduce that.
func TestRenderKeepsNumberText(t *testing.T) {
	v, err := parseJSON([]byte(`{"exp":1e3,"big":12345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	got := v.render("  ")
	for _, want := range []string{"1e3", "12345678901234567890"} {
		if !strings.Contains(got, want) {
			t.Errorf("number was reformatted, losing %q:\n%s", want, got)
		}
	}
}

func pyIndent(unit string) string {
	if unit == "\t" {
		return `"\t"`
	}
	return `"` + unit + `"`
}

// Key order is the whole point: a rewrite must not reorder a user's file.
func TestRenderPreservesKeyOrder(t *testing.T) {
	const doc = `{"zebra":1,"apple":2,"middle":{"z":1,"a":2}}`
	v, err := parseJSON([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	got := v.render("  ")
	zi, ai := strings.Index(got, "zebra"), strings.Index(got, "apple")
	if zi > ai {
		t.Errorf("top-level keys were sorted:\n%s", got)
	}
	if strings.Index(got, `"z"`) > strings.Index(got, `"a"`) {
		t.Errorf("nested keys were sorted:\n%s", got)
	}
}

// A new key lands at the end, so an appended hook does not appear in the
// middle of somebody's file.
func TestSetAppends(t *testing.T) {
	v, err := parseJSON([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	v.set("c", newNumber(3))
	if got := v.render("  "); !strings.HasSuffix(strings.TrimSpace(got), "\"c\": 3\n}") {
		t.Errorf("new key did not land last:\n%s", got)
	}
	// Replacing an existing key keeps its position.
	v.set("a", newNumber(9))
	if got := v.render("  "); strings.Index(got, `"a"`) > strings.Index(got, `"b"`) {
		t.Errorf("replacing a key moved it:\n%s", got)
	}
}

// json.loads keeps only the last value for a duplicated key; a rewrite would
// then delete data the file visibly contains. Both installers refuse.
func TestParseRejectsDuplicateKeys(t *testing.T) {
	if _, err := parseJSON([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Error("duplicate key accepted")
	}
	if _, err := parseJSON([]byte(`{"o":{"a":1,"a":2}}`)); err == nil {
		t.Error("nested duplicate key accepted")
	}
}

func TestParseRejectsTrailingContent(t *testing.T) {
	for _, doc := range []string{`{} {}`, `{"a":1} garbage`, `[1,2] 3`} {
		if _, err := parseJSON([]byte(doc)); err == nil {
			t.Errorf("accepted malformed document %q", doc)
		}
	}
}

func TestDetectIndent(t *testing.T) {
	cases := []struct{ text, want string }{
		{"{\n  \"a\": 1\n}", "  "},
		{"{\n    \"a\": 1\n}", "    "},
		{"{\n\t\"a\": 1\n}", "\t"},
		{`{"a":1}`, "  "},
		{"", "  "},
		{"{\n}", "  "},
		{"[\n      1\n]", "      "},
		// Trailing whitespace after the brace must not be mistaken for the
		// indent unit of the next line.
		{"{   \n   \"a\": 1\n}", "   "},
	}
	for _, c := range cases {
		if got := detectIndent(c.text); got != c.want {
			t.Errorf("detectIndent(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
