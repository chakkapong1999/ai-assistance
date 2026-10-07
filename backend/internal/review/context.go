package review

import (
	"fmt"
	"strings"
)

const (
	contextPad      = 3    // unchanged/removed lines kept around a finding
	contextMaxLines = 60   // longest excerpt
	contextMaxBytes = 4000 // largest excerpt
)

// CodeContext returns the part of the diff a finding is about, as one
// unified-diff hunk ("@@ -a,b +c,d @@" followed by ' ', '+' and '-' lines).
// The dashboard shows it so a finding can be read next to the real code. It
// is "" when the finding's lines are not in the chunk.
func CodeContext(chunk Chunk, path string, start, end int) string {
	for _, p := range chunk.Parts {
		if p.Path != path {
			continue
		}
		for _, h := range p.Hunks {
			if h.NewLines == 0 || start > h.NewStart+h.NewLines-1 || end < h.NewStart {
				continue
			}
			if ex := excerpt(h, start, end); ex != "" {
				return ex
			}
		}
	}
	return ""
}

func excerpt(h Hunk, start, end int) string {
	first, last := -1, -1
	for i, l := range h.Lines {
		if l.Kind != '-' && l.NewNo >= start && l.NewNo <= end {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return ""
	}
	if last-first+1 > contextMaxLines {
		last = first + contextMaxLines - 1
	}
	lo, hi := max(first-contextPad, 0), min(last+contextPad, len(h.Lines)-1)

	// old-side line number of every line, to write a correct header
	oldNo := make([]int, len(h.Lines))
	n := h.OldStart
	for i, l := range h.Lines {
		oldNo[i] = n
		if l.Kind != '+' {
			n++
		}
	}

	var b strings.Builder
	oldFirst, newFirst, oldN, newN := 0, 0, 0, 0
	for i := lo; i <= hi; i++ {
		l := h.Lines[i]
		row := string(l.Kind) + l.Text + "\n"
		if i > first && b.Len()+len(row) > contextMaxBytes {
			break
		}
		if l.Kind != '+' {
			if oldN == 0 {
				oldFirst = oldNo[i]
			}
			oldN++
		}
		if l.Kind != '-' {
			if newN == 0 {
				newFirst = l.NewNo
			}
			newN++
		}
		b.WriteString(row)
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@\n%s", oldFirst, oldN, newFirst, newN, strings.TrimSuffix(b.String(), "\n"))
}
