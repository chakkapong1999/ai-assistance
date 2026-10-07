package review

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxTitleRunes       = 200
	maxExplanationRunes = 4000
	maxSummaryRunes     = 2000
)

type rawSuggestion struct {
	Original  string `json:"original"`
	Suggested string `json:"suggested"`
}

type rawFinding struct {
	File        string         `json:"file"`
	LineStart   int            `json:"line_start"`
	LineEnd     int            `json:"line_end"`
	Severity    string         `json:"severity"`
	Category    string         `json:"category"`
	Title       string         `json:"title"`
	Explanation string         `json:"explanation"`
	Suggestion  *rawSuggestion `json:"suggestion"`
}

type rawOutput struct {
	Summary  string       `json:"summary"`
	Findings []rawFinding `json:"findings"`
}

// extractJSON tolerates the two ways models wrap JSON despite instructions:
// a ```json fence and chatter before/after the object.
func extractJSON(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil
	}
	return []byte(s[start : end+1])
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func validSeverity(s string) bool {
	switch s {
	case SeverityCritical, SeverityMajor, SeverityMinor, SeverityInfo:
		return true
	}
	return false
}

func normaliseCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case CategoryBug, CategorySecurity, CategoryPerformance, CategoryMaintainability, CategoryStyle, CategoryTest:
		return c
	}
	return CategoryOther
}

// ParseOutput checks the *shape* of a model answer. Whether the findings are
// true of the actual diff is checked later by Sanitize.
//
// A finding with a missing file/title or an unknown severity makes the whole
// answer invalid (ErrInvalidOutput): that is a malformed answer worth a
// retry, not something to quietly paper over.
func ParseOutput(raw []byte) (Result, error) {
	body := extractJSON(raw)
	if body == nil {
		return Result{}, fmt.Errorf("%w: no JSON object found", ErrInvalidOutput)
	}
	var out rawOutput
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}

	res := Result{Summary: truncateRunes(strings.TrimSpace(out.Summary), maxSummaryRunes)}
	for i, f := range out.Findings {
		file := strings.TrimSpace(f.File)
		title := strings.TrimSpace(f.Title)
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		switch {
		case file == "":
			return Result{}, fmt.Errorf("%w: finding %d has no file", ErrInvalidOutput, i)
		case title == "":
			return Result{}, fmt.Errorf("%w: finding %d has no title", ErrInvalidOutput, i)
		case !validSeverity(sev):
			return Result{}, fmt.Errorf("%w: finding %d has unknown severity %q", ErrInvalidOutput, i, f.Severity)
		case f.LineStart < 1:
			return Result{}, fmt.Errorf("%w: finding %d has line_start %d", ErrInvalidOutput, i, f.LineStart)
		}
		end := f.LineEnd
		if end < f.LineStart {
			end = f.LineStart
		}
		nf := Finding{
			FilePath:    strings.TrimPrefix(file, "./"),
			LineStart:   f.LineStart,
			LineEnd:     end,
			Severity:    sev,
			Category:    normaliseCategory(f.Category),
			Title:       truncateRunes(title, maxTitleRunes),
			Explanation: truncateRunes(strings.TrimSpace(f.Explanation), maxExplanationRunes),
		}
		if s := f.Suggestion; s != nil && s.Original != s.Suggested && (s.Original != "" || s.Suggested != "") {
			nf.Suggestion = &Suggestion{Original: s.Original, Suggested: s.Suggested}
		}
		res.Findings = append(res.Findings, nf)
	}
	return res, nil
}

// Sanitize keeps only findings that are verifiably about the chunk the model
// was shown, and attaches a unified diff to each surviving suggestion. It
// returns how many findings were dropped.
//
//   - the file must be in the chunk;
//   - the line range must overlap lines the commit actually touched or showed;
//   - a suggestion's "original" must match the real new-side lines (we accept
//     up to 3 lines of drift and re-anchor, because models miscount); a
//     suggestion that does not match is removed but the finding is kept.
func Sanitize(res Result, chunk Chunk) (Result, int) {
	newSide := map[string]map[int]string{} // path -> line number -> text
	ranges := map[string][][2]int{}        // path -> [start,end] of hunks (new side)
	for _, p := range chunk.Parts {
		m := newSide[p.Path]
		if m == nil {
			m = map[int]string{}
			newSide[p.Path] = m
		}
		for _, h := range p.Hunks {
			for _, l := range h.Lines {
				if l.Kind != '-' {
					m[l.NewNo] = l.Text
				}
			}
			if h.NewLines > 0 {
				ranges[p.Path] = append(ranges[p.Path], [2]int{h.NewStart, h.NewStart + h.NewLines - 1})
			}
		}
	}

	dropped := 0
	kept := make([]Finding, 0, len(res.Findings))
	for _, f := range res.Findings {
		lines, ok := newSide[f.FilePath]
		if !ok || !overlaps(ranges[f.FilePath], f.LineStart, f.LineEnd) {
			dropped++
			continue
		}
		if f.Suggestion != nil {
			if start, ok := anchor(lines, f.LineStart, f.Suggestion.Original); ok {
				n := lineCount(f.Suggestion.Original)
				f.LineStart, f.LineEnd = start, start+max(n, 1)-1
				f.Suggestion.UnifiedDiff = UnifiedDiff(f.FilePath, start, f.Suggestion.Original, f.Suggestion.Suggested)
			} else {
				f.Suggestion = nil
			}
		}
		kept = append(kept, f)
	}
	res.Findings = kept
	return res, dropped
}

func overlaps(rs [][2]int, start, end int) bool {
	for _, r := range rs {
		if start <= r[1] && end >= r[0] {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func lineCount(s string) int { return len(splitLines(s)) }

// anchor finds where the suggestion's original text really sits, trying the
// stated line first and then a few lines either side. Comparison ignores
// trailing whitespace. An empty original means a pure insertion at `at`.
func anchor(lines map[int]string, at int, original string) (int, bool) {
	orig := splitLines(original)
	if len(orig) == 0 {
		_, ok := lines[at]
		return at, ok
	}
	matches := func(start int) bool {
		for i, o := range orig {
			t, ok := lines[start+i]
			if !ok || strings.TrimRight(t, " \t\r") != strings.TrimRight(o, " \t\r") {
				return false
			}
		}
		return true
	}
	for _, d := range []int{0, -1, 1, -2, 2, -3, 3} {
		if at+d >= 1 && matches(at+d) {
			return at + d, true
		}
	}
	return 0, false
}

var severityRank = map[string]int{SeverityCritical: 0, SeverityMajor: 1, SeverityMinor: 2, SeverityInfo: 3}

// SortFindings orders by severity, then file, then line, and removes exact
// duplicates (same file, line and title), which appear when chunks overlap.
func SortFindings(fs []Finding) []Finding {
	seen := map[string]bool{}
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		k := fmt.Sprintf("%s\x00%d\x00%s", f.FilePath, f.LineStart, strings.ToLower(f.Title))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] < severityRank[b.Severity]
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		return a.LineStart < b.LineStart
	})
	return out
}

// Score is computed in code, never by the model, so it is comparable across
// commits and model changes: 100 - 15*critical - 7*major - 2*minor, floor 0.
func Score(fs []Finding) int {
	s := 100
	for _, f := range fs {
		switch f.Severity {
		case SeverityCritical:
			s -= 15
		case SeverityMajor:
			s -= 7
		case SeverityMinor:
			s -= 2
		}
	}
	if s < 0 {
		return 0
	}
	return s
}
