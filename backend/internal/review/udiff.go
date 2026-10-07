package review

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a suggestion as a patch the dashboard can show (and a
// person can `git apply`). startLine is the 1-based line in the new file where
// `original` begins; an empty original is an insertion before startLine.
func UnifiedDiff(path string, startLine int, original, suggested string) string {
	o, s := splitLines(original), splitLines(suggested)
	oldStart, newStart := startLine, startLine
	// Per the unified format, a zero-length side is addressed by the line before it.
	if len(o) == 0 {
		oldStart = startLine - 1
	}
	if len(s) == 0 {
		newStart = startLine - 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, len(o), newStart, len(s))
	for _, l := range o {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range s {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}
