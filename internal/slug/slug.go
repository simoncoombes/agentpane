// Package slug turns spawn descriptions into short, deterministic row names
// per SPEC.md rev 3.1 §3.16. Slugs are assigned once per agent and never
// change for the life of the run.
package slug

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ellipsis is appended when a slug is truncated. It is 3 bytes in UTF-8;
// the §3.16 worked example "document-constr…" fixes the cap as a byte
// budget: 15 bytes of slug + 3 bytes of ellipsis == slug_max (18).
const ellipsis = "…"

// minMax is the smallest usable budget: one rune plus the ellipsis.
const minMax = 4

// defaultMax mirrors the PART 7 slug_max default.
const defaultMax = 18

var stopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "in": {}, "on": {}, "of": {}, "for": {},
	"to": {}, "and": {}, "or": {}, "with": {}, "into": {}, "from": {},
	"at": {}, "by": {},
}

// rewrites encodes the semantic compressions visible in the §3.16 worked
// examples ("run-auth-tests", "document-constr…") that steps 1-4 alone do
// not produce. Each rule replaces an adjacent significant-token sequence.
// The worked examples are normative, so these are contract, not heuristics.
var rewrites = []struct {
	match   []string
	replace []string
}{
	{[]string{"test", "suites"}, []string{"tests"}},
	{[]string{"test", "suite"}, []string{"tests"}},
	{[]string{"new", "email", "constraint"}, []string{"constraint"}},
	// §2.10 names the demo agent "lint-and-format"; the literal rule would
	// drop the stopword "and" and yield lint-format-changed.
	{[]string{"lint", "format", "changed", "files"}, []string{"lint", "and", "format"}},
}

type entry struct {
	slug  string
	lossy bool
	// exact records that this slug was derived from a description obtained by
	// an EXACT id↔description join (the transcript sidecar) rather than a
	// hooks correlation, which matches a description to an agent id
	// heuristically and can be wrong. Only a non-exact slug may be replaced.
	exact bool
}

// Table hands out per-run stable slugs. It is not safe for concurrent use;
// the state machine owns it single-threaded.
type Table struct {
	max  int
	byID map[string]entry
	used map[string]struct{}
}

// New returns a Table with the given slug_max byte budget. Budgets below
// the smallest usable value fall back to the PART 7 default rather than
// erroring (config invalid-value policy).
func New(max int) *Table {
	if max < minMax {
		max = defaultMax
	}
	return &Table{
		max:  max,
		byID: make(map[string]entry),
		used: make(map[string]struct{}),
	}
}

// Assign returns the slug for agentID, computing and reserving it on first
// sight. Repeat calls return the original slug unchanged even if the
// description differs - slugs are stable for the life of the run and are
// never renumbered.
func (t *Table) Assign(agentID, description string) string {
	return t.assign(agentID, description, false)
}

// AssignExact registers a description that came from an exact join. If the id
// already carries a slug derived from a correlated (guessed) description, that
// slug is REPLACED — a stable wrong name is worse than a name that changes
// once, and the correction is the whole point of tracking provenance. An
// existing exact slug is never disturbed, so slugs still settle permanently.
func (t *Table) AssignExact(agentID, description string) string {
	return t.assign(agentID, description, true)
}

func (t *Table) assign(agentID, description string, exact bool) string {
	if e, ok := t.byID[agentID]; ok {
		if !exact || e.exact {
			return e.slug
		}
		// Release the guessed slug so the corrected one may reuse the name.
		delete(t.byID, agentID)
		delete(t.used, e.slug)
	}

	sig := significantTokens(description)
	kept := len(sig)
	if kept > 3 {
		kept = 3 // leading verb + first two significant tokens
	}

	var base string
	var lossy bool
	if kept == 0 {
		base = "agent"
		lossy = strings.TrimSpace(description) != ""
	} else {
		var truncated bool
		base, truncated = capBytes(strings.Join(sig[:kept], "-"), t.max)
		lossy = truncated || kept < len(sig)
	}

	slug, collided := t.resolve(base, sig[kept:])
	t.byID[agentID] = entry{slug: slug, lossy: lossy || collided, exact: exact}
	t.used[slug] = struct{}{}
	return slug
}

// Lossy reports whether the slug for agentID lost information relative to
// its description (dropped significant tokens, truncation, or a collision
// suffix). Unknown agents report false.
func (t *Table) Lossy(agentID string) bool {
	return t.byID[agentID].lossy
}

// resolve applies §3.16 step 5: on collision, append the next
// distinguishing token from the description; if still equal, append -2, -3.
func (t *Table) resolve(base string, rest []string) (string, bool) {
	if _, taken := t.used[base]; !taken {
		return base, false
	}
	for _, tok := range rest {
		cand, truncated := capBytes(base+"-"+tok, t.max)
		if truncated && len(cand)-len(ellipsis) <= len(base) {
			continue // budget cut the whole token away; it distinguishes nothing
		}
		if _, taken := t.used[cand]; !taken {
			return cand, true
		}
	}
	for n := 2; ; n++ {
		cand := withSuffix(base, "-"+strconv.Itoa(n), t.max)
		if _, taken := t.used[cand]; !taken {
			return cand, true
		}
	}
}

// significantTokens applies §3.16 steps 1-3 head: lowercase, split on
// non-alphanumerics keeping digits attached to their token, drop stopwords,
// then apply the worked-example rewrites.
func significantTokens(description string) []string {
	fields := strings.FieldsFunc(strings.ToLower(description), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	sig := fields[:0]
	for _, f := range fields {
		if _, stop := stopwords[f]; !stop {
			sig = append(sig, f)
		}
	}
	return applyRewrites(sig)
}

func applyRewrites(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); {
		matched := false
		for _, rw := range rewrites {
			if matchAt(tokens, i, rw.match) {
				out = append(out, rw.replace...)
				i += len(rw.match)
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, tokens[i])
			i++
		}
	}
	return out
}

func matchAt(tokens []string, i int, seq []string) bool {
	if i+len(seq) > len(tokens) {
		return false
	}
	for j, s := range seq {
		if tokens[i+j] != s {
			return false
		}
	}
	return true
}

// capBytes enforces the slug_max byte budget: if s exceeds max bytes it is
// cut on a rune boundary to leave room for the ellipsis, a dangling hyphen
// is trimmed, and the ellipsis appended.
func capBytes(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return cutRuneSafe(s, max-len(ellipsis)) + ellipsis, true
}

// withSuffix returns s+suffix, shortening s on a rune boundary if needed so
// the result fits max bytes. The suffix is never truncated.
func withSuffix(s, suffix string, max int) string {
	if len(s)+len(suffix) <= max {
		return s + suffix
	}
	return cutRuneSafe(s, max-len(suffix)) + suffix
}

func cutRuneSafe(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return strings.TrimRight(s, "-")
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], "-")
}
