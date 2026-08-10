package segment

import (
	"regexp"

	"github.com/jbeda/mdreflow/internal/gm"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
)

// NoBreakSpans returns byte ranges within text that a sentence break must
// never land inside. text is a reflow cluster's joined line — a string
// that exists nowhere in the document itself — parsed standalone through
// gm.NewInline() (docs/design.md, "No-break spans: ask goldmark, not a
// hand grammar"). The *breakable* regions are the segments of plain
// ast.Text nodes whose ancestry between the parse's Paragraph and the
// node is only Emphasis/Strikethrough; every other byte — link syntax,
// code-span delimiters and interiors, autolink URLs, raw HTML, emphasis
// delimiter runs — is no-break, computed as the complement. Constructs
// invisible to this parse (inline math, Hugo shortcodes, MDX {expr},
// footnote references, and the near-tag opener guard) are layered on top
// from the regex scanners kept below.
//
// The reflow pipeline uses this to filter a Segmenter's candidate breaks,
// independent of which Segmenter produced them.
func NoBreakSpans(text string) []Span {
	out := noBreakSpansFromParse(text)
	out = append(out, keptRegexSpans(text)...)
	return out
}

// noBreakSpansFromParse computes NoBreakSpans's ask-goldmark component:
// the complement of the parse's allowed (breakable) Text segments. A
// degenerate parse — anything but exactly one Paragraph block — falls
// back to one span covering the whole text, per docs/design.md's
// "Degenerate parses fall back to no reflow of the cluster."
func noBreakSpansFromParse(text string) []Span {
	para, ok := parseSingleParagraph(text)
	if !ok {
		if len(text) == 0 {
			return nil
		}
		return []Span{{Start: 0, End: len(text)}}
	}
	return complement(allowedSpans(para), len(text))
}

// CodeSpans reports text's real (matched-open-and-close) code span
// ranges — used by package reflow to tell whether a hard-break boundary
// or cluster join falls inside a genuine code span, and by blockmap's
// guard-arm masking. text may contain "\n" (a paragraph's lines joined
// that way, matching how goldmark's inline parser sees a paragraph).
//
// Its answers come from CodeSpan nodes of the same gm.NewInline() parse
// NoBreakSpans uses, which is what retires the old scanner's linkify
// blind spot: a backtick inside a bare linkify URL is destination
// content, not a delimiter, to both.
//
// A degenerate parse returns nil, not a whole-text span: unlike
// NoBreakSpans, CodeSpans has no "protect everything" fallback direction
// to take — nil is unprotected, and the render backstop bounds the
// consequences (docs/design.md).
func CodeSpans(text string) []Span {
	para, ok := parseSingleParagraph(text)
	if !ok {
		return nil
	}
	return codeSpansFromParse(text, para)
}

// parseSingleParagraph parses text with gm.NewInline() and reports
// whether the result is exactly one Paragraph block, returning it if so.
// The paragraph parser declines an all-indented line, producing an empty
// document (nil FirstChild) — guarded against explicitly, not merely
// assumed absent.
func parseSingleParagraph(text string) (ast.Node, bool) {
	md := gm.NewInline()
	reader := gmtext.NewReader([]byte(text))
	doc := md.Parser().Parse(reader)
	first := doc.FirstChild()
	if first == nil || first.NextSibling() != nil || first.Kind() != ast.KindParagraph {
		return nil, false
	}
	return first, true
}

// allowedSpans returns the byte segments of para's plain-text content:
// every *ast.Text node reachable from para through a chain of only
// Emphasis/Strikethrough nodes. Anything reached through any other node
// kind (Link, Image, AutoLink, CodeSpan, RawHTML, ...) is deliberately
// not descended into — its bytes are protected regardless of what it
// might itself contain, matching docs/design.md's "any node kind the
// walk doesn't recognize defaults to protected."
func allowedSpans(para ast.Node) []Span {
	var out []Span
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c.Kind() {
			case ast.KindText:
				t := c.(*ast.Text)
				out = append(out, Span{Start: t.Segment.Start, End: t.Segment.Stop})
			case ast.KindEmphasis, extast.KindStrikethrough:
				walk(c)
			}
		}
	}
	walk(para)
	return out
}

// complement returns the byte ranges of [0,textLen) not covered by
// allowed, which is assumed sorted and non-overlapping — true of
// allowedSpans's output, since it visits Text nodes in document order and
// two Text segments can never overlap the same source bytes.
func complement(allowed []Span, textLen int) []Span {
	var out []Span
	pos := 0
	for _, a := range allowed {
		if a.Start > pos {
			out = append(out, Span{Start: pos, End: a.Start})
		}
		if a.End > pos {
			pos = a.End
		}
	}
	if pos < textLen {
		out = append(out, Span{Start: pos, End: textLen})
	}
	return out
}

// codeSpansFromParse walks para for *ast.CodeSpan nodes and, for each,
// returns the byte range from its opening backtick run through its
// closing backtick run — the same "delimiter through delimiter, inclusive"
// extent the legacy codeSpans scanner reported.
//
// A CodeSpan's children are the Text segments of its interior content (one
// per source line it spans); the node itself carries no source extent for
// its delimiters. The opening run is found by scanning left from the first
// child's Segment.Start and the closing run by scanning right from the last
// child's Segment.Stop — in each case skipping CommonMark's stripped
// padding first, then walking over the backtick run. The content segment
// is NOT adjacent to the delimiter run: a code span whose interior begins
// and ends with a space has one space stripped from each side (a
// backtick-space-backslash-space-backtick span renders just the backslash,
// whose content segment starts at the backslash, past the stripped space),
// and a span crossing a line has its
// line ending between the content and the closing run; both are whitespace,
// and only whitespace ever separates content from delimiter. Missing that
// padding sizes the span down to the bare interior and stops it protecting
// the content — an idempotency break found by FuzzFormat on "` \\\n`",
// whose interior backslash was then mistaken for a hard break. A CodeSpan
// with no children (an empty backtick-pair-with-nothing span) is skipped:
// an empty interior contains no join boundary and no maskable bytes, so
// omitting it is behaviorally inert for both consumers.
func codeSpansFromParse(text string, para ast.Node) []Span {
	var out []Span
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if c.Kind() == ast.KindCodeSpan {
				if first, last := c.FirstChild(), c.LastChild(); first != nil && last != nil {
					ft, ok1 := first.(*ast.Text)
					lt, ok2 := last.(*ast.Text)
					if ok1 && ok2 {
						start := delimRunStart(text, ft.Segment.Start)
						end := delimRunEnd(text, lt.Segment.Stop)
						out = append(out, Span{Start: start, End: end})
					}
				}
			}
			walk(c)
		}
	}
	walk(para)
	return out
}

// delimRunStart returns the start of the code-span opening backtick run
// whose interior content begins at contentStart, skipping any stripped
// whitespace padding between the run and the content before walking back
// over the backticks. See codeSpansFromParse on why only whitespace ever
// separates the two.
func delimRunStart(text string, contentStart int) int {
	i := contentStart
	for i > 0 && isCodeSpanPad(text[i-1]) {
		i--
	}
	for i > 0 && text[i-1] == '`' {
		i--
	}
	return i
}

// delimRunEnd returns the end of the code-span closing backtick run whose
// interior content ends at contentStop, the mirror of delimRunStart.
func delimRunEnd(text string, contentStop int) int {
	i := contentStop
	for i < len(text) && isCodeSpanPad(text[i]) {
		i++
	}
	for i < len(text) && text[i] == '`' {
		i++
	}
	return i
}

// isCodeSpanPad reports whether c is whitespace that can sit between a code
// span's backtick delimiter and its interior content: CommonMark's single
// stripped space, or a line ending the span crosses (the "\n" joiners
// CodeSpans's callers pass, and the CRs a raw source line may carry).
func isCodeSpanPad(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// keptRegexSpans finds the inline constructs invisible to gm.NewInline()'s
// parse: inline math, Hugo shortcodes, MDX/JSX {expr} spans, footnote
// references (the profile does not enable the footnote extension), and
// the near-tag opener guard — see docs/design.md's "What stays regex."
// Unlike legacyNoBreakSpans's otherSpans, this omits autolinkRE (the ask-
// goldmark parse sees AutoLink nodes directly) and htmlTagSpans's precise
// tag grammar (RawHTML nodes cover well-formed tags directly; only the
// opener-to-next-">" idempotency stabilizer below has no AST equivalent).
func keptRegexSpans(text string) []Span {
	var out []Span
	for _, re := range [...]*regexp.Regexp{
		footnoteRefRE,
		inlineMathRE,
		hugoShortcodeRE,
		curlyExprRE,
	} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			out = append(out, Span{Start: m[0], End: m[1]})
		}
	}
	out = append(out, htmlTagOpenerSpans(text)...)
	return out
}

// footnoteRefRE matches a footnote reference like "[^1]". The ask-goldmark
// parse has no footnote extension registered, so these are plain bracket
// text to it.
var footnoteRefRE = regexp.MustCompile(`\[\^[^\]\n]+\]`)

// inlineMathRE matches single-dollar inline math, "$...$". It does not
// attempt to distinguish from a "$$" display-math delimiter; display
// math is block-level and handled by the skip-list, not this scanner.
var inlineMathRE = regexp.MustCompile(`\$[^$\n]+\$`)

// hugoShortcodeRE matches an inline Hugo shortcode, "{{< ... >}}" or
// "{{% ... %}}".
var hugoShortcodeRE = regexp.MustCompile(`\{\{[<%].*?[%>]\}\}`)

// curlyExprRE matches an inline MDX/JSX "{expr}" span. It also matches
// (harmlessly, redundantly) the interior of a Hugo shortcode span, since
// both use curly braces; overlapping no-break spans are fine, a break is
// blocked if it lands in any of them.
var curlyExprRE = regexp.MustCompile(`\{[^{}\n]+\}`)

// htmlTagOpenerRE matches the start of an inline HTML/JSX tag: "<",
// optionally "/", then a letter — CommonMark's own tag-name-start rule,
// deliberately not requiring the rest of the tag to be well-formed (see
// htmlTagOpenerSpans's doc comment on why).
var htmlTagOpenerRE = regexp.MustCompile(`</?[A-Za-z]`)

// htmlTagOpenerSpans returns, for every same-line inline-HTML tag opener
// ("<" or "</" followed by a letter), the byte range from that opener
// through the next ">" on the same line (or through the line's end, if
// none) — regardless of whether that stretch actually satisfies
// CommonMark's precise tag grammar, and regardless of whether the
// ask-goldmark parse recognized it as a RawHTML node.
//
// This broader, opener-to-next-">" span exists specifically to keep a
// *break point* out of a "maybe a tag" region even when the parse
// declines to call it one, which happens whenever the tag grammar
// doesn't quite validate (an attribute with no value, say). Without it,
// NoBreakSpans lets a break land in the middle of such a stretch — which
// can flip the very question of whether it *is* a tag from one reflow
// pass to the next: found by FuzzFormat on `0y70A><A0 A= >00` (ModeWrap,
// width 14), where a width-based break landed between "A=" and " >00",
// starting a fresh line with ">00" — CommonMark reads a bare leading ">"
// as a blockquote marker, so escapeBlockInterrupt backslash-escaped it.
// That backslash is now part of the source on the *next* format pass,
// shifting where the same width computation lands its break — this time
// nowhere at all, since the extra byte changes the fit — so the second
// pass produces different output than the first: an idempotency failure,
// which (unlike a render-preservation gap) design.md does not allow any
// documented exception for. An ambiguous near-tag region needs one
// stable, generously wide answer to "keep out of this," not narrower
// ones that can each land differently across passes.
// No AST equivalent is possible: this is an idempotency stabilizer for a
// region the parse *declines* to call a tag, which is exactly what the
// parse cannot be asked about.
func htmlTagOpenerSpans(text string) []Span {
	var out []Span
	lineStart := 0
	for {
		lineEnd := len(text)
		if nl := indexByteFrom(text, lineStart, '\n'); nl >= 0 {
			lineEnd = nl
		}
		line := text[lineStart:lineEnd]
		for _, loc := range htmlTagOpenerRE.FindAllStringIndex(line, -1) {
			start := lineStart + loc[0]
			end := lineEnd
			if gt := indexByteFrom(text, start, '>'); gt >= 0 && gt < lineEnd {
				end = gt + 1
			}
			out = append(out, Span{Start: start, End: end})
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return out
}

// indexByteFrom returns the index of the first occurrence of b in
// text[from:], as an absolute offset into text, or -1 if not found.
func indexByteFrom(text string, from int, b byte) int {
	for i := from; i < len(text); i++ {
		if text[i] == b {
			return i
		}
	}
	return -1
}
