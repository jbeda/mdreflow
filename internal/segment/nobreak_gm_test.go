package segment

import "testing"

// TestNoBreakSpansDegenerateFallback checks the "whole cluster passes
// through unbroken" fallback: text whose parse yields anything but a
// single Paragraph (an all-indented line, which the paragraph parser
// declines, producing an empty document) reports one span covering the
// whole text.
func TestNoBreakSpansDegenerateFallback(t *testing.T) {
	text := "    all indented, not a paragraph"
	spans := NoBreakSpans(text)
	if !fullyCovered(spans, 0, len(text)) {
		t.Fatalf("NoBreakSpans(%q) = %v, want full coverage (degenerate fallback)", text, spans)
	}
}

// TestCodeSpansDegenerateFallback checks CodeSpans's degenerate fallback,
// which is nil (the unprotected direction), not a whole-text span.
func TestCodeSpansDegenerateFallback(t *testing.T) {
	text := "    all indented, not a paragraph"
	spans := CodeSpans(text)
	if spans != nil {
		t.Fatalf("CodeSpans(%q) = %v, want nil", text, spans)
	}
}

// TestNoBreakSpansUnresolvedReferenceNotProtected documents the #30
// behavior change from TestNoBreakSpansCover: an unresolved "[shortcut
// ref]" is plain bracket prose to the ask-goldmark parse (no reference
// definitions are registered), so it is no longer protected.
func TestNoBreakSpansUnresolvedReferenceNotProtected(t *testing.T) {
	text := "Read [ref] now."
	start, end := len("Read "), len("Read [ref]")
	spans := NoBreakSpans(text)
	if fullyCovered(spans, start, end) {
		t.Fatalf("NoBreakSpans(%q) = %v, want [ref] NOT fully covered (unresolved reference)", text, spans)
	}
}

// TestNoBreakSpansLinkifyProtectsBacktickInURL is the #29 shape: a bare
// (linkified) URL containing a backtick is AutoLink content, not a code
// span delimiter, so the backtick inside it must not pair with a later,
// genuine opening backtick.
func TestNoBreakSpansLinkifyProtectsBacktickInURL(t *testing.T) {
	text := "see https://ex.am/pa`th?x=1 and `code` more"
	urlStart := len("see ")
	urlEnd := len("see https://ex.am/pa`th?x=1")
	spans := NoBreakSpans(text)
	if !fullyCovered(spans, urlStart, urlEnd) {
		t.Fatalf("NoBreakSpans(%q) = %v, want linkified URL [%d,%d) fully covered", text, spans, urlStart, urlEnd)
	}

	codeStart := len("see https://ex.am/pa`th?x=1 and ")
	codeEnd := codeStart + len("`code`")
	if !fullyCovered(spans, codeStart, codeEnd) {
		t.Fatalf("NoBreakSpans(%q) = %v, want `code` [%d,%d) fully covered", text, spans, codeStart, codeEnd)
	}
}

// TestCodeSpansLinkifyPairsCorrectly checks that CodeSpans, over the same
// #29 shape, does not treat the backtick inside the bare URL as an
// opening delimiter: the only real code span is "`code`".
func TestCodeSpansLinkifyPairsCorrectly(t *testing.T) {
	text := "see https://ex.am/pa`th?x=1 and `code` more"
	wantStart := len("see https://ex.am/pa`th?x=1 and ")
	wantEnd := wantStart + len("`code`")

	spans := CodeSpans(text)
	if len(spans) != 1 {
		t.Fatalf("CodeSpans(%q) = %v, want exactly one span (the URL backtick must not pair)", text, spans)
	}
	if spans[0].Start != wantStart || spans[0].End != wantEnd {
		t.Fatalf("CodeSpans(%q) = %v, want [%d,%d)", text, spans, wantStart, wantEnd)
	}
}

// TestNoBreakSpansLinkTitleWithSpaces checks that a link's destination
// and title, including internal spaces, are fully protected as part of
// the complement (link syntax has no allowed Text ancestry).
func TestNoBreakSpansLinkTitleWithSpaces(t *testing.T) {
	text := `See [the docs](https://example.com/ab "a title with spaces") now.`
	start := len("See ")
	end := len(`See [the docs](https://example.com/ab "a title with spaces")`)
	spans := NoBreakSpans(text)
	if !fullyCovered(spans, start, end) {
		t.Fatalf("NoBreakSpans(%q) = %v, want link [%d,%d) fully covered", text, spans, start, end)
	}
}

// TestNoBreakSpansEmphasisInteriorBreakable checks that plain text inside
// emphasis is breakable (not part of the complement): only the "*"
// delimiters themselves are no-break.
func TestNoBreakSpansEmphasisInteriorBreakable(t *testing.T) {
	text := "a *phrase with several words* b"
	spans := NoBreakSpans(text)
	interiorStart := len("a *phrase ")
	if isInSomeSpan(spans, interiorStart) {
		t.Fatalf("NoBreakSpans(%q) = %v, want the space at %d (inside emphasis text) to be breakable", text, spans, interiorStart)
	}
	// The delimiters themselves must still be protected.
	openDelim := len("a ")
	if !isInSomeSpan(spans, openDelim) {
		t.Fatalf("NoBreakSpans(%q) = %v, want the opening \"*\" at %d protected", text, spans, openDelim)
	}
}

// TestNoBreakSpansCodeSpanProtected checks that a code span's interior
// (which would otherwise look like ordinary breakable prose) is fully
// protected, not just its delimiters.
func TestNoBreakSpansCodeSpanProtected(t *testing.T) {
	text := "a `code with spaces` b"
	start := len("a ")
	end := start + len("`code with spaces`")
	spans := NoBreakSpans(text)
	if !fullyCovered(spans, start, end) {
		t.Fatalf("NoBreakSpans(%q) = %v, want code span [%d,%d) fully covered", text, spans, start, end)
	}
}

// TestCodeSpansMultiLine checks that a code span crossing a "\n" (a
// paragraph's lines joined that way, per CodeSpans's doc comment) is
// reported as one span spanning the join.
func TestCodeSpansMultiLine(t *testing.T) {
	text := "before `code\nspans a line` after"
	wantStart := len("before ")
	wantEnd := wantStart + len("`code\nspans a line`")

	spans := CodeSpans(text)
	if len(spans) != 1 {
		t.Fatalf("CodeSpans(%q) = %v, want exactly one span", text, spans)
	}
	if spans[0].Start != wantStart || spans[0].End != wantEnd {
		t.Fatalf("CodeSpans(%q) = %v, want [%d,%d)", text, spans, wantStart, wantEnd)
	}
}

// TestNoBreakSpansEmptyInput checks the empty-string edge case: no
// parse, no spans, no panic.
func TestNoBreakSpansEmptyInput(t *testing.T) {
	if spans := NoBreakSpans(""); spans != nil {
		t.Fatalf(`NoBreakSpans("") = %v, want nil`, spans)
	}
	if spans := CodeSpans(""); spans != nil {
		t.Fatalf(`CodeSpans("") = %v, want nil`, spans)
	}
}

// TestNoBreakSpansSingleWord checks a single-word input: nothing to
// protect, no spans, no panic.
func TestNoBreakSpansSingleWord(t *testing.T) {
	if spans := NoBreakSpans("hello"); spans != nil {
		t.Fatalf(`NoBreakSpans("hello") = %v, want nil`, spans)
	}
	if spans := CodeSpans("hello"); spans != nil {
		t.Fatalf(`CodeSpans("hello") = %v, want nil`, spans)
	}
}

// isInSomeSpan reports whether pos falls within any of spans.
func isInSomeSpan(spans []Span, pos int) bool {
	for _, s := range spans {
		if s.Start <= pos && pos < s.End {
			return true
		}
	}
	return false
}
