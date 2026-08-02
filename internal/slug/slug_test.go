package slug

import "testing"

// The five §3.16 worked examples, verbatim.
func TestSpecExamples(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{"fix", "Fix the TS2345 fallout in api handlers", "fix-ts2345-fallout"},
		{"run", "Run the auth test suite in watch mode", "run-auth-tests"},
		{"audit", "Audit the users schema for missing indexes", "audit-users-schema"},
		{"document", "Document the new email constraint", "document-constr…"},
		{"port-auth", "Port the auth routes", "port-auth-routes"},
		{"port-user", "Port the user routes", "port-user-routes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(18).Assign("a1", tt.description)
			if got != tt.want {
				t.Errorf("Assign(%q) = %q, want %q", tt.description, got, tt.want)
			}
		})
	}
}

func TestSpecExamplePairDistinct(t *testing.T) {
	tab := New(18)
	a := tab.Assign("a1", "Port the auth routes")
	b := tab.Assign("a2", "Port the user routes")
	if a != "port-auth-routes" || b != "port-user-routes" {
		t.Errorf("got %q / %q, want port-auth-routes / port-user-routes", a, b)
	}
}

func TestTruncationBudget(t *testing.T) {
	got := New(18).Assign("a1", "Document the new email constraint")
	if got != "document-constr…" {
		t.Fatalf("got %q, want document-constr…", got)
	}
	if n := len(got); n != 18 {
		t.Errorf("truncated slug is %d bytes, want 18 (slug_max)", n)
	}
}

func TestCollisions(t *testing.T) {
	t.Run("next distinguishing token", func(t *testing.T) {
		tab := New(18)
		a := tab.Assign("a1", "Fix auth flow fast")
		b := tab.Assign("a2", "Fix auth flow slow")
		c := tab.Assign("a3", "Fix auth flow quickly")
		if a != "fix-auth-flow" {
			t.Errorf("first = %q, want fix-auth-flow", a)
		}
		if b != "fix-auth-flow-slow" {
			t.Errorf("second = %q, want fix-auth-flow-slow", b)
		}
		if c != "fix-auth-flow-q…" {
			t.Errorf("third = %q, want fix-auth-flow-q…", c)
		}
	})
	t.Run("token eaten by budget falls to -2", func(t *testing.T) {
		tab := New(18)
		a := tab.Assign("a1", "Fix the auth routes now")
		b := tab.Assign("a2", "Fix the auth routes later")
		if a != "fix-auth-routes" {
			t.Errorf("first = %q, want fix-auth-routes", a)
		}
		if b != "fix-auth-routes-2" {
			t.Errorf("second = %q, want fix-auth-routes-2", b)
		}
	})
	t.Run("identical descriptions fall to -2 -3", func(t *testing.T) {
		tab := New(18)
		a := tab.Assign("a1", "Port the auth routes")
		b := tab.Assign("a2", "Port the auth routes")
		c := tab.Assign("a3", "Port the auth routes")
		if a != "port-auth-routes" || b != "port-auth-routes-2" || c != "port-auth-routes-3" {
			t.Errorf("got %q / %q / %q", a, b, c)
		}
		if len(b) > 18 || len(c) > 18 {
			t.Errorf("suffixed slugs exceed slug_max: %q %q", b, c)
		}
	})
	t.Run("suffix fits budget by shortening base", func(t *testing.T) {
		tab := New(18)
		a := tab.Assign("a1", "Document the new email constraint")
		b := tab.Assign("a2", "Document the new email constraint")
		if a != "document-constr…" {
			t.Errorf("first = %q", a)
		}
		if b != "document-constr-2" {
			t.Errorf("second = %q, want document-constr-2", b)
		}
	})
}

func TestNeverRenumber(t *testing.T) {
	tab := New(18)
	first := tab.Assign("a1", "Port the auth routes")
	tab.Assign("a2", "Port the auth routes")
	tab.Assign("a3", "Port the user routes")
	if got := tab.Assign("a1", "Port the auth routes"); got != first {
		t.Errorf("earlier agent renumbered: %q -> %q", first, got)
	}
	if got := tab.Assign("a2", "Port the auth routes"); got != "port-auth-routes-2" {
		t.Errorf("collision agent unstable: %q", got)
	}
}

func TestIdempotentAcrossDescriptionChange(t *testing.T) {
	tab := New(18)
	first := tab.Assign("a1", "Fix the TS2345 fallout in api handlers")
	if got := tab.Assign("a1", "A completely different description"); got != first {
		t.Errorf("re-Assign with new description changed slug: %q -> %q", first, got)
	}
}

func TestLossy(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        bool
	}{
		{"all tokens kept", "Port the auth routes", false},
		{"tokens dropped", "Fix the TS2345 fallout in api handlers", true},
		{"truncated", "Document the new email constraint", true},
		{"stopwords only dropped", "Run the linter", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab := New(18)
			tab.Assign("a1", tt.description)
			if got := tab.Lossy("a1"); got != tt.want {
				t.Errorf("Lossy(%q) = %v, want %v", tt.description, got, tt.want)
			}
		})
	}
	t.Run("collision suffix is lossy", func(t *testing.T) {
		tab := New(18)
		tab.Assign("a1", "Port the auth routes")
		tab.Assign("a2", "Port the auth routes")
		if tab.Lossy("a1") {
			t.Error("first assignee wrongly lossy")
		}
		if !tab.Lossy("a2") {
			t.Error("suffixed assignee not lossy")
		}
	})
	t.Run("unknown agent", func(t *testing.T) {
		if New(18).Lossy("nope") {
			t.Error("unknown agent reported lossy")
		}
	})
}

func TestMalformedInput(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{"empty", ""},
		{"whitespace", "   \t\n"},
		{"stopwords only", "the a an of"},
		{"punctuation only", "!!! --- ///"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab := New(18)
			a := tab.Assign("a1", tt.description)
			b := tab.Assign("a2", tt.description)
			if a == "" || b == "" {
				t.Fatalf("empty slug for %q: %q / %q", tt.description, a, b)
			}
			if a == b {
				t.Errorf("duplicate slug %q for two agents", a)
			}
		})
	}
}

func TestDigitsStayAttached(t *testing.T) {
	got := New(18).Assign("a1", "Fix TS2345 now")
	if got != "fix-ts2345-now" {
		t.Errorf("got %q, want fix-ts2345-now", got)
	}
}

func TestTinyMaxFallsBackToDefault(t *testing.T) {
	got := New(1).Assign("a1", "Fix the TS2345 fallout in api handlers")
	if got != "fix-ts2345-fallout" {
		t.Errorf("got %q, want default-budget slug", got)
	}
}

func TestCustomMax(t *testing.T) {
	got := New(10).Assign("a1", "Fix the TS2345 fallout in api handlers")
	if got != "fix-ts2…" {
		t.Errorf("got %q, want fix-ts2…", got)
	}
	if len(got) > 10 {
		t.Errorf("slug %q exceeds 10-byte budget", got)
	}
}
