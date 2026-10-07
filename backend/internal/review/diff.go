package review

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Line is one line of a hunk. Kind is '+', '-' or ' '. NewNo is the 1-based
// line number in the new file (0 for removed lines).
type Line struct {
	Kind  byte
	Text  string
	NewNo int
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []Line
}

func (h Hunk) size() int {
	n := 32
	for _, l := range h.Lines {
		n += len(l.Text) + 8
	}
	return n
}

// FileDiff is one file's section of a git-style unified diff.
type FileDiff struct {
	Path    string // path after the commit (before it, for deletions)
	OldPath string
	Status  string // added, deleted, modified, renamed
	Binary  bool
	Hunks   []Hunk
}

func (f FileDiff) size() int {
	n := 0
	for _, h := range f.Hunks {
		n += h.size()
	}
	return n
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func stripPrefix(p string) string {
	switch {
	case strings.HasPrefix(p, "a/"), strings.HasPrefix(p, "b/"):
		return p[2:]
	}
	return p
}

// ParseDiff parses a git-style unified diff (what Bitbucket returns). Hunk
// bodies are consumed by the counts in their header, so a removed line that
// happens to start with "-- " is never mistaken for a file header.
func ParseDiff(text string) []FileDiff {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var files []FileDiff
	var cur *FileDiff
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		ln := lines[i]

		if strings.HasPrefix(ln, "diff --git ") {
			flush()
			cur = &FileDiff{Status: "modified"}
			// Fallback names from the header; "--- / +++ / rename" override.
			rest := strings.TrimPrefix(ln, "diff --git ")
			if j := strings.LastIndex(rest, " b/"); j >= 0 {
				cur.OldPath = stripPrefix(rest[:j])
				cur.Path = rest[j+1+2:]
			}
			continue
		}
		if cur == nil {
			continue
		}

		switch {
		case strings.HasPrefix(ln, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(ln, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(ln, "rename from "):
			cur.Status = "renamed"
			cur.OldPath = strings.TrimPrefix(ln, "rename from ")
		case strings.HasPrefix(ln, "rename to "):
			cur.Status = "renamed"
			cur.Path = strings.TrimPrefix(ln, "rename to ")
		case strings.HasPrefix(ln, "Binary files "), strings.HasPrefix(ln, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(ln, "--- "):
			if p := strings.TrimPrefix(ln, "--- "); p != "/dev/null" {
				cur.OldPath = stripPrefix(p)
			}
		case strings.HasPrefix(ln, "+++ "):
			if p := strings.TrimPrefix(ln, "+++ "); p != "/dev/null" {
				cur.Path = stripPrefix(p)
			}
		case strings.HasPrefix(ln, "@@ "):
			m := hunkHeader.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			h := Hunk{
				OldStart: atoiDefault(m[1], 0), OldLines: atoiDefault(m[2], 1),
				NewStart: atoiDefault(m[3], 0), NewLines: atoiDefault(m[4], 1),
			}
			oldLeft, newLeft := h.OldLines, h.NewLines
			newNo := h.NewStart
			for oldLeft > 0 || newLeft > 0 {
				i++
				if i >= len(lines) {
					break
				}
				l := lines[i]
				// Content lines always carry a prefix, so a raw "@@ " or
				// "diff --git" means the header counts were wrong: stop
				// rather than swallow the next hunk or file.
				if strings.HasPrefix(l, "@@ ") || strings.HasPrefix(l, "diff --git ") {
					i--
					break
				}
				if strings.HasPrefix(l, `\`) { // "\ No newline at end of file"
					continue
				}
				kind, body := byte(' '), l
				if l != "" {
					kind, body = l[0], l[1:]
				}
				switch kind {
				case '+':
					h.Lines = append(h.Lines, Line{Kind: '+', Text: body, NewNo: newNo})
					newNo++
					newLeft--
				case '-':
					h.Lines = append(h.Lines, Line{Kind: '-', Text: body})
					oldLeft--
				default: // context (an empty line is context whose space was stripped)
					h.Lines = append(h.Lines, Line{Kind: ' ', Text: body, NewNo: newNo})
					newNo++
					oldLeft--
					newLeft--
				}
			}
			// Swallow a trailing "\ No newline" marker belonging to this hunk.
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], `\`) {
				i++
			}
			cur.Hunks = append(cur.Hunks, h)
		}
	}
	flush()

	for k := range files {
		f := &files[k]
		if f.Status == "deleted" && f.Path == "" {
			f.Path = f.OldPath
		}
		if f.Path == "" {
			f.Path = f.OldPath
		}
	}
	return files
}

// Limits bound what is sent to the model.
type Limits struct {
	MaxFileBytes  int // files bigger than this are skipped
	MaxChunkBytes int // soft limit per reviewer call; a single hunk may exceed it
	MaxFiles      int // files beyond this are skipped
}

// DefaultLimits are conservative so one call stays well inside a context
// window and a 10-minute job timeout.
func DefaultLimits() Limits {
	return Limits{MaxFileBytes: 200_000, MaxChunkBytes: 60_000, MaxFiles: 100}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxChunkBytes <= 0 {
		l.MaxChunkBytes = d.MaxChunkBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	return l
}

// Skipped records why a file was not reviewed so the dashboard can show it.
type Skipped struct {
	Path   string
	Reason string
}

var (
	lockfiles = map[string]bool{
		"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
		"go.sum": true, "cargo.lock": true, "poetry.lock": true,
		"composer.lock": true, "gemfile.lock": true, "pipfile.lock": true,
		"gradle.lockfile": true, "pubspec.lock": true, "podfile.lock": true,
	}
	skipDirs     = []string{"node_modules/", "vendor/", "dist/", ".next/", "__pycache__/", "target/classes/"}
	skipSuffixes = []string{
		".min.js", ".min.css", ".map", ".pb.go", "_generated.go", ".gen.go", ".g.dart", ".freezed.dart",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".svg", ".pdf", ".zip", ".jar", ".gz",
		".woff", ".woff2", ".ttf", ".eot", ".mp4", ".mov", ".class",
	}
)

func skipReason(f FileDiff) string {
	lower := strings.ToLower(f.Path)
	base := path.Base(lower)
	switch {
	case f.Binary:
		return "binary file"
	case f.Status == "deleted":
		return "file deleted"
	case len(f.Hunks) == 0:
		return "no textual changes"
	case lockfiles[base]:
		return "lockfile"
	}
	for _, d := range skipDirs {
		if strings.HasPrefix(lower, d) || strings.Contains(lower, "/"+d) {
			return "vendored or build output"
		}
	}
	for _, s := range skipSuffixes {
		if strings.HasSuffix(lower, s) {
			return "generated, minified or asset file"
		}
	}
	return ""
}

// Filter drops files that are not worth (or not safe to) send to a model.
func Filter(files []FileDiff, lim Limits) (kept []FileDiff, skipped []Skipped) {
	lim = lim.withDefaults()
	for _, f := range files {
		switch reason := skipReason(f); {
		case reason != "":
			skipped = append(skipped, Skipped{f.Path, reason})
		case f.size() > lim.MaxFileBytes:
			skipped = append(skipped, Skipped{f.Path, fmt.Sprintf("file diff larger than %d bytes", lim.MaxFileBytes)})
		case len(kept) >= lim.MaxFiles:
			skipped = append(skipped, Skipped{f.Path, fmt.Sprintf("more than %d files in commit", lim.MaxFiles)})
		default:
			kept = append(kept, f)
		}
	}
	return kept, skipped
}

// Part is the subset of one file's hunks that lands in one chunk.
type Part struct {
	Path   string
	Status string
	Hunks  []Hunk
}

// Chunk is what one reviewer call sees.
type Chunk struct {
	Parts []Part
}

// Paths lists the files in the chunk.
func (c Chunk) Paths() []string {
	out := make([]string, 0, len(c.Parts))
	for _, p := range c.Parts {
		out = append(out, p.Path)
	}
	return out
}

// Render prints the chunk with new-file line numbers so the model does not
// have to count lines (it is bad at it).
//
//	=== FILE: a/b.go (modified) ===
//	@@ -10,3 +10,4 @@
//	   10   context
//	   11 + added
//	      - removed
func (c Chunk) Render() string {
	var b strings.Builder
	for _, p := range c.Parts {
		fmt.Fprintf(&b, "=== FILE: %s (%s) ===\n", p.Path, p.Status)
		for _, h := range p.Hunks {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
			for _, l := range h.Lines {
				switch l.Kind {
				case '+':
					fmt.Fprintf(&b, "%6d + %s\n", l.NewNo, l.Text)
				case '-':
					fmt.Fprintf(&b, "       - %s\n", l.Text)
				default:
					fmt.Fprintf(&b, "%6d   %s\n", l.NewNo, l.Text)
				}
			}
		}
	}
	return b.String()
}

// Split groups hunks into chunks of roughly maxBytes. A hunk is never split,
// so a single huge hunk becomes its own (oversized) chunk; Filter already
// bounds a whole file by MaxFileBytes.
func Split(files []FileDiff, maxBytes int) []Chunk {
	if maxBytes <= 0 {
		maxBytes = DefaultLimits().MaxChunkBytes
	}
	var chunks []Chunk
	var cur Chunk
	curBytes := 0
	flush := func() {
		if len(cur.Parts) > 0 {
			chunks = append(chunks, cur)
		}
		cur, curBytes = Chunk{}, 0
	}
	for _, f := range files {
		for _, h := range f.Hunks {
			sz := h.size()
			if curBytes > 0 && curBytes+sz > maxBytes {
				flush()
			}
			if n := len(cur.Parts); n > 0 && cur.Parts[n-1].Path == f.Path {
				cur.Parts[n-1].Hunks = append(cur.Parts[n-1].Hunks, h)
			} else {
				cur.Parts = append(cur.Parts, Part{Path: f.Path, Status: f.Status, Hunks: []Hunk{h}})
			}
			curBytes += sz
		}
	}
	flush()
	return chunks
}
