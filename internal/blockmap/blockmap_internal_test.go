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

// TestPrecededByBlankLine checks precededByBlankLine's doc comment: true
// when the raw physical source line immediately before contentStart is
// empty or all spaces/tabs.
func TestPrecededByBlankLine(t *testing.T) {
	cases := []struct {
		name         string
		source       string
		contentStart int
		want         bool
	}{
		{"blank line before", "foo\n\nbar", 5, true},
		{"whitespace-only line before", "foo\n   \nbar", 8, true},
		{"non-blank line before", "foo\nbar", 4, false},
		{"contentStart at start of source: no previous line", "bar", 0, false},
		{"CRLF blank line before", "foo\r\n\r\nbar", 7, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := precededByBlankLine([]byte(tc.source), tc.contentStart); got != tc.want {
				t.Errorf("precededByBlankLine(%q, %d) = %v, want %v", tc.source, tc.contentStart, got, tc.want)
			}
		})
	}
}

// TestPrecededByBareLinkRefDefLine tables bareLinkRefDefLineRE's shape via
// precededByBareLinkRefDefLine: a line consisting solely of
// "[label]:" (optionally indented and/or blockquote-prefixed), nothing
// after the colon.
func TestPrecededByBareLinkRefDefLine(t *testing.T) {
	cases := []struct {
		name         string
		source       string
		contentStart int
		want         bool
	}{
		{"bare link ref def line", "[foo]:\nbar", 7, true},
		{"leading space allowed", "  [foo]:\nbar", 9, true},
		{"blockquote-nested bare def line", ">[foo]:\nbar", 8, true},
		{"destination present disqualifies", "[foo]: /url\nbar", 12, false},
		{"footnote-shaped label still counts (not excluded here)", "[^foo]:\nbar", 8, true},
		{"ordinary prose line does not qualify", "just prose\nbar", 11, false},
		{"contentStart at start of source: no previous line", "bar", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := precededByBareLinkRefDefLine([]byte(tc.source), tc.contentStart); got != tc.want {
				t.Errorf("precededByBareLinkRefDefLine(%q, %d) = %v, want %v", tc.source, tc.contentStart, got, tc.want)
			}
		})
	}
}

// TestIsSelfCompleteLinkRefDef checks isSelfCompleteLinkRefDef's doc
// comment: true when the definition's own recorded opening line, reparsed
// in isolation, still forms a complete definition with nothing left over
// — false when the label/destination/title needed a following line to
// complete.
func TestIsSelfCompleteLinkRefDef(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   bool
	}{
		{
			name:   "self-complete one-liner",
			source: "[foo]: /url\n\nbody\n",
			want:   true,
		},
		{
			name:   "self-complete with title",
			source: "[foo]: /url \"Title\"\n\nbody\n",
			want:   true,
		},
		{
			name:   "label split across a line break needs the next line",
			source: "[\\]\n]:0\n\"\"0\n",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte(tc.source)
			doc := gm.New().Parser().Parse(text.NewReader(source))
			lrd := findFirstLinkRefDef(doc)
			if lrd == nil {
				t.Fatalf("source %q did not parse to a LinkReferenceDefinition at all", tc.source)
			}
			if got := isSelfCompleteLinkRefDef(source, lrd); got != tc.want {
				t.Errorf("isSelfCompleteLinkRefDef(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

// findFirstLinkRefDef returns doc's first LinkReferenceDefinition
// descendant (depth-first), or nil.
func findFirstLinkRefDef(n ast.Node) ast.Node {
	if n.Kind() == ast.KindLinkReferenceDefinition {
		return n
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if found := findFirstLinkRefDef(c); found != nil {
			return found
		}
	}
	return nil
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

// TestHasPossibleLinkRefDefOpener checks hasPossibleLinkRefDefOpener's doc
// comment: an unanchored "[label]:" match anywhere in any line, excluding
// the footnote-shaped "[^label]:" form.
func TestHasPossibleLinkRefDefOpener(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"opener present", []string{"[foo]: bar"}, true},
		{"opener mid line still matches (unanchored)", []string{"prose [foo]: bar more"}, true},
		{"footnote-shaped label excluded", []string{"[^foo]: bar"}, false},
		{"no opener at all", []string{"just prose"}, false},
		{"opener on a later line", []string{"prose", "[foo]:"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPossibleLinkRefDefOpener(tc.lines); got != tc.want {
				t.Errorf("hasPossibleLinkRefDefOpener(%v) = %v, want %v", tc.lines, got, tc.want)
			}
		})
	}
}

// TestHasUnbalancedBracketAndParen checks hasUnclosedDelimiterAcrossLine
// (via its two callers): proper nested-depth tracking across lines, not a
// per-line arithmetic sum — an unmatched close byte earlier in a line
// must not appear to "cancel out" an unrelated open byte later on the
// same line.
func TestHasUnbalancedBracketAndParen(t *testing.T) {
	cases := []struct {
		name        string
		lines       []string
		wantBracket bool
		wantParen   bool
	}{
		{"balanced on one line", []string{"[foo](bar)"}, false, false},
		{"bracket left open at end of paragraph", []string{"[foo"}, true, false},
		{
			// Per the doc comment, this counts as unbalanced even though
			// it is later closed: an open left dangling at the end of any
			// non-final line is itself the hazard (a label/destination
			// spanning the line break), independent of whether a later
			// line happens to close it.
			name:        "bracket left open at end of a non-final line still counts, even though later closed",
			lines:       []string{"[foo", "bar]"},
			wantBracket: true,
			wantParen:   false,
		},
		{"paren left open across lines", []string{"(foo", "bar"}, false, true},
		{
			// Arithmetic sum would read this as balanced (one close, one
			// open cancel out), but the close is stray/unmatched text
			// before the open, which is structurally still open at the
			// line's end — found by FuzzFormat on ")[](  \n)" (see
			// hasUnbalancedParen's doc comment).
			name:        "stray close then open on same line does not cancel arithmetically",
			lines:       []string{")[](", ")"},
			wantBracket: false,
			wantParen:   true,
		},
		{"open bracket alone, never closed", []string{"[", "still open"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUnbalancedBracket(tc.lines); got != tc.wantBracket {
				t.Errorf("hasUnbalancedBracket(%v) = %v, want %v", tc.lines, got, tc.wantBracket)
			}
			if got := hasUnbalancedParen(tc.lines); got != tc.wantParen {
				t.Errorf("hasUnbalancedParen(%v) = %v, want %v", tc.lines, got, tc.wantParen)
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

// TestParenGuardRequiresBracket pins issue #14: the paren arm of build's
// link-hazard guard only fires when a "[" exists somewhere in the
// paragraph — every hazard it guards (inline link destination, definition
// title) needs one, and reflow never creates one. Bracket-free prose with
// a parenthetical spanning a line break must stay reflow-eligible.
func TestParenGuardRequiresBracket(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		eligible bool // does Paragraphs return a reflow-eligible paragraph?
	}{
		{"paren only, spanning", "A torus (a portal\nyou pass through) here. Second sentence.\n", true},
		{"bracket spanning", "A torus [a portal\nyou pass through] here. Second sentence.\n", false},
		{"bare def opener", "[0]:\n0\n\"\"0\n", false},
		{"def opener mid-paragraph", "[! [0]:0\n0\n", false},
		{"def title spanning", "[label]: /url (title\ncontinues) here. Second sentence.\n", false},
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
