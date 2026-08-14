package segment

import (
	"github.com/yuin/goldmark/ast"
)

// inlineOpenerOffsets returns the byte offsets in text at which an inline
// node that can open a sentence begins: a code span or an emphasis run,
// each measured from its opening delimiter rather than its content.
//
// This is the ask-goldmark half of the sentence-start rule (docs/design.md,
// "A sentence may also open with inline markup"). Breaks cannot decide the
// question from the leading byte alone: a backtick that opens no code span,
// or an asterisk that opens no emphasis, is plain text and must keep
// joining, and only the parse knows which it is. The leading byte is a
// prefilter that decides whether to ask; this answers.
//
// A degenerate parse — anything but exactly one Paragraph block, the same
// bar parseSingleParagraph sets for NoBreakSpans — returns nil, so every
// candidate is refused and the segmenter falls back to the character
// allow-set alone. Refusing is the safe direction here: it joins, which is
// what the pipeline already did.
func inlineOpenerOffsets(text string) map[int]struct{} {
	para, ok := parseSingleParagraph(text)
	if !ok {
		return nil
	}
	out := make(map[int]struct{})
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if start, ok := inlineNodeStart(text, c); ok {
				out[start] = struct{}{}
			}
			walk(c)
		}
	}
	walk(para)
	return out
}

// inlineNodeStart returns the byte offset of n's opening delimiter, for
// the node kinds that can open a sentence. Neither kind carries a source
// extent of its own — goldmark records segments only on ast.Text — so the
// opener is derived from the node's leftmost content and the delimiter run
// that must precede it.
//
// The recursion through the leftmost child is what makes a nested
// construct exact rather than approximate: emphasis wrapping a code span
// resolves the code span's own opener first, then steps back over the
// emphasis delimiters, so bold-italic and italic-around-code both report
// the outermost delimiter. Scanning left over a raw run of "*" and "_"
// bytes instead would stop at the wrong byte for the code-span case and
// would over-reach past an escaped delimiter ("\**x*") for the plain one;
// ast.Emphasis.Level is the run's exact length, so no scan is needed.
func inlineNodeStart(text string, n ast.Node) (int, bool) {
	switch n.Kind() {
	case ast.KindCodeSpan:
		first, ok := n.FirstChild().(*ast.Text)
		if !ok {
			// An empty backtick pair has no content to measure back
			// from, and opens nothing worth breaking before.
			return 0, false
		}
		return delimRunStart(text, first.Segment.Start), true
	case ast.KindEmphasis:
		e := n.(*ast.Emphasis)
		c := n.FirstChild()
		if c == nil {
			return 0, false
		}
		inner, ok := inlineNodeStart(text, c)
		if !ok {
			t, isText := c.(*ast.Text)
			if !isText {
				return 0, false
			}
			inner = t.Segment.Start
		}
		if inner-e.Level < 0 {
			return 0, false
		}
		return inner - e.Level, true
	}
	return 0, false
}

// isInlineOpenerByte reports whether b can begin a code span or an
// emphasis run, and so is worth an inlineOpenerOffsets parse. It is a
// prefilter only: a byte passing it still has to be confirmed against the
// parse, since an unmatched delimiter is plain text.
func isInlineOpenerByte(b byte) bool {
	return b == '`' || b == '*' || b == '_'
}
