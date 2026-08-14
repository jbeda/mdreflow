package segment

import (
	"reflect"
	"testing"
)

// TestBreaksInlineOpeners tables the AST-confirmed sentence-opener rule
// (docs/design.md, "A sentence may also open with inline markup"): a
// sentence beginning with a code span or an emphasis run breaks from the
// one before it, while a delimiter byte that opens neither keeps joining.
//
// Every want is a single break at the space after "ends." (offset 16), or
// nil for the cases that must stay joined, so the table reads as a
// yes/no on the opener rule rather than as offset arithmetic.
func TestBreaksInlineOpeners(t *testing.T) {
	const prefix = "A sentence ends." // len 16, so the gap is [16,17)
	gap := []Span{{Start: 16, End: 17}}

	cases := []struct {
		name string
		rest string
		want []Span
	}{
		// The three shapes from issue #59.
		{"code span opens", "`code` opens it.", gap},
		{"strong opens", "**Bold** opens it.", gap},
		{"emphasis opens", "*Italic* opens it.", gap},

		// Underscore emphasis is the same node kind.
		{"underscore emphasis opens", "_Under_ opens it.", gap},

		// Nesting: the outermost delimiter is what lands at the line
		// start, so inlineNodeStart must resolve through the children.
		{"bold italic opens", "***Both*** opens it.", gap},
		{"emphasis around code opens", "*`code`* opens it.", gap},
		{"code around emphasis is a code span", "`*x*` opens it.", gap},

		// An unmatched delimiter parses as plain text, not a node, and
		// must keep joining — the case a character allow-set gets wrong.
		{"unmatched backtick joins", "`unmatched here.", nil},
		{"unmatched asterisk joins", "*unmatched here.", nil},
		{"unmatched underscore joins", "_unmatched here.", nil},

		// Emphasis requires non-whitespace after the opening delimiter,
		// so these are literal punctuation, not openers.
		{"spaced asterisk joins", "* spaced here.", nil},
		{"spaced underscore joins", "_ spaced here.", nil},

		// The lowercase-word rule is untouched: this is what keeps
		// "e.g. foo" intact, and the opener rule must not widen it.
		{"lowercase word still joins", "lowercase here.", nil},

		// A backslash-escaped delimiter renders as a literal character
		// and opens nothing; the offset the parse is asked about is the
		// escaped byte's, which carries no node.
		{"escaped asterisk joins", "\\*escaped* here.", nil},
	}

	seg := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := prefix + " " + tc.rest
			got := seg.Breaks(text)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Breaks(%q) = %v, want %v", text, got, tc.want)
			}
		})
	}
}

// TestBreaksFenceShapedCodeSpanStillOffered checks the division of work
// between this package and reflow. A code span whose delimiter run is
// three or more backticks IS a sentence opener and Breaks offers it. The
// refusal lives in reflow.filterLineStartHazards, which is the only layer
// that knows the candidate would put a fence opener at a line start; see
// TestSentenceBreakBeforeFenceShapedCodeSpan there for the other half.
//
// Asserting it here pins the boundary: if the opener rule ever grew its
// own delimiter-length guard, the two layers would silently both be
// enforcing it and the reflow-side test would stop proving anything.
func TestBreaksFenceShapedCodeSpanStillOffered(t *testing.T) {
	seg := New(nil)
	text := "A sentence ends. ```lit``` opens it."
	want := []Span{{Start: 16, End: 17}}
	if got := seg.Breaks(text); !reflect.DeepEqual(got, want) {
		t.Errorf("Breaks(%q) = %v, want %v", text, got, want)
	}
}

// TestInlineOpenerOffsetsDegenerateParse covers the fallback direction:
// text that does not parse as exactly one paragraph yields no openers, so
// every candidate falls back to the character allow-set and joins.
func TestInlineOpenerOffsetsDegenerateParse(t *testing.T) {
	// An all-indented line: the paragraph parser declines it outright,
	// producing an empty document (see parseSingleParagraph).
	if got := inlineOpenerOffsets("    indented"); got != nil {
		t.Errorf("inlineOpenerOffsets(indented) = %v, want nil", got)
	}
	// Two blocks, not one.
	if got := inlineOpenerOffsets("a\n\nb"); got != nil {
		t.Errorf("inlineOpenerOffsets(two blocks) = %v, want nil", got)
	}
	if got := inlineOpenerOffsets(""); got != nil {
		t.Errorf("inlineOpenerOffsets(empty) = %v, want nil", got)
	}
}

// TestInlineOpenerOffsetsPositions pins the offsets themselves, which the
// Breaks-level table cannot see: an opener is reported at its opening
// delimiter, not at its content.
func TestInlineOpenerOffsetsPositions(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []int
	}{
		{"code span at zero", "`c` x", []int{0}},
		{"strong at zero", "**b** x", []int{0}},
		{"emphasis mid text", "ab *c* d", []int{3}},
		// Nested emphasis reports each level's own delimiter: the outer
		// "*" at 0 and the inner "**" at 1. Only offset 0 can ever be a
		// break candidate, since a candidate must follow whitespace and
		// an inner delimiter is always preceded by the outer one.
		{"nested reports each level's delimiter", "***b***", []int{0, 1}},
		{"emphasis around code", "*`c`*", []int{0, 1}},
		{"no openers in plain text", "plain text here", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inlineOpenerOffsets(tc.text)
			for _, w := range tc.want {
				if _, ok := got[w]; !ok {
					t.Errorf("inlineOpenerOffsets(%q) = %v, missing offset %d", tc.text, got, w)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("inlineOpenerOffsets(%q) = %v, want exactly offsets %v", tc.text, got, tc.want)
			}
		})
	}
}
