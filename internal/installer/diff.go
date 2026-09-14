package installer

// diff.go — a unified diff, for showing someone what is about to happen to
// their settings file before they say yes.
//
// This is display only: nothing parses it back, and the proposed content is
// applied verbatim rather than by replaying the diff. It exists because
// "[y/N]" on an unexplained change is not a real question. Python's difflib
// does this job on the unix side; the Go standard library has no equivalent,
// so here is one, kept small and tested rather than pulled in as a
// dependency.

import (
	"fmt"
	"strings"
)

const diffContext = 3

// unifiedDiff renders the change from old to new in unified format, labelled
// with the path being changed.
func unifiedDiff(path, old, new string) string {
	a, b := splitLines(old), splitLines(new)
	ops := diffOps(a, b)

	var out strings.Builder
	var hunk []string
	var aStart, bStart, aCount, bCount int
	pending := 0 // trailing context lines already emitted into hunk

	flush := func() {
		if len(hunk) == 0 {
			return
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", rng(aStart, aCount), rng(bStart, bCount))
		for _, l := range hunk {
			out.WriteString(l)
		}
		hunk, aCount, bCount, pending = nil, 0, 0, 0
	}

	ai, bi := 0, 0
	for i := 0; i < len(ops); i++ {
		op := ops[i]
		switch op.kind {
		case opEqual:
			if len(hunk) == 0 {
				// Look ahead: only keep context that precedes a change.
				if !changeWithin(ops, i, diffContext) {
					ai, bi = ai+1, bi+1
					continue
				}
				aStart, bStart = ai+1, bi+1
			}
			if pending >= diffContext && !changeWithin(ops, i, diffContext) {
				flush()
				ai, bi = ai+1, bi+1
				continue
			}
			hunk = append(hunk, " "+a[ai])
			aCount, bCount, pending = aCount+1, bCount+1, pending+1
			ai, bi = ai+1, bi+1
		case opDelete:
			if len(hunk) == 0 {
				aStart, bStart = ai+1, bi+1
			}
			hunk = append(hunk, "-"+a[ai])
			aCount, pending = aCount+1, 0
			ai++
		case opInsert:
			if len(hunk) == 0 {
				aStart, bStart = ai+1, bi+1
			}
			hunk = append(hunk, "+"+b[bi])
			bCount, pending = bCount+1, 0
			bi++
		}
	}
	flush()

	if out.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("--- %s\n+++ %s (proposed)\n", path, path) + out.String()
}

// rng renders a hunk range, using unified diff's "0 lines at position n"
// convention for an empty side.
func rng(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// changeWithin reports whether a non-equal op appears within n positions
// after i — the test for "is this equal line worth printing as context".
func changeWithin(ops []diffOp, i, n int) bool {
	for j := i + 1; j <= i+n && j < len(ops); j++ {
		if ops[j].kind != opEqual {
			return true
		}
	}
	return false
}

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type diffOp struct{ kind opKind }

// diffOps is a longest-common-subsequence diff over lines. Settings files are
// small — hundreds of lines at most — so the quadratic table is the right
// trade for code anyone can check by reading it.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opEqual})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{opDelete})
			i++
		default:
			ops = append(ops, diffOp{opInsert})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{opDelete})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{opInsert})
	}
	return ops
}

// splitLines keeps the line terminators, so a file with no trailing newline
// diffs as one.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
