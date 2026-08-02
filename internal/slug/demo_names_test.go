package slug

import "testing"

// The §2.10 demo cast names are normative (PART 10: --demo reproduces §2.10
// exactly). Every description the fake source uses must slug to its §2.10 row
// name.
func TestDemoCastNames(t *testing.T) {
	cases := []struct{ desc, want string }{
		{"Fix the TS2345 fallout in api handlers", "fix-ts2345-fallout"},
		{"Run the auth test suite in watch mode", "run-auth-tests"},
		{"Audit the users schema for missing indexes", "audit-users-schema"},
		{"Document the new email constraint", "document-constr…"},
		{"Lint and format the changed files", "lint-and-format"},
	}
	tbl := New(18)
	for i, c := range cases {
		if got := tbl.Assign(string(rune('a'+i)), c.desc); got != c.want {
			t.Errorf("Assign(%q) = %q, want %q", c.desc, got, c.want)
		}
	}
}
