package blockmap

import (
	"testing"

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
			// The exemption instead fires when THIS paragraph's own first
			// line is the footnote opener — even directly after another
			// (possibly also caret-led) definition line, the ordinary
			// back-to-back footnote layout.
			name:         "footnote-own paragraph exempt even when preceded by another def line",
			source:       "[^1]: first\n[^2]: second",
			trimmed:      []string{"[^2]: second"},
			contentStart: 12,
			want:         false,
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
			// lines (seed 97329a80dd2cb7d4; see defRunAbove).
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
			if got := inLinkRefDefZone([]byte(tc.source), tc.trimmed, tc.contentStart, defRunAbove([]byte(tc.source))); got != tc.want {
				t.Errorf("inLinkRefDefZone(%q, %v, %d) = %v, want %v", tc.source, tc.trimmed, tc.contentStart, got, tc.want)
			}
		})
	}
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

// TestHasBacktickInBareURL tables hasBacktickInBareURL: a backtick inside
// a linkify-eligible bare URL token (seed 41e98cb4c9e00729), including the
// mid-token spelling linkify also fires on.
func TestHasBacktickInBareURL(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"backtick inside a scheme URL", []string{"see http://e.m/`x here"}, true},
		{"backtick inside a www URL", []string{"see www.e.m/`x here"}, true},
		{"mid-token URL after a stray \"](\"", []string{"[a](https://e.m/p`q rest"}, true},
		{"URL without a backtick", []string{"see http://e.m/x here"}, false},
		{"backtick without a URL", []string{"see `code` here"}, false},
		{"backtick and URL in separate tokens", []string{"see `code` and http://e.m/x"}, false},
		{"no lines", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasBacktickInBareURL(tc.lines); got != tc.want {
				t.Errorf("hasBacktickInBareURL(%v) = %v, want %v", tc.lines, got, tc.want)
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
