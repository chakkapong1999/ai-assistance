package review

import (
	"strings"
	"testing"
)

func ctxChunk() Chunk {
	h := Hunk{OldStart: 10, OldLines: 6, NewStart: 10, NewLines: 7}
	n := 10
	add := func(k byte, t string) {
		l := Line{Kind: k, Text: t}
		if k != '-' {
			l.NewNo = n
			n++
		}
		h.Lines = append(h.Lines, l)
	}
	add(' ', "a")
	add(' ', "b")
	add('-', "old")
	add('+', "new1")
	add('+', "new2")
	add(' ', "c")
	add(' ', "d")
	add(' ', "e")
	add(' ', "f")
	return Chunk{Parts: []Part{{Path: "x.go", Hunks: []Hunk{h}}}}
}

func TestCodeContextIsAHunkAroundTheFinding(t *testing.T) {
	got := CodeContext(ctxChunk(), "x.go", 12, 12) // new1
	want := "@@ -10,5 +10,6 @@\n a\n b\n-old\n+new1\n+new2\n c\n d"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCodeContextHeaderStartsAtTheFirstShownLine(t *testing.T) {
	// "e" is new line 16; context starts 3 lines earlier, at the second added
	// line, which sits at old line 13 because one line was removed above it.
	got := CodeContext(ctxChunk(), "x.go", 16, 16)
	want := "@@ -13,4 +13,5 @@\n+new2\n c\n d\n e\n f"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCodeContextEmptyWhenNotInChunk(t *testing.T) {
	if CodeContext(ctxChunk(), "other.go", 12, 12) != "" || CodeContext(ctxChunk(), "x.go", 99, 99) != "" {
		t.Fatal("want empty")
	}
}

func TestCodeContextIsCapped(t *testing.T) {
	h := Hunk{OldStart: 1, OldLines: 500, NewStart: 1, NewLines: 500}
	for i := 1; i <= 500; i++ {
		h.Lines = append(h.Lines, Line{Kind: ' ', Text: strings.Repeat("x", 100), NewNo: i})
	}
	got := CodeContext(Chunk{Parts: []Part{{Path: "f", Hunks: []Hunk{h}}}}, "f", 1, 500)
	if len(got) > contextMaxBytes+100 || strings.Count(got, "\n") > contextMaxLines+contextPad+1 {
		t.Fatalf("not capped: %d bytes, %d lines", len(got), strings.Count(got, "\n"))
	}
}
