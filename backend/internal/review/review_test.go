package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
)

// sampleDiff has: a modified Go file (2 hunks, one removed line starting with
// "-- "), a new file, a deleted file, a lockfile, a binary file and a rename.
const sampleDiff = `diff --git a/svc/user.go b/svc/user.go
index 111..222 100644
--- a/svc/user.go
+++ b/svc/user.go
@@ -10,3 +10,4 @@ func Load() {
 	a := 1
-	b := 2
+	b := compute()
+	use(b)
 	c := 3
@@ -40,3 +41,3 @@ func Save() {
 	x := 1
--- not a header, a removed comment
+	y := 2
 	z := 3
diff --git a/svc/new.go b/svc/new.go
new file mode 100644
--- /dev/null
+++ b/svc/new.go
@@ -0,0 +1,2 @@
+package svc
+func New() {}
diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,1 +0,0 @@
-package old
diff --git a/package-lock.json b/package-lock.json
--- a/package-lock.json
+++ b/package-lock.json
@@ -1,1 +1,1 @@
-{}
+{"a":1}
diff --git a/logo.png b/logo.png
Binary files a/logo.png and b/logo.png differ
diff --git a/a.txt b/b.txt
similarity index 90%
rename from a.txt
rename to b.txt
--- a/a.txt
+++ b/b.txt
@@ -1,1 +1,1 @@
-old
+new
`

func TestParseDiff(t *testing.T) {
	files := ParseDiff(sampleDiff)
	if len(files) != 6 {
		t.Fatalf("files = %d, want 6: %+v", len(files), files)
	}
	u := files[0]
	if u.Path != "svc/user.go" || u.Status != "modified" || len(u.Hunks) != 2 {
		t.Fatalf("user.go parsed wrong: %+v", u)
	}
	// Line numbering on the new side: context 10, added 11 and 12, context 13.
	var got []int
	for _, l := range u.Hunks[0].Lines {
		if l.Kind != '-' {
			got = append(got, l.NewNo)
		}
	}
	if want := []int{10, 11, 12, 13}; !equalInts(got, want) {
		t.Fatalf("hunk0 new lines = %v, want %v", got, want)
	}
	// The "--- not a header" line must be a removed line, not a file header.
	second := u.Hunks[1].Lines
	if second[1].Kind != '-' || !strings.HasPrefix(second[1].Text, "-- not a header") {
		t.Fatalf("hunk1 line 1 = %+v", second[1])
	}
	if files[1].Status != "added" || files[1].Path != "svc/new.go" {
		t.Fatalf("new.go: %+v", files[1])
	}
	if files[2].Status != "deleted" || files[2].Path != "old.go" {
		t.Fatalf("old.go: %+v", files[2])
	}
	if !files[4].Binary {
		t.Fatalf("png not binary: %+v", files[4])
	}
	if r := files[5]; r.Status != "renamed" || r.Path != "b.txt" || r.OldPath != "a.txt" {
		t.Fatalf("rename: %+v", r)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilter(t *testing.T) {
	kept, skipped := Filter(ParseDiff(sampleDiff), Limits{})
	var keptPaths []string
	for _, f := range kept {
		keptPaths = append(keptPaths, f.Path)
	}
	if want := "svc/user.go,svc/new.go,b.txt"; strings.Join(keptPaths, ",") != want {
		t.Fatalf("kept = %v, want %s", keptPaths, want)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Path] = s.Reason
	}
	for path, want := range map[string]string{
		"old.go": "file deleted", "package-lock.json": "lockfile", "logo.png": "binary file",
	} {
		if reasons[path] != want {
			t.Errorf("skip reason for %s = %q, want %q", path, reasons[path], want)
		}
	}
}

func TestFilterLimits(t *testing.T) {
	big := FileDiff{Path: "big.go", Status: "modified", Hunks: []Hunk{{NewStart: 1, NewLines: 1,
		Lines: []Line{{Kind: '+', Text: strings.Repeat("x", 500), NewNo: 1}}}}}
	small := FileDiff{Path: "s.go", Status: "modified", Hunks: []Hunk{{NewStart: 1, NewLines: 1,
		Lines: []Line{{Kind: '+', Text: "x", NewNo: 1}}}}}
	kept, skipped := Filter([]FileDiff{big, small, small, small}, Limits{MaxFileBytes: 300, MaxFiles: 2})
	if len(kept) != 2 || len(skipped) != 2 {
		t.Fatalf("kept=%d skipped=%d, want 2/2: %+v", len(kept), len(skipped), skipped)
	}
}

func TestSplitKeepsHunksWholeAndGroupsByFile(t *testing.T) {
	mk := func(start int) Hunk {
		return Hunk{NewStart: start, NewLines: 1, Lines: []Line{{Kind: '+', Text: strings.Repeat("y", 100), NewNo: start}}}
	}
	files := []FileDiff{
		{Path: "a.go", Status: "modified", Hunks: []Hunk{mk(1), mk(10), mk(20)}},
		{Path: "b.go", Status: "modified", Hunks: []Hunk{mk(1)}},
	}
	// Each hunk is ~140 bytes; a 300-byte budget fits two.
	chunks := Split(files, 300)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if n := len(chunks[0].Parts[0].Hunks); n != 2 {
		t.Fatalf("first chunk hunks = %d, want 2", n)
	}
	total := 0
	for _, c := range chunks {
		for _, p := range c.Parts {
			total += len(p.Hunks)
		}
	}
	if total != 4 {
		t.Fatalf("hunks across chunks = %d, want 4 (none lost)", total)
	}
	// One hunk larger than the budget still gets reviewed, alone.
	huge := []FileDiff{{Path: "h.go", Hunks: []Hunk{{NewStart: 1, NewLines: 1,
		Lines: []Line{{Kind: '+', Text: strings.Repeat("z", 1000), NewNo: 1}}}}}}
	if got := Split(huge, 100); len(got) != 1 {
		t.Fatalf("oversized hunk chunks = %d, want 1", len(got))
	}
}

func TestRenderNumbersNewSide(t *testing.T) {
	kept, _ := Filter(ParseDiff(sampleDiff), Limits{})
	out := Split(kept[:1], 0)[0].Render()
	for _, want := range []string{"=== FILE: svc/user.go (modified) ===", "    11 + \tb := compute()", "       - \tb := 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestMaskSecrets(t *testing.T) {
	const keyBlock = "+-----BEGIN RSA PRIVATE KEY-----\n+MIIEowIBAAKCAQEA1234567890\n+abcdefghijklmnop\n+-----END RSA PRIVATE KEY-----\n"
	in := "+aws := \"AKIAIOSFODNN7EXAMPLE\"\n" +
		"+password = \"hunter2hunter2\"\n" +
		"+DB_PASSWORD=s3cr3tvalue99\n" +
		"+url := \"https://bob:p4ssw0rd@example.com/x\"\n" +
		"+h := \"Authorization: Bearer abcdefghijklmnop1234567890\"\n" +
		"+ghp := \"ghp_abcdefghijklmnopqrstuvwxyz0123456789\"\n" +
		keyBlock +
		"+token: string\n" +
		"+safe := compute(1)\n"
	out := MaskSecrets(in)

	for _, leak := range []string{"AKIAIOSFODNN7EXAMPLE", "hunter2hunter2", "s3cr3tvalue99", "p4ssw0rd",
		"abcdefghijklmnop1234567890", "ghp_abcdef", "MIIEowIBAAKCAQEA", "abcdefghijklmnop\n"} {
		if strings.Contains(out, leak) {
			t.Errorf("secret %q leaked:\n%s", leak, out)
		}
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Fatal("masking must not add or remove lines")
	}
	for _, keep := range []string{"+token: string", "+safe := compute(1)", "-----BEGIN RSA PRIVATE KEY-----", "-----END RSA PRIVATE KEY-----"} {
		if !strings.Contains(out, keep) {
			t.Errorf("over-masked, lost %q:\n%s", keep, out)
		}
	}
}

func TestMaskSecretsTruncatedKeyDoesNotSwallowNextFile(t *testing.T) {
	in := "+-----BEGIN PRIVATE KEY-----\n+AAAA\n@@ -1,1 +1,1 @@\n+safe line\n"
	if out := MaskSecrets(in); !strings.Contains(out, "+safe line") {
		t.Fatalf("key state leaked past hunk boundary:\n%s", out)
	}
}

func TestParseOutput(t *testing.T) {
	good := "Here you go:\n```json\n" + `{"summary":"ok","findings":[{"file":"./a.go","line_start":3,"line_end":2,"severity":"MAJOR","category":"weird","title":"T","explanation":"E","suggestion":{"original":"x","suggested":"y"}}]}` + "\n```"
	res, err := ParseOutput([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	f := res.Findings[0]
	if f.FilePath != "a.go" || f.Severity != SeverityMajor || f.Category != CategoryOther || f.LineEnd != 3 || f.Suggestion == nil {
		t.Fatalf("normalisation wrong: %+v", f)
	}

	bad := map[string]string{
		"no json":      "I could not review this.",
		"broken json":  `{"summary": }`,
		"no file":      `{"findings":[{"line_start":1,"severity":"major","title":"t"}]}`,
		"no title":     `{"findings":[{"file":"a","line_start":1,"severity":"major"}]}`,
		"bad severity": `{"findings":[{"file":"a","line_start":1,"severity":"urgent","title":"t"}]}`,
		"bad line":     `{"findings":[{"file":"a","line_start":0,"severity":"major","title":"t"}]}`,
		"wrong types":  `{"findings":"none"}`,
	}
	for name, raw := range bad {
		if _, err := ParseOutput([]byte(raw)); !errors.Is(err, ErrInvalidOutput) {
			t.Errorf("%s: err = %v, want ErrInvalidOutput", name, err)
		}
	}
	if res, err := ParseOutput([]byte(`{"summary":"fine","findings":[]}`)); err != nil || len(res.Findings) != 0 {
		t.Fatalf("empty findings should be valid: %v %+v", err, res)
	}
}

func chunkFor(t *testing.T) Chunk {
	t.Helper()
	kept, _ := Filter(ParseDiff(sampleDiff), Limits{})
	return Split(kept[:1], 0)[0]
}

func TestSanitize(t *testing.T) {
	ch := chunkFor(t)
	res := Result{Findings: []Finding{
		{FilePath: "svc/user.go", LineStart: 11, LineEnd: 11, Severity: SeverityMajor, Title: "ok",
			Suggestion: &Suggestion{Original: "\tb := compute()", Suggested: "\tb, err := compute()"}},
		{FilePath: "svc/other.go", LineStart: 1, LineEnd: 1, Severity: SeverityMajor, Title: "unknown file"},
		{FilePath: "svc/user.go", LineStart: 500, LineEnd: 500, Severity: SeverityMajor, Title: "line outside diff"},
		// Model is one line off: the real text is on line 12.
		{FilePath: "svc/user.go", LineStart: 11, LineEnd: 11, Severity: SeverityMinor, Title: "drift",
			Suggestion: &Suggestion{Original: "\tuse(b)", Suggested: "\tuse(b, 1)"}},
		// Invented original text: finding stays, suggestion goes.
		{FilePath: "svc/user.go", LineStart: 11, LineEnd: 11, Severity: SeverityMinor, Title: "hallucinated",
			Suggestion: &Suggestion{Original: "does not exist", Suggested: "x"}},
	}}
	out, dropped := Sanitize(res, ch)
	if dropped != 2 || len(out.Findings) != 3 {
		t.Fatalf("dropped=%d kept=%d, want 2/3: %+v", dropped, len(out.Findings), out.Findings)
	}
	if d := out.Findings[0].Suggestion.UnifiedDiff; !strings.Contains(d, "@@ -11,1 +11,1 @@") || !strings.Contains(d, "-\tb := compute()") || !strings.Contains(d, "+\tb, err := compute()") {
		t.Fatalf("unified diff wrong:\n%s", d)
	}
	if f := out.Findings[1]; f.LineStart != 12 || f.Suggestion == nil {
		t.Fatalf("drifted finding not re-anchored to 12: %+v", f)
	}
	if out.Findings[2].Suggestion != nil {
		t.Fatal("hallucinated suggestion should be removed")
	}
}

func TestUnifiedDiff(t *testing.T) {
	got := UnifiedDiff("a.go", 5, "old1\nold2\n", "new1\n")
	want := "--- a/a.go\n+++ b/a.go\n@@ -5,2 +5,1 @@\n-old1\n-old2\n+new1\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := UnifiedDiff("a.go", 5, "", "ins\n"); !strings.Contains(got, "@@ -4,0 +5,1 @@") {
		t.Fatalf("insertion header wrong:\n%s", got)
	}
	if got := UnifiedDiff("a.go", 5, "gone\n", ""); !strings.Contains(got, "@@ -5,1 +4,0 @@") {
		t.Fatalf("deletion header wrong:\n%s", got)
	}
}

func TestScoreAndSort(t *testing.T) {
	fs := []Finding{
		{Severity: SeverityMinor, FilePath: "b", LineStart: 1, Title: "m"},
		{Severity: SeverityCritical, FilePath: "z", LineStart: 9, Title: "c"},
		{Severity: SeverityMajor, FilePath: "a", LineStart: 2, Title: "M"},
		{Severity: SeverityInfo, FilePath: "a", LineStart: 1, Title: "i"},
		{Severity: SeverityMajor, FilePath: "a", LineStart: 2, Title: "m"}, // duplicate of the major (title case-insensitive)
	}
	sorted := SortFindings(fs)
	var order []string
	for _, f := range sorted {
		order = append(order, f.Severity)
	}
	if got := strings.Join(order, ","); got != "critical,major,minor,info" {
		t.Fatalf("order = %s (duplicate not removed or misordered)", got)
	}
	if got := Score(sorted); got != 100-15-7-2 {
		t.Fatalf("score = %d, want 76", got)
	}
	many := make([]Finding, 10)
	for i := range many {
		many[i].Severity = SeverityCritical
	}
	if Score(many) != 0 {
		t.Fatal("score must floor at 0")
	}
	if Score(nil) != 100 {
		t.Fatal("no findings = 100")
	}
}

func TestMockScenarios(t *testing.T) {
	ch := chunkFor(t)
	req := Request{Chunk: ch}
	ctx := context.Background()

	if res, err := (Mock{Scenario: config.ScenarioClean}).Review(ctx, req); err != nil || len(res.Findings) != 0 || res.Model != ModelMock {
		t.Fatalf("clean: %v %+v", err, res)
	}
	if _, err := (Mock{Scenario: config.ScenarioInvalidJSON}).Review(ctx, req); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("invalid_json: %v", err)
	}
	var ule *UsageLimitError
	if _, err := (Mock{Scenario: config.ScenarioUsageLimit}).Review(ctx, req); !errors.As(err, &ule) {
		t.Fatalf("usage_limit: %v", err)
	}
	if _, err := (Mock{Scenario: config.ScenarioTimeout}).Review(ctx, req); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	res, err := (Mock{Scenario: config.ScenarioFindings}).Review(ctx, req)
	if err != nil || len(res.Findings) != 2 {
		t.Fatalf("findings: %v %+v", err, res)
	}
	// The mock's own findings must survive the real validation.
	if _, dropped := Sanitize(res, ch); dropped != 0 {
		t.Fatalf("mock findings were dropped by Sanitize: %d", dropped)
	}
	// Delay honours cancellation.
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := (Mock{Scenario: config.ScenarioClean, Delay: time.Hour}).Review(cctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delay not cancellable: %v", err)
	}
}

func TestFactory(t *testing.T) {
	rv, err := New(config.Config{ReviewerMode: config.ReviewerMock, MockReviewScenario: config.ScenarioClean})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rv.(Mock); !ok {
		t.Fatalf("got %T", rv)
	}
	rv, _ = New(config.Config{ReviewerMode: config.ReviewerClaudeCLI, ClaudeBin: "claude"})
	if _, ok := rv.(*ClaudeCLI); !ok {
		t.Fatalf("got %T", rv)
	}
	if _, err := New(config.Config{ReviewerMode: "gpt"}); err == nil {
		t.Fatal("unknown mode must error")
	}
}

func TestRunWithMock(t *testing.T) {
	ctx := context.Background()
	out, err := Run(ctx, Mock{Scenario: config.ScenarioFindings}, Input{Repo: "r", Commit: "c", Diff: sampleDiff}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reviewable || out.Model != ModelMock || len(out.Findings) != 2 || out.Score != 100-7-2 {
		t.Fatalf("outcome: %+v", out)
	}
	if len(out.Skipped) != 3 {
		t.Fatalf("skipped = %+v, want deleted+lockfile+binary", out.Skipped)
	}

	// Everything filtered out: not an error, and the reviewer is never called.
	lockOnly := "diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1,1 +1,1 @@\n-a\n+b\n"
	out, err = Run(ctx, failingReviewer{}, Input{Diff: lockOnly}, Limits{})
	if err != nil || out.Reviewable || len(out.Skipped) != 1 {
		t.Fatalf("lockfile-only: %v %+v", err, out)
	}
	// Empty diff is skipped, garbage is an error.
	if out, err := Run(ctx, failingReviewer{}, Input{Diff: "  \n"}, Limits{}); err != nil || out.Reviewable {
		t.Fatalf("empty: %v %+v", err, out)
	}
	if _, err := Run(ctx, failingReviewer{}, Input{Diff: "hello world"}, Limits{}); !errors.Is(err, ErrUnparseableDiff) {
		t.Fatalf("garbage: %v", err)
	}
}

type failingReviewer struct{}

func (failingReviewer) Review(context.Context, Request) (Result, error) {
	return Result{}, errors.New("reviewer must not be called")
}

func TestRunErrorKindsSurviveWrapping(t *testing.T) {
	var ule *UsageLimitError
	_, err := Run(context.Background(), Mock{Scenario: config.ScenarioUsageLimit}, Input{Diff: sampleDiff}, Limits{})
	if !errors.As(err, &ule) {
		t.Fatalf("usage limit lost in wrapping: %v", err)
	}
	_, err = Run(context.Background(), Mock{Scenario: config.ScenarioInvalidJSON}, Input{Diff: sampleDiff}, Limits{})
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("invalid output lost in wrapping: %v", err)
	}
}

// capturingReviewer records what the model would have seen.
type capturingReviewer struct{ seen []string }

func (c *capturingReviewer) Review(_ context.Context, r Request) (Result, error) {
	c.seen = append(c.seen, BuildPrompt(r))
	return Result{Model: "cap"}, nil
}

func TestSecretsNeverReachReviewer(t *testing.T) {
	diff := "diff --git a/c.go b/c.go\n--- a/c.go\n+++ b/c.go\n@@ -1,1 +1,2 @@\n a\n+key := \"AKIAIOSFODNN7EXAMPLE\"\n"
	cr := &capturingReviewer{}
	if _, err := Run(context.Background(), cr, Input{Diff: diff, Message: "m"}, Limits{}); err != nil {
		t.Fatal(err)
	}
	if len(cr.seen) != 1 || strings.Contains(cr.seen[0], "AKIAIOSFODNN7EXAMPLE") || !strings.Contains(cr.seen[0], "[REDACTED]") {
		t.Fatalf("prompt leaked or unmasked:\n%v", cr.seen)
	}
}

func TestRunMultiChunkAggregates(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for i := 0; i < 6; i++ {
		start := i*100 + 1
		b.WriteString("@@ -" + strconv.Itoa(start) + ",1 +" + strconv.Itoa(start) + ",1 @@\n")
		b.WriteString("+" + strings.Repeat("q", 400) + "\n")
	}
	out, err := Run(context.Background(), Mock{Scenario: config.ScenarioFindings}, Input{Diff: b.String()}, Limits{MaxChunkBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if out.Chunks < 2 {
		t.Fatalf("expected several chunks, got %d", out.Chunks)
	}
	// Each chunk gets 2 mock findings at different lines; none are lost or deduped.
	if len(out.Findings) != 2*out.Chunks {
		t.Fatalf("findings = %d, want %d", len(out.Findings), 2*out.Chunks)
	}
}

// --- Claude CLI, through a real process -----------------------------------

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func cliRequest(t *testing.T) Request {
	return Request{Repo: "r", Commit: "c", Chunk: chunkFor(t)}
}

func TestClaudeCLIHappyPathReadsStdinAndIsolatesCwd(t *testing.T) {
	// The fake echoes how it was called so the test can assert on it, and
	// answers in the real envelope shape.
	bin := fakeClaude(t, `
cat > "$TMPDIR_OUT_STDIN" 2>/dev/null || cat >/dev/null
printf '%s' "$*" > "$TMPDIR_OUT_ARGS"
pwd > "$TMPDIR_OUT_PWD"
cat <<'EOF'
{"type":"result","subtype":"success","is_error":false,"result":"{\"summary\":\"fine\",\"findings\":[{\"file\":\"svc/user.go\",\"line_start\":11,\"severity\":\"minor\",\"category\":\"style\",\"title\":\"t\"}]}"}
EOF
`)
	dir := t.TempDir()
	t.Setenv("TMPDIR_OUT_STDIN", filepath.Join(dir, "stdin"))
	t.Setenv("TMPDIR_OUT_ARGS", filepath.Join(dir, "args"))
	t.Setenv("TMPDIR_OUT_PWD", filepath.Join(dir, "pwd"))

	res, err := NewClaudeCLI(CLIOptions{Bin: bin, Model: "m1"}).Review(context.Background(), cliRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != ModelClaudeCLI || len(res.Findings) != 1 || res.Summary != "fine" {
		t.Fatalf("result: %+v", res)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if got := string(args); got != "-p --output-format json --max-turns 1 --tools  --model m1" {
		t.Fatalf("args = %q", got)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if !strings.Contains(string(stdin), "=== FILE: svc/user.go") || !strings.Contains(string(stdin), "untrusted") {
		t.Fatalf("prompt not delivered on stdin:\n%s", stdin)
	}
	pwd, _ := os.ReadFile(filepath.Join(dir, "pwd"))
	if !strings.Contains(string(pwd), "aicr-claude-") {
		t.Fatalf("cwd = %q, want a throwaway directory", pwd)
	}
	if _, err := os.Stat(strings.TrimSpace(string(pwd))); !os.IsNotExist(err) {
		t.Fatal("temp dir was not cleaned up")
	}
}

func TestClaudeCLIErrors(t *testing.T) {
	ctx := context.Background()
	req := cliRequest(t)
	var ule *UsageLimitError

	// Usage limit reported through the JSON envelope, exit 1.
	bin := fakeClaude(t, `cat >/dev/null; echo '{"is_error":true,"result":"Claude AI usage limit reached|1900000000"}'; exit 1`)
	c := NewClaudeCLI(CLIOptions{Bin: bin})
	c.now = func() time.Time { return time.Unix(1899999000, 0) }
	_, err := c.Review(ctx, req)
	if !errors.As(err, &ule) {
		t.Fatalf("want UsageLimitError, got %v", err)
	}
	if want := 1000*time.Second + time.Minute; ule.RetryAfter != want {
		t.Fatalf("RetryAfter = %s, want %s", ule.RetryAfter, want)
	}

	// Usage limit on stderr only.
	bin = fakeClaude(t, `cat >/dev/null; echo "Error: rate limit exceeded" >&2; exit 1`)
	if _, err := NewClaudeCLI(CLIOptions{Bin: bin}).Review(ctx, req); !errors.As(err, &ule) || ule.RetryAfter != DefaultUsageLimitWait {
		t.Fatalf("stderr limit: %v", err)
	}

	// Other failure is a plain error, not a usage limit and not invalid output.
	bin = fakeClaude(t, `cat >/dev/null; echo "boom" >&2; exit 3`)
	_, err = NewClaudeCLI(CLIOptions{Bin: bin}).Review(ctx, req)
	if err == nil || errors.As(err, &ule) || errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("plain failure: %v", err)
	}

	// Exit 0 but the model answered with prose.
	bin = fakeClaude(t, `cat >/dev/null; echo '{"is_error":false,"result":"Looks good to me!"}'`)
	if _, err := NewClaudeCLI(CLIOptions{Bin: bin}).Review(ctx, req); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("prose answer: %v", err)
	}

	// Exit 0 but stdout is not the envelope at all.
	bin = fakeClaude(t, `cat >/dev/null; echo 'hello'`)
	if _, err := NewClaudeCLI(CLIOptions{Bin: bin}).Review(ctx, req); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("non-json stdout: %v", err)
	}

	// Binary missing.
	if _, err := NewClaudeCLI(CLIOptions{Bin: "/nonexistent/claude"}).Review(ctx, req); err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestClaudeCLITimeoutKillsProcess(t *testing.T) {
	bin := fakeClaude(t, `exec sleep 30`)
	start := time.Now()
	_, err := NewClaudeCLI(CLIOptions{Bin: bin, Timeout: 200 * time.Millisecond}).Review(context.Background(), cliRequest(t))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("process was not killed promptly")
	}
}

func TestClaudeCLIParentCancelIsNotATimeout(t *testing.T) {
	bin := fakeClaude(t, `exec sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := NewClaudeCLI(CLIOptions{Bin: bin, Timeout: time.Minute}).Review(ctx, cliRequest(t))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestClassifyUsageLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if _, ok := ClassifyUsageLimit("Something unrelated failed", now); ok {
		t.Fatal("false positive")
	}
	e, ok := ClassifyUsageLimit("5-hour limit reached ∙ resets 3pm", now)
	if !ok || e.RetryAfter != DefaultUsageLimitWait {
		t.Fatalf("limit without epoch should use the default wait: %v %v", ok, e)
	}
	e, ok = ClassifyUsageLimit("Claude AI usage limit reached|1700003600", now)
	if !ok || e.RetryAfter != time.Hour+time.Minute {
		t.Fatalf("epoch parse: %v %v", ok, e)
	}
	// A reset time in the past or absurdly far away is bounded.
	if e, _ := ClassifyUsageLimit("usage limit|1600000000", now); e.RetryAfter != DefaultUsageLimitWait {
		t.Fatalf("past epoch should fall back to default, got %s", e.RetryAfter)
	}
	if e, _ := ClassifyUsageLimit("usage limit|4000000000", now); e.RetryAfter != maxUsageWait {
		t.Fatalf("far epoch should be capped, got %s", e.RetryAfter)
	}
}

func TestParseDiffSurvivesWrongHunkCounts(t *testing.T) {
	// Header claims 5 old lines but only 2 follow; the next hunk and file must still parse.
	d := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,5 +1,5 @@\n x\n+y\n@@ -20,1 +20,1 @@\n-p\n+q\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,1 +1,1 @@\n-m\n+n\n"
	files := ParseDiff(d)
	if len(files) != 2 || len(files[0].Hunks) != 2 || files[1].Path != "b.go" {
		t.Fatalf("recovery failed: %+v", files)
	}
}

func TestClaudeCLIReportsUsage(t *testing.T) {
	ctx := context.Background()
	// Cached input counts as input: it was still sent to the model.
	bin := fakeClaude(t, `cat >/dev/null; cat <<'EOF'
{"type":"result","is_error":false,"result":"{\"summary\":\"ok\",\"findings\":[]}","total_cost_usd":0.0421,"usage":{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":3000,"output_tokens":55}}
EOF
`)
	res, err := NewClaudeCLI(CLIOptions{Bin: bin}).Review(ctx, cliRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if u := res.Usage; !u.Known || u.InputTokens != 3210 || u.OutputTokens != 55 || u.CostUSD != 0.0421 {
		t.Fatalf("usage = %+v", u)
	}
	// An envelope without usage is "not measured", never zero.
	bin = fakeClaude(t, `cat >/dev/null; echo '{"is_error":false,"result":"{\"summary\":\"ok\",\"findings\":[]}"}'`)
	res, err = NewClaudeCLI(CLIOptions{Bin: bin}).Review(ctx, cliRequest(t))
	if err != nil || res.Usage.Known {
		t.Fatalf("usage = %+v, err = %v; want not known", res.Usage, err)
	}
}

type usageReviewer struct{ Mock }

func (u usageReviewer) Review(ctx context.Context, r Request) (Result, error) {
	res, err := u.Mock.Review(ctx, r)
	res.Usage = Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 0.5, Known: true}
	return res, err
}

func TestRunSumsUsageAcrossChunks(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for i := 0; i < 6; i++ {
		start := i*100 + 1
		b.WriteString("@@ -" + strconv.Itoa(start) + ",1 +" + strconv.Itoa(start) + ",1 @@\n+" + strings.Repeat("q", 400) + "\n")
	}
	rv := usageReviewer{Mock{Scenario: config.ScenarioFindings}}
	out, err := Run(context.Background(), rv, Input{Diff: b.String()}, Limits{MaxChunkBytes: 1000})
	if err != nil || out.Chunks < 2 {
		t.Fatalf("chunks=%d err=%v", out.Chunks, err)
	}
	n := out.Chunks
	if u := out.Usage; !u.Known || u.InputTokens != 100*n || u.OutputTokens != 10*n || u.CostUSD != 0.5*float64(n) {
		t.Fatalf("usage over %d chunks = %+v", n, u)
	}
	// The mock reports nothing, and that stays "unknown".
	out, _ = Run(context.Background(), Mock{Scenario: config.ScenarioFindings}, Input{Diff: b.String()}, Limits{MaxChunkBytes: 1000})
	if out.Usage.Known {
		t.Fatalf("mock usage must be unknown: %+v", out.Usage)
	}
}

func TestPullRequestPromptSaysItIsAPullRequest(t *testing.T) {
	ch := Split(ParseDiff(sampleDiffForPrompt), 1<<20)[0]
	pr := BuildPrompt(Request{Repo: "acme/api", Commit: "abc123", Message: "Add x\n\nbody", Author: "Al", Chunk: ch, PullRequest: true})
	for _, want := range []string{"of one pull request", "all its commits", "Pull request head: abc123", "Pull request title and description (untrusted)", "Add x"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pull request prompt lacks %q", want)
		}
	}
	if strings.Contains(pr, "git commit") {
		t.Error("pull request prompt still talks about one git commit")
	}
	cm := BuildPrompt(Request{Repo: "acme/api", Commit: "abc123", Message: "Add x", Author: "Al", Chunk: ch})
	if !strings.Contains(cm, "of one git commit") || strings.Contains(cm, "pull request") || !strings.Contains(cm, "Commit: abc123") {
		t.Error("the commit prompt changed")
	}
}

const sampleDiffForPrompt = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n x\n+y\n"

func TestCLIFailureDetailIsNeverEmpty(t *testing.T) {
	var env cliEnvelope
	_ = json.Unmarshal([]byte(`{"is_error":true,"subtype":"error_max_turns","result":""}`), &env)
	if got := cliFailureDetail(env, nil, RunResult{ExitCode: 1}); !strings.Contains(got, "error_max_turns") {
		t.Fatalf("subtype missing: %q", got)
	}
	got := cliFailureDetail(cliEnvelope{}, errors.New("x"), RunResult{Stdout: []byte("Error: not logged in")})
	if !strings.Contains(got, "not logged in") {
		t.Fatalf("stdout missing: %q", got)
	}
	if got := cliFailureDetail(cliEnvelope{}, errors.New("x"), RunResult{}); got == "" {
		t.Fatal("empty message")
	}
}
