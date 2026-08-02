package term

import (
	"bytes"
	"testing"
)

func TestEmitterGolden(t *testing.T) {
	tests := []struct {
		name string
		emit func(e Emitter)
		want string
	}{
		{
			name: "SetTitle",
			emit: func(e Emitter) { e.SetTitle("⚑ fix-ts2345 needs you") },
			want: "\x1b]1;⚑ fix-ts2345 needs you\a",
		},
		{
			name: "SetTitle strips control bytes",
			emit: func(e Emitter) { e.SetTitle("evil\x1b]9;pwn\atitle") },
			want: "\x1b]1;evil]9;pwntitle\a",
		},
		{
			name: "ClearTitle",
			emit: func(e Emitter) { e.ClearTitle() },
			want: "\x1b]1;\a",
		},
		{
			name: "Badge",
			// base64("⚑ needs you") == "4pqRIG5lZWRzIHlvdQ=="
			emit: func(e Emitter) { e.Badge("⚑ needs you") },
			want: "\x1b]1337;SetBadgeFormat=4pqRIG5lZWRzIHlvdQ==\a",
		},
		{
			name: "ClearBadge",
			emit: func(e Emitter) { e.ClearBadge() },
			want: "\x1b]1337;SetBadgeFormat=\a",
		},
		{
			name: "RequestAttention",
			emit: func(e Emitter) { e.RequestAttention() },
			want: "\x1b]1337;RequestAttention=1\a",
		},
		{
			name: "Notify",
			emit: func(e Emitter) { e.Notify("agentpane: fix-ts2345 needs you") },
			want: "\x1b]9;agentpane: fix-ts2345 needs you\a",
		},
		{
			name: "Bell",
			emit: func(e Emitter) { e.Bell() },
			want: "\a",
		},
		{
			name: "EnableFocusReporting",
			emit: func(e Emitter) { e.EnableFocusReporting() },
			want: "\x1b[?1004h",
		},
		{
			name: "DisableFocusReporting",
			emit: func(e Emitter) { e.DisableFocusReporting() },
			want: "\x1b[?1004l",
		},
		{
			name: "CopyToClipboard OSC 52",
			// base64("hi") == "aGk="
			emit: func(e Emitter) { e.CopyToClipboard("hi") },
			want: "\x1b]52;c;aGk=\a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			e := Emitter{W: &buf, Enabled: true, CopyFallback: func(string) error { return nil }}
			tt.emit(e)
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHyperlink(t *testing.T) {
	e := Emitter{Enabled: true}
	got := e.Hyperlink("file:///a/b.go", "b.go")
	want := "\x1b]8;;file:///a/b.go\x1b\\b.go\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := e.Hyperlink("", "plain"); got != "plain" {
		t.Errorf("empty url: got %q, want plain text", got)
	}
	d := Emitter{Enabled: false}
	if got := d.Hyperlink("file:///a", "plain"); got != "plain" {
		t.Errorf("disabled: got %q, want plain text", got)
	}
}

func TestCopyToClipboardFallback(t *testing.T) {
	var buf bytes.Buffer
	var fallback []string
	e := Emitter{W: &buf, Enabled: true, CopyFallback: func(s string) error {
		fallback = append(fallback, s)
		return nil
	}}
	e.CopyToClipboard("yanked text")
	if len(fallback) != 1 || fallback[0] != "yanked text" {
		t.Errorf("fallback got %v, want one call with the yanked text", fallback)
	}
}

func TestDisabledEmitterIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	called := false
	e := Emitter{W: &buf, Enabled: false, CopyFallback: func(string) error {
		called = true
		return nil
	}}
	e.SetTitle("t")
	e.ClearTitle()
	e.Badge("b")
	e.ClearBadge()
	e.RequestAttention()
	e.Notify("n")
	e.Bell()
	e.CopyToClipboard("c")
	e.EnableFocusReporting()
	e.DisableFocusReporting()
	if buf.Len() != 0 {
		t.Errorf("disabled emitter wrote %q", buf.String())
	}
	if called {
		t.Error("disabled emitter ran the clipboard fallback")
	}
	// The zero value must be safe too (§5.4: never break a render).
	var zero Emitter
	zero.SetTitle("t")
	zero.Bell()
}
