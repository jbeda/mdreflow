package blockmap

import (
	"testing"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"
)

// TestLineStart checks lineStart's doc comment: the byte offset of the
// start of the physical line containing pos.
func TestLineStart(t *testing.T) {
	source := []byte("first\nsecond\nthird")
	cases := []struct {
		name string
		pos  int
		want int
	}{
		{"start of first line", 0, 0},
		{"mid first line", 3, 0},
		{"start of second line", 6, 6},
		{"mid second line", 9, 6},
		{"start of third line", 13, 13},
		{"end of source", len(source), 13},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lineStart(source, tc.pos); got != tc.want {
				t.Errorf("lineStart(%q, %d) = %d, want %d", source, tc.pos, got, tc.want)
			}
			// LineStart is the exported wrapper; must agree.
			if got := LineStart(source, tc.pos); got != tc.want {
				t.Errorf("LineStart(%q, %d) = %d, want %d", source, tc.pos, got, tc.want)
			}
		})
	}
}

// TestContinuationPrefix checks continuationPrefix's doc comment: every
// '>' byte in the first line's own container prefix is kept literally,
// everything else (list markers, digits, padding) becomes a space, at the
// same byte width.
func TestContinuationPrefix(t *testing.T) {
	cases := []struct {
		name         string
		source       string
		contentStart int
		want         string
	}{
		{"no container prefix", "prose here", 0, ""},
		{"bullet list marker becomes spaces", "- item", 2, "  "},
		{"ordered list marker becomes spaces", "1. item", 3, "   "},
		{"blockquote marker keeps the angle bracket", "> quote", 2, "> "},
		{"nested blockquote keeps both angle brackets", ">> quote", 3, ">> "},
		{"blockquote plus list marker: only angle brackets kept", "> - item", 4, ">   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := continuationPrefix([]byte(tc.source), tc.contentStart)
			if got != tc.want {
				t.Errorf("continuationPrefix(%q, %d) = %q, want %q", tc.source, tc.contentStart, got, tc.want)
			}
			if len(got) != tc.contentStart {
				t.Errorf("continuationPrefix(%q, %d) byte width = %d, want %d (must match the original prefix width)", tc.source, tc.contentStart, len(got), tc.contentStart)
			}
		})
	}
}

// TestFrontMatterEnd tables frontMatterEnd's doc comment: requires an
// exact "---" opener on source's first line and a later exact "---"
// closer line; an unterminated or absent opener means no front matter at
// all.
func TestFrontMatterEnd(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   int
	}{
		{
			name:   "simple front matter",
			source: "---\ntitle: x\n---\nbody\n",
			want:   len("---\ntitle: x\n---\n"),
		},
		{
			name:   "front matter with no trailing newline on closer",
			source: "---\ntitle: x\n---",
			want:   len("---\ntitle: x\n---"),
		},
		{
			name:   "no opener at all",
			source: "title: x\n---\nbody\n",
			want:   -1,
		},
		{
			name:   "opener with no closer treated as absent",
			source: "---\ntitle: x\nbody\n",
			want:   -1,
		},
		{
			name:   "closing delimiter must be exact, not a longer dash run",
			source: "---\ntitle: x\n----\nbody\n",
			want:   -1,
		},
		{
			name:   "CRLF line endings still recognized",
			source: "---\r\ntitle: x\r\n---\r\nbody\r\n",
			want:   len("---\r\ntitle: x\r\n---\r\n"),
		},
		{
			name:   "empty source has no front matter",
			source: "",
			want:   -1,
		},
		{
			name:   "closing dots not recognized as a closer",
			source: "---\ntitle: x\n...\nbody\n",
			want:   -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := frontMatterEnd([]byte(tc.source)); got != tc.want {
				t.Errorf("frontMatterEnd(%q) = %d, want %d", tc.source, got, tc.want)
			}
		})
	}
}

// TestInLinkRefDefZone tables inLinkRefDefZone's doc comment: design.md's
// blunt, shape-based link-reference-definition zone predicate. It replaces
// the prior precededByBlankLine, precededByBareLinkRefDefLine,
// isSelfCompleteLinkRefDef, and hasPossibleLinkRefDefOpener truth tables —
// the outcomes those pinned are re-expressed here as zone in/out verdicts,
// per design.md's "The link-reference-definition zone: skip bluntly, by
// shape" (some outcomes deliberately changed: see the per-case comments).
func TestInLinkRefDefZone(t *testing.T) {
	cases := []struct {
		name         string
		source       string
		trimmed      []string
		contentStart int
		want         bool
	}{
		// (a): the paragraph's own lines contain a non-footnote def shape.
		{"opener present", "[foo]: bar", []string{"[foo]: bar"}, 0, true},
		{"opener mid line still matches (unanchored)", "prose [foo]: bar more", []string{"prose [foo]: bar more"}, 0, true},
		{"footnote-shaped label excluded", "[^foo]: bar", []string{"[^foo]: bar"}, 0, false},
		{"no opener at all", "just prose", []string{"just prose"}, 0, false},
		{"opener on a later line", "prose\n[foo]:", []string{"prose", "[foo]:"}, 0, true},
		{"reflow-escaped spelling still matches", `\[foo]: bar`, []string{`\[foo]: bar`}, 0, true},

		// (b): the raw source line directly above contentStart opens with
		// a non-footnote def shape (no blank line between, by construction
		// — a blank line can never match the shape).
		{"bare link ref def line above", "[foo]:\nbar", []string{"bar"}, 7, true},
		{"leading space allowed above", "  [foo]:\nbar", []string{"bar"}, 9, true},
		{"blockquote-nested def line above", ">[foo]:\nbar", []string{"bar"}, 8, true},
		{"destination-carrying def line above still counts (design.md drops the bare-only restriction)", "[foo]: /url\nbar", []string{"bar"}, 12, true},
		{
			// Unlike (a), a caret-led label above DOES still count here:
			// mdreflow's goldmark configuration has no footnote extension
			// (package reflow's isCompleteLinkRefDefLine doc comment), so
			// "[^label]:" is nothing special to the parser when it belongs
			// to a different, already-parsed sibling — the exact same
			// multi-line-destination hazard applies. Found by FuzzFormat on
			// "[^0]:\n0\n\"\"0" (issue-class regression): this paragraph's
			// own first line ("0") is not itself a footnote opener, so the
			// exemption below does not apply, and skipping it here is what
			// keeps single-pass reflow idempotent.
			name:   "caret-led label above still counts when this paragraph is not itself a footnote body",
			source: "[^foo]:\nbar", trimmed: []string{"bar"}, contentStart: 8, want: true,
		},
		{
			// The ordinary back-to-back footnote layout stays eligible
			// with no special case: a COMPLETE footnote-body line above
			// ("[^1]: first") sets none of the neighbor facts, so nothing
			// fires (see inLinkRefDefZone's no-exemption comment).
			name:         "back-to-back footnote bodies stay eligible (complete def line above sets no facts)",
			source:       "[^1]: first\n[^2]: second",
			trimmed:      []string{"[^2]: second"},
			contentStart: 12,
			want:         false,
		},
		{
			// The exemption shields against caret evidence ONLY (#41): a
			// non-caret "[label]:" chain start above freezes a
			// footnote-shaped paragraph like any other, because the
			// titleless def completes its destination from the line below
			// and joining changes whether it forms. Found by FuzzFormat on
			// " [0]:\n [^0]:0\n\"\"0" (seed issue41_def_above_footnote_body).
			name:         "#41: non-caret def line above freezes a footnote-shaped paragraph",
			source:       " [0]:\n[^0]:0",
			trimmed:      []string{"[^0]:0"},
			contentStart: 6,
			want:         true,
		},
		{
			// The caret side is hazardous too: a bare caret opener is a
			// titleless definition to the footnote-less parser and
			// completes its destination from the line below, so it
			// freezes a footnote-shaped paragraph the same way. Found by
			// FuzzFormat on " [^0]:\n [^0]:0\n\"\"0" (seed
			// issue41_caret_above_footnote_body) four minutes into the
			// post-#41 soak, which reflowed under the exemption the first
			// #41 fix kept for caret evidence.
			name:         "bare caret opener above a footnote-shaped paragraph freezes it too",
			source:       "[^0]:\n[^1]: body more",
			trimmed:      []string{"[^1]: body more"},
			contentStart: 6,
			want:         true,
		},
		{
			// Transitive reach behaves the same: a non-caret chain
			// start farther up the run freezes a footnote-shaped
			// paragraph...
			name:         "#41 transitive: non-caret def beyond the immediate neighbor freezes a footnote body",
			source:       "[0]:\nx\n[^1]:0",
			trimmed:      []string{"[^1]:0"},
			contentStart: 7,
			want:         true,
		},
		{
			// ...and so does a bare caret opener farther up: the
			// transitive bit carries all three evidence kinds uniformly.
			name:         "transitive caret reach freezes a footnote body too",
			source:       "[^0]:\nx y\n[^1]: body",
			trimmed:      []string{"[^1]: body"},
			contentStart: 10,
			want:         true,
		},
		{
			// The same transitive caret reach still freezes a paragraph
			// that is NOT itself footnote-shaped.
			name:         "transitive caret reach still counts for a non-footnote paragraph",
			source:       "[^0]:\nx y\nplain",
			trimmed:      []string{"plain"},
			contentStart: 10,
			want:         true,
		},
		{
			// (c): the def shape spans the boundary — label opens on the
			// preceding raw line (via an escaped closing bracket) and
			// closes on this paragraph's own first line. Found by
			// FuzzFormat/issue#11 on "[\]\n]:0\n\"\"0".
			name:         "def shape spans the boundary via an escaped bracket",
			source:       "[\\]\n]:0",
			trimmed:      []string{"]:0"},
			contentStart: 4,
			want:         true,
		},
		{"ordinary prose line above does not qualify", "just prose\nbar", []string{"bar"}, 11, false},
		{"contentStart at start of source: no previous line", "bar", []string{"bar"}, 0, false},
		{"blank line above disqualifies (shape can never match blank text)", "[foo]:\n\nbar", []string{"bar"}, 8, false},
		{
			// A def line farther up the same non-blank run still counts:
			// its title scan can reach the paragraph across intervening
			// lines (seed 97329a80dd2cb7d4; see scanLineFacts).
			name:         "def line beyond the immediate neighbor, same non-blank run",
			source:       "[1]:0\n\"20\n0\n00\nbar",
			trimmed:      []string{"bar"},
			contentStart: 15,
			want:         true,
		},
		{
			// A blank line between resets the run: the def's reach cannot
			// cross a blank line (a blank terminates title scanning).
			name:         "def line above a blank line does not count",
			source:       "[1]:0\n\ntext\nbar",
			trimmed:      []string{"bar"},
			contentStart: 12,
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inLinkRefDefZone([]byte(tc.source), tc.trimmed, tc.contentStart, scanLineFacts([]byte(tc.source))); (got != SkipNone) != tc.want {
				t.Errorf("inLinkRefDefZone(%q, %v, %d) = %v, want in-zone=%v", tc.source, tc.trimmed, tc.contentStart, got, tc.want)
			}
		})
	}
}

// paragraphEligibleAt reports whether the paragraph containing needle (a
// substring expected to appear exactly once in src) is reflow-eligible —
// i.e. NOT frozen by the link-reference-definition zone. It parses src
// under the default dialect, finds needle's byte offset, and checks
// whether that offset falls inside any of Paragraphs' returned ranges
// (only reflow-eligible paragraphs are returned at all; a frozen one is
// skipped and never appears there — see collect's use of build's skip
// return).
func paragraphEligibleAt(t *testing.T, src, needle string) bool {
	t.Helper()
	b := []byte(src)
	idx := indexOf(src, needle)
	if idx < 0 {
		t.Fatalf("needle %q not found in source %q", needle, src)
	}
	doc := gm.New().Parser().Parse(text.NewReader(b))
	for _, p := range Paragraphs(doc, b) {
		if idx >= p.Start && idx < p.End {
			return true
		}
	}
	return false
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestNeighborDefShapeReachability covers inLinkRefDefZone's check (c)
// after the chain-reachability narrowing (issue #37): a mid-line
// "[label]:" shape freezes its neighbor only when a definition chain
// could actually reach it, and defChainStartRE's marker tolerance closes
// the blindspot the narrowing depends on (a definition opening after a
// list marker now freezes its own run transitively, the same as a
// top-level one).
func TestNeighborDefShapeReachability(t *testing.T) {
	t.Run("issue #37: mid-prose shape in a code span no longer freezes the sibling bullet", func(t *testing.T) {
		src := "- See `runnerGroups[0]: priorityClassName is not allowed` here for more context now.\n" +
			"- This continues with more prose and no special characters at all now.\n"
		// The first bullet stays ineligible: its OWN line contains the
		// shape, caught by the untouched contains check (a).
		if got := paragraphEligibleAt(t, src, "See `runnerGroups"); got {
			t.Errorf("first bullet eligible = %v, want false (contains check (a) still fires)", got)
		}
		// The second bullet is now eligible: the shape on the line above
		// has prose ("See `runnerGroups") to its left, so no definition
		// chain could reach it.
		if got := paragraphEligibleAt(t, src, "This continues"); !got {
			t.Errorf("second bullet eligible = %v, want true", got)
		}
	})

	t.Run("mid-line shape with prose to its left, non-list layout, still unfreezes its neighbor", func(t *testing.T) {
		src := "# see `x[l]: u` here\n" +
			"plain paragraph line without brackets at all here for length.\n"
		if got := paragraphEligibleAt(t, src, "plain paragraph"); !got {
			t.Errorf("eligible = %v, want true", got)
		}
	})

	t.Run("marker-def single step: a list-marker-led definition still freezes its sibling", func(t *testing.T) {
		src := "- [a]: /url\n" +
			"- plain sibling paragraph text with more words here now indeed.\n"
		if got := paragraphEligibleAt(t, src, "plain sibling"); got {
			t.Errorf("eligible = %v, want false (defChainStartRE must match the marker-led opener)", got)
		}
	})

	// The remaining cases exercise inLinkRefDefZone/scanLineFacts
	// directly, mirroring TestInLinkRefDefZone's own pattern: the shapes
	// under test (a non-marker numeral-paren prefix, and a multi-line
	// transitive reach through a marker-led opener) either collapse into
	// a single AST paragraph or fail to split into the separate blocks
	// needed under a real parse, so the function-level check is the
	// direct way to pin down the byte-level verdict.
	t.Run("marker-def transitive: reach carries across intervening lines like a top-level opener", func(t *testing.T) {
		// Mirrors the seed 97329a80dd2cb7d4 case in TestInLinkRefDefZone
		// ("[1]:0\n\"20\n0\n00\nbar") with the opener now led by a list
		// marker — new coverage from defChainStartRE's marker tolerance.
		source := "- [1]:0\n\"20\n0\n00\nbar"
		trimmed := []string{"bar"}
		contentStart := 17
		if got := inLinkRefDefZone([]byte(source), trimmed, contentStart, scanLineFacts([]byte(source))); got == SkipNone {
			t.Errorf("inLinkRefDefZone(%q, %v, %d) = %v, want true", source, trimmed, contentStart, got)
		}
	})

	t.Run("boundary-spanning shape still fires unconditionally", func(t *testing.T) {
		source := "[ab\ncd]: x"
		trimmed := []string{"cd]: x"}
		contentStart := 4
		if got := inLinkRefDefZone([]byte(source), trimmed, contentStart, scanLineFacts([]byte(source))); got == SkipNone {
			t.Errorf("inLinkRefDefZone(%q, %v, %d) = %v, want true", source, trimmed, contentStart, got)
		}
	})

	t.Run("plausible-prefix conservatism: a numeral-paren prefix that is not a real marker still freezes", func(t *testing.T) {
		source := "3.5) [a]: x\nplain paragraph line here"
		trimmed := []string{"plain paragraph line here"}
		contentStart := 12
		if got := inLinkRefDefZone([]byte(source), trimmed, contentStart, scanLineFacts([]byte(source))); got == SkipNone {
			t.Errorf("inLinkRefDefZone(%q, %v, %d) = %v, want true", source, trimmed, contentStart, got)
		}
	})
}

// TestFootnoteDefFirstLineRE checks footnoteDefFirstLineRE's shape: a
// footnote definition's own "[^label]:" marker at the start of a trimmed
// line, per design.md's footnote continuation-indent rule.
func TestFootnoteDefFirstLineRE(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"footnote def opener", "[^1]: body", true},
		// "[^]:" is NOT footnote-shaped: goldmark's footnote extension
		// requires a non-space label character after the "^", so this is
		// an ordinary definition labeled "^" (seed 6042b560f6c7dcd2).
		{"empty caret label is a plain definition, not a footnote", "[^]: body", false},
		{"space-led caret label is a plain definition, not a footnote", "[^ ]: body", false},
		{"reflow-escaped spelling still matches", `\[^1]: body`, true},
		{"non-footnote def opener does not match", "[1]: body", false},
		{"ordinary prose does not match", "just prose", false},
		{"marker must be at the start", "prose [^1]: body", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := footnoteDefFirstLineRE.MatchString(tc.line); got != tc.want {
				t.Errorf("footnoteDefFirstLineRE.MatchString(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestHasControlByte checks hasControlByte's doc comment: a C0 control byte
// other than tab/newline/CR triggers it; tab, newline, and CR do not.
func TestHasControlByte(t *testing.T) {
	cases := []struct {
		name string
		b    string
		want bool
	}{
		{"plain prose", "hello world", false},
		{"tab allowed", "hello\tworld", false},
		{"newline allowed", "hello\nworld", false},
		{"CR allowed", "hello\rworld", false},
		{"form feed disallowed", "hello\fworld", true},
		{"vertical tab disallowed", "hello\vworld", true},
		{"NUL disallowed", "hello\x00world", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasControlByte([]byte(tc.b)); got != tc.want {
				t.Errorf("hasControlByte(%q) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

// TestLooksLikeUnterminatedTag checks looksLikeUnterminatedTag's doc
// comment: a line opening a "<letter" tag with no closing '>' anywhere on
// the same (trimmed) line.
func TestLooksLikeUnterminatedTag(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"unterminated tag", "<A", true},
		{"unterminated tag with attrs", "<A foo", true},
		{"terminated tag on same line", "<A>", false},
		{"not a tag at all", "hello", false},
		{"digit after angle bracket is not a tag start", "<3 hearts", false},
		{"empty line", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeUnterminatedTag(tc.line); got != tc.want {
				t.Errorf("looksLikeUnterminatedTag(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestHasUnbalancedBracketAndDestParen checks the bracket arm's
// nested-depth tracking (proper structural tracking across lines, not a
// per-line arithmetic sum — an unmatched close byte earlier in a line
// must not appear to "cancel out" an unrelated open byte later on the
// same line) and the paren arm's "]("-armed narrowing (issue #16: only a
// "(" immediately preceded by "]" can open an inline link destination, so
// a plain prose parenthetical spanning a line break must not trigger it).
func TestHasUnbalancedBracketAndDestParen(t *testing.T) {
	cases := []struct {
		name          string
		lines         []string
		wantBracket   bool
		wantDestParen bool
	}{
		{"balanced on one line", []string{"[foo](bar)"}, false, false},
		{"bracket left open at end of paragraph", []string{"[foo"}, true, false},
		{
			// Per the doc comment, this counts as unbalanced even though
			// it is later closed: an open left dangling at the end of any
			// non-final line is itself the hazard (a label/destination
			// spanning the line break), independent of whether a later
			// line happens to close it.
			name:          "bracket left open at end of a non-final line still counts, even though later closed",
			lines:         []string{"[foo", "bar]"},
			wantBracket:   true,
			wantDestParen: false,
		},
		{
			// Issue #16: a prose paren open across lines is not a
			// destination — nothing arms it.
			name:          "prose paren left open across lines does not arm",
			lines:         []string{"(foo", "bar"},
			wantBracket:   false,
			wantDestParen: false,
		},
		{
			// Arithmetic sum would read this as balanced (one close, one
			// open cancel out), but the close is stray/unmatched text
			// before the open, which is structurally still open — and
			// "]("-armed — at the line's end: found by FuzzFormat on
			// ")[](  \n)" (see hasUnclosedDestParen's doc comment).
			name:          "stray close then armed open on same line does not cancel arithmetically",
			lines:         []string{")[](", ")"},
			wantBracket:   false,
			wantDestParen: true,
		},
		{"open bracket alone, never closed", []string{"[", "still open"}, true, false},
		{
			name:          "armed destination paren spanning a line",
			lines:         []string{"[t](/a", "b)"},
			wantBracket:   false,
			wantDestParen: true,
		},
		{
			// The armed flag is per-paren, not depth-0-only: the same
			// spanning destination nested inside an ordinary prose
			// parenthetical is the same hazard.
			name:          "armed paren nested inside a prose paren still counts",
			lines:         []string{"(x [](  ", "\")0)"},
			wantBracket:   false,
			wantDestParen: true,
		},
		{
			// A prose paren whose interior merely *contains* a balanced
			// link does not arm: the "]("-opened paren closed on its own
			// line, and the one left open was never armed.
			name:          "prose paren containing a balanced link does not arm",
			lines:         []string{"(see [x](/y) and", "more)"},
			wantBracket:   false,
			wantDestParen: false,
		},
		{
			// Armed paren open only at the end of the *final* line: no
			// line break spans it, so nothing to guard.
			name:          "armed paren open only at end of final line does not count",
			lines:         []string{"prose", "then [t](/a"},
			wantBracket:   false,
			wantDestParen: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUnbalancedBracket(tc.lines); got != tc.wantBracket {
				t.Errorf("hasUnbalancedBracket(%v) = %v, want %v", tc.lines, got, tc.wantBracket)
			}
			if got := hasUnclosedDestParen(tc.lines); got != tc.wantDestParen {
				t.Errorf("hasUnclosedDestParen(%v) = %v, want %v", tc.lines, got, tc.wantDestParen)
			}
		})
	}
}

// TestBareCaretOpenerRE tables bareCaretOpenerRE: a footnote-shaped
// opener with nothing but whitespace after its colon, at a line's end,
// with no left-boundary requirement (seed 39bb3b34cfc62d3d — a
// definition can start immediately after a previous one's title closes).
func TestBareCaretOpenerRE(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"alone on the line", "[^0]:", true},
		{"after whitespace", "x [^0]:", true},
		{"directly after a closing title quote", `"7"[^0]:`, true},
		{"trailing spaces and CR still bare", "[^0]:  \r", true},
		{"escaped spelling", `\[^0]:`, true},
		{"content after the colon is a footnote body, not a bare opener", "[^0]: body", false},
		{"non-caret label is not this regex's business", "[0]:", false},
		{"empty caret label is not footnote-shaped", "[^]:", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bareCaretOpenerRE.MatchString(tc.line); got != tc.want {
				t.Errorf("bareCaretOpenerRE.MatchString(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestHasEmptyLine checks hasEmptyLine's simple membership test.
func TestHasEmptyLine(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"no empty line", []string{"a", "b"}, false},
		{"empty line present", []string{"a", "", "b"}, true},
		{"single empty line", []string{""}, true},
		{"empty slice", []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasEmptyLine(tc.lines); got != tc.want {
				t.Errorf("hasEmptyLine(%v) = %v, want %v", tc.lines, got, tc.want)
			}
		})
	}
}

// TestWholeNodeSkip tables dialect.go's whole-node skip rules
// (wholeNodeAny and wholeNodeAll) via wholeNodeSkip.
func TestWholeNodeSkip(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"math block fence line triggers wholeNodeAny", []string{"$$", "x = 1", "$$"}, true},
		{"toml front matter fence triggers wholeNodeAny", []string{"+++", "title = \"x\"", "+++"}, true},
		{"mdx block expr triggers wholeNodeAny", []string{"{expr}"}, true},
		{"mdx import export triggers wholeNodeAll when every line matches", []string{"import Foo from 'foo'", "export default Foo"}, true},
		{"mdx import export does not trigger when mixed with prose", []string{"import Foo from 'foo'", "some prose"}, false},
		{"ordinary prose triggers nothing", []string{"just some text"}, false},
		{"docusaurus directive fence is lineBoundary, not whole-node", []string{":::note", "body", ":::"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wholeNodeSkip(tc.lines); got != tc.want {
				t.Errorf("wholeNodeSkip(%v) = %v, want %v", tc.lines, got, tc.want)
			}
		})
	}
}

// TestIsBoundaryLine tables dialect.go's lineBoundary skip rules via
// isBoundaryLine, including the GitHub-alert-marker's BlockquoteOnly
// gating.
func TestIsBoundaryLine(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		inBlockquote bool
		want         bool
	}{
		{"docusaurus directive fence", ":::note", false, true},
		{"hugo shortcode block", "{{< foo >}}", false, true},
		{"github alert marker inside blockquote", "[!NOTE]", true, true},
		{"github alert marker outside blockquote does not count", "[!NOTE]", false, false},
		{"ordinary prose is not a boundary", "just prose", false, false},
		{"math fence is whole-node, not a line boundary", "$$", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBoundaryLine(tc.content, tc.inBlockquote); got != tc.want {
				t.Errorf("isBoundaryLine(%q, inBlockquote=%v) = %v, want %v", tc.content, tc.inBlockquote, got, tc.want)
			}
		})
	}
}

// TestMarkerLineStart tables MarkerLineStart's doc comment: a
// prefix-only, unanchored-at-end match of any lineBoundary/wholeNodeAny
// marker shape at the start of s (leading spaces/tabs allowed), excluding
// mdx-import-export by design.
func TestMarkerLineStart(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"directive fence prefix", ":::note more text", true},
		{"hugo shortcode prefix", "{{< foo >}} trailing", true},
		{"math fence prefix", "$$ trailing", true},
		{"toml front matter prefix", "+++ trailing", true},
		{"mdx block expr prefix", "{expr} trailing", true},
		{"github alert marker prefix", "[!WARNING] trailing", true},
		{"leading whitespace allowed", "   :::note", true},
		{"import/export deliberately not a marker", "import Foo from 'foo'", false},
		{"ordinary prose is not a marker", "just some prose", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkerLineStart(tc.s); got != tc.want {
				t.Errorf("MarkerLineStart(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

// TestParenGuardNarrowing pins issues #14 and #16: the paren arm of
// build's link-hazard guard fires only on a "]("-opened paren left open
// at a line end — the inline-destination hazard its doc comment names —
// never on a plain prose parenthetical spanning a line break, no matter
// what brackets appear elsewhere in the paragraph (#16: an unrelated "["
// anywhere used to re-arm it, which in link-dense prose was nearly
// always).
// A bracket inside an inline code span is literal: it cannot open a link
// label, a reference definition, or a destination, so it must not arm the
// spanning-delimiter guards. An unmatched backtick run opens no code span
// at all, so a bracket after one is ordinary prose and must still arm them.
func TestCodeSpanBracketsDoNotArmGuards(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		eligible bool
	}{
		{"code-span bracket spanning a break", "Queues N jobs\n(matrix on `runs-on: [self-hosted,\n<label>]`), analogous. Second.\n", true},
		{"code span itself spans the break", "Text with `code [span\nacross` lines here. Second sentence.\n", true},
		// An unmatched run opens no code span, so this bracket is outside
		// one and the masking does not touch it. It is eligible only
		// because the paragraph holds no "]:" for a definition to form
		// from — see couldFormLinkRefDef.
		{"unmatched backtick leaves the bracket outside any span", "Text with `unclosed [bracket\nand more. Second sentence.\n", true},
		{"backtick run length must match to close", "A ``x `y` [z\nw`` here. Second sentence.\n", true},
		{"real destination outside a code span still arms", "See [t](/a\nb) and `arr[0]` here. Second one.\n", false},
		// inLinkRefDefZone runs ahead of the masking and is deliberately blunt
		// about definition-shaped lines, so this stays skipped. Recorded so the
		// interaction is not mistaken for the masking failing.
		{"definition label inside a code span still hits the zone", "Text `[label]: /url\nmore` here. Second sentence.\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.src)
			doc := gm.New().Parser().Parse(text.NewReader(b))
			if got := len(Paragraphs(doc, b)) > 0; got != tc.eligible {
				t.Errorf("eligible = %v, want %v", got, tc.eligible)
			}
		})
	}
}

// A MkDocs admonition body is prose that every CommonMark parser sees as
// an indented code block, so it is recognized only under the mkdocs
// dialect and never by default.
func TestMkDocsAdmonitionBody(t *testing.T) {
	const body = "!!! tip \"T\"\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n"
	cases := []struct {
		name     string
		src      string
		mkdocs   bool
		eligible bool
	}{
		{"body is invisible under the default dialect", body, false, false},
		{"body reflows under the mkdocs dialect", body, true, true},
		{"bare marker with no class word is not a marker", "!!!\n\n    Some prose here. And more of it.\n", true, false},
		{"marker prefix run-on into a word is not a marker", "!!!bang\n\n    Some prose here. And more of it.\n", true, false},
		{"four bangs is not a marker", "!!!!x\n\n    Some prose here. And more of it.\n", true, false},
		{"question prefix run-on into digits is not a marker", "???01010\n\n    Some prose here. And more of it.\n", true, false},
		{"plain indented code after prose is untouched", "Some prose here.\n\n    func main() { x := 1\n    fmt.Println(x) }\n", true, false},
		{"a fenced block inside the body is untouched", "!!! note \"X\"\n\n    ```go\n    x := 1\n    ```\n", true, false},
		{"a multi-paragraph body is left alone", "!!! note \"X\"\n\n    First para here.\n\n    Second para here.\n", true, false},
		{"collapsible marker is recognized", "??? note \"X\"\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n", true, true},
		{"expandable collapsible marker with title", "???+ tip \"Expandable\"\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n", true, true},
		{"custom title", "!!! note \"Custom Title\"\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n", true, true},
		{"empty title", `!!! note ""` + "\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n", true, true},
		{"Material inline modifier", "!!! note inline\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n", true, true},
		{"Material inline end modifier with title", `!!! note inline end "Title"` + "\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.src)
			doc := gm.New().Parser().Parse(text.NewReader(b))
			got := false
			for _, p := range ParagraphsForDialect(doc, b, tc.mkdocs) {
				if _, isCode := p.Node.(*ast.CodeBlock); isCode {
					got = true
				}
			}
			if got != tc.eligible {
				t.Errorf("eligible = %v, want %v", got, tc.eligible)
			}
		})
	}
}

// A backtick inside a bare URL is destination content to goldmark's
// linkify, never a code-span delimiter, whether the backtick sits inside
// the URL or the URL sits inside a real code span elsewhere: masking
// (segment.CodeSpans, itself linkify-aware) agrees with the parse, so
// none of these shapes disqualify their paragraph.
func TestBacktickInBareURLDoesNotDisqualify(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"backtick inside a bare URL", "See https://example.com/a`b and more here.\nSecond line. Third sentence.\n"},
		{"www form", "See www.example.com/a`b here and more.\nSecond line. Third sentence.\n"},
		{"email form", "Mail a@b.com/x`y here and more text.\nSecond line. Third sentence.\n"},
		{"URL inside a code span", "Pushed to\n`oci://ghcr.io/org/chart`, with its version set. Second sentence.\n"},
		{"code span then a separate URL", "See `code` and https://example.com/x here.\nSecond line. Third sentence.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.src)
			doc := gm.New().Parser().Parse(text.NewReader(b))
			if got := len(Paragraphs(doc, b)); got == 0 {
				t.Errorf("Paragraphs = 0, want at least 1: this shape has no guard to skip it")
			}
		})
	}
}

// A MkDocs admonition written without a blank line after its marker is a
// single paragraph: the indented body is a lazy continuation. Reflowing it
// as ordinary prose joins the marker into the body and drops the indent,
// and the callout stops being one. The marker boundary is mkdocs-only, so
// this exercises ParagraphsForDialect with mkdocs enabled.
func TestLazyAdmonitionKeepsItsShape(t *testing.T) {
	const src = "!!! tip \"Title here\"\n    The recommended shape is the v2 API at\n    a decomposed resource. Second sentence here.\n"
	b := []byte(src)
	doc := gm.New().Parser().Parse(text.NewReader(b))
	paras := ParagraphsForDialect(doc, b, true)
	if len(paras) != 1 {
		t.Fatalf("got %d paragraphs, want 1", len(paras))
	}
	p := paras[0]
	if !p.Boundary[0] {
		t.Error("marker line is not a boundary, so reflow may join it into the body")
	}
	if p.ContPrefix != "    " {
		t.Errorf("ContPrefix = %q, want the 4-space body indent", p.ContPrefix)
	}
}

// The same shape under the default (GFM) dialect is ordinary prose: the
// marker boundary logic must not run outside mkdocs.
func TestLazyAdmonitionShapeIsOrdinaryProseUnderGFM(t *testing.T) {
	const src = "!!! tip \"Title here\"\n    The recommended shape is the v2 API at\n    a decomposed resource. Second sentence here.\n"
	b := []byte(src)
	doc := gm.New().Parser().Parse(text.NewReader(b))
	paras := Paragraphs(doc, b)
	if len(paras) != 1 {
		t.Fatalf("got %d paragraphs, want 1", len(paras))
	}
	p := paras[0]
	if p.Boundary[0] {
		t.Error("marker line is a boundary under GFM, want the admonition rule to be mkdocs-only")
	}
	if p.ContPrefix != "" {
		t.Errorf("ContPrefix = %q, want no admonition body indent under GFM", p.ContPrefix)
	}
}

// A reflow wrap cut can end a line at any point in the original paragraph,
// so the admonition-marker verdict must depend only on the "!!!"/"???"
// prefix, never on where the line happens to end. An end-anchored version
// of this predicate let a cut that isolated "!!! ev" on its own line read
// as a marker when the source line was really "!!! ev BX1201" (issue #51):
// the verdict flipped depending on a boundary reflow itself moves. Pinning
// this directly against the regex (rather than only through end-to-end
// reflow) keeps the property visible if the regex is ever touched again.
func TestAdmonitionMarkerVerdictIndependentOfLineEnd(t *testing.T) {
	cases := []struct {
		name  string
		short string // a marker-shaped line
		long  string // the same prefix, with more words appended
	}{
		{"three-bang marker", "!!! ev", "!!! ev BX1201"},
		{"collapsible marker", "??? note", "??? note extra words here"},
		{"expandable collapsible marker", "???+ tip", `???+ tip "Title" trailing words`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			short := admonitionMarkerRE.MatchString(tc.short)
			long := admonitionMarkerRE.MatchString(tc.long)
			if !short {
				t.Errorf("marker-shaped line %q does not match", tc.short)
			}
			if short != long {
				t.Errorf("verdict depends on line end: short=%v (%q), long=%v (%q)", short, tc.short, long, tc.long)
			}
		})
	}
}

func TestParenGuardNarrowing(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		eligible bool // does Paragraphs return a reflow-eligible paragraph?
	}{
		{"paren only, spanning", "A torus (a portal\nyou pass through) here. Second sentence.\n", true},
		{"paren spanning with unrelated bracket elsewhere (#16)", "Control plane (GMC rolls to\nrc.6, [self-hosted] ready). Second sentence here.\n", true},
		// Narrowed here: with no "]:" anywhere, no definition can form, so a
		// wrapped link is safe to reflow.
		{"bracket spanning, no definition shape", "A torus [a portal\nyou pass through] here. Second sentence.\n", true},
		{"bracket spanning into a definition shape", "A torus [a portal\nyou pass through]: /url here.\n", false},
		{"bare def opener", "[0]:\n0\n\"\"0\n", false},
		{"def opener mid-paragraph", "[! [0]:0\n0\n", false},
		{"def title spanning", "[label]: /url (title\ncontinues) here. Second sentence.\n", false},
		{"inline destination spanning", "See [t](/a\nb) here. Second sentence.\n", false},
		{"image destination spanning", "An image ![alt](\n/dest) here. Second sentence.\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.src)
			doc := gm.New().Parser().Parse(text.NewReader(b))
			if got := len(Paragraphs(doc, b)) > 0; got != tc.eligible {
				t.Errorf("eligible = %v, want %v", got, tc.eligible)
			}
		})
	}
}

// TestMaskCodeSpansIsLinkifyAware pins issue #30's replacement for the old
// ordering invariant (issue #28): maskCodeSpans now sources its spans from
// segment.CodeSpans, which parses the joined text through the same
// goldmark configuration (linkify included) the document render uses, so
// a backtick inside a GFM bare URL is destination content to it, never a
// delimiter — exactly as goldmark itself sees it. The historical hazard
// shape is FuzzFormat seed 41e98cb4c9e00729 (docs/design.md, quoted there
// in full): a lone backtick sits inside a bare URL, followed by a real
// code span later in the paragraph. This shape carries no bracket or
// definition hazard of its own, so it exercises maskCodeSpans without
// tripping an unrelated guard.
func TestMaskCodeSpansIsLinkifyAware(t *testing.T) {
	const line = "see http://e.m/` and `code` and http://x.example/` tail"
	lines := []string{line}

	// Not skipped: there is no blind spot left to guard against, so the
	// paragraph reaches masking and reflow eligibility normally.
	src := []byte(line + "\n")
	doc := gm.New().Parser().Parse(text.NewReader(src))
	if got := len(Paragraphs(doc, src)); got != 1 {
		t.Fatalf("Paragraphs = %d, want 1: a backtick inside a bare URL no longer skips the paragraph", got)
	}

	// Masked correctly: both URLs' backticks are destination content, not
	// delimiters, so they stay untouched; only the real code span's
	// interior becomes filler, with its own backtick delimiters intact.
	const want = "see http://e.m/` and `xxxx` and http://x.example/` tail"
	if got := maskCodeSpans(lines)[0]; got != want {
		t.Fatalf("maskCodeSpans(%q)[0] = %q, want %q", line, got, want)
	}
}
