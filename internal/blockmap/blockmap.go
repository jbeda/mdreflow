// Package blockmap derives the set of reflow-eligible paragraphs from a
// goldmark AST, anywhere in the document: top-level, and nested inside any
// depth of List/ListItem/Blockquote nesting.
//
// For each eligible Paragraph, blockmap also derives:
//
//   - a per-line Boundary classification (see docs/m0-spike-findings.md's
//     Recommendation, rule 2): a source line matching a dialect marker
//     regex is immovable and must be emitted byte-for-byte, never joined
//     into a reflowed prose run;
//   - a ContPrefix: the exact bytes package reflow must prepend to every
//     output line after the paragraph's first (list-item padding and/or
//     blockquote "> " markers, composed for arbitrary nesting).
//
// Paragraphs whose entire text matches a dialect marker (front matter
// fences, math blocks, MDX block constructs — see dialect.go) are excluded
// from the returned slice entirely; since package reflow only touches
// paragraphs it is given, excluding one is exactly equivalent to a
// byte-for-byte skip, no separate mechanism needed.
package blockmap

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark/ast"
	gfmast "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
)

// Paragraph describes one reflow-eligible paragraph, wherever it is nested.
// Despite the name, Node may be an *ast.TextBlock rather than an
// *ast.Paragraph: goldmark replaces a tight list item's Paragraph child
// with a TextBlock (see (*listParser).Close in goldmark's parser package),
// so list items in a tight list — the common case — are TextBlock nodes,
// not Paragraph nodes. Both expose Lines(), which is all this package and
// package reflow need.
type Paragraph struct {
	// Node is the goldmark AST node for the paragraph or (tight-list)
	// text block.
	Node ast.Node
	// Start and End are the byte offsets, into the source, spanned by the
	// paragraph's raw source lines (End is exclusive). For a nested
	// paragraph this range includes the container-prefix bytes ("> ",
	// list-item padding, ...) of every line after the first: those bytes
	// sit contiguously between one line's content end and the next line's
	// content start in the source, so they fall inside [Start, End)
	// automatically even though Node.Lines()'s segments themselves exclude
	// them.
	Start, End int
	// ContPrefix is the exact byte string package reflow must write before
	// every output line except the paragraph's first (which needs no
	// prefix: the source bytes up to Start, copied byte-for-byte by the
	// caller, already contain the original first-line prefix).
	ContPrefix string
	// Boundary has one entry per Node.Lines() index. Boundary[i] == true
	// means source line i matched a dialect marker regex (see dialect.go)
	// and must be emitted byte-for-byte; package reflow never joins across
	// it.
	Boundary []bool
}

// Paragraphs walks doc (at any depth) and returns every reflow-eligible
// Paragraph node, in source order, skipping any paragraph a dialect
// whole-node rule matches.
//
// doc must be the *ast.Document returned by parsing source with the
// mdreflow-configured goldmark instance (see package gm); source must be
// the exact bytes that were parsed, since the returned ranges index into it.
func Paragraphs(doc ast.Node, source []byte) []Paragraph {
	return ParagraphsForDialect(doc, source, false)
}

// ParagraphsForDialect is Paragraphs with dialect-specific block
// recognition enabled. mkdocs additionally treats a MkDocs admonition body
// as prose; see admonitionBody for why that cannot be the default.
func ParagraphsForDialect(doc ast.Node, source []byte, mkdocs bool) []Paragraph {
	var out []Paragraph
	fmEnd := frontMatterEnd(source)
	collect(doc, source, false, 0, fmEnd, defRunAbove(source), &out, mkdocs)
	return out
}

// defRunAbove reports, per physical line (keyed by the line's start byte
// offset), whether a definition-shaped line — the same shapes
// inLinkRefDefZone checks on the immediately preceding line — occurs
// anywhere ABOVE that line within its contiguous run of non-blank lines.
// A blank line resets the run.
//
// This exists because a definition's reach downward is not limited to one
// line: its title alone may span arbitrarily many lines, so a paragraph
// can sit several lines below the "[label]:" opener yet still be the next
// text the definition's own scan touches — reflowing that paragraph moves
// the title's closing boundary and re-carves every line in between on the
// next parse. Found by FuzzFormat on
// "[0]:\n1\n\"\n\"[0]:0\n[1]:0\n\"20\n0\n00\n\"" (seed 97329a80dd2cb7d4):
// the only reflow-eligible paragraph was the two-line tail of a title
// spanning three lines below its def, one line beyond the neighbor check.
// The transitive rule is verdict-stable by construction: every paragraph
// inside a def-containing run is in-zone, so nothing in such a run ever
// reflows, so the run's line layout — and with it every verdict keyed on
// it — cannot change between passes. Computed in one top-down pass so the
// zone check stays O(1) per paragraph.
func defRunAbove(source []byte) map[int]bool {
	m := make(map[int]bool)
	seen := false
	for ls := 0; ls < len(source); {
		end := ls
		for end < len(source) && source[end] != '\n' {
			end++
		}
		line := bytes.TrimRight(source[ls:end], "\r")
		m[ls] = seen
		if len(bytes.Trim(line, " \t")) == 0 {
			seen = false
		} else if defLineOpenerRE.Match(line) || bareCaretOpenerRE.Match(line) || orphanDefCloserRE.Match(line) {
			seen = true
		}
		ls = end + 1
	}
	return m
}

// maxContainerDepth caps how many List/Blockquote container levels deep a
// paragraph may be nested and still get continuation-indented reflow (see
// continuationPrefix). Beyond this depth, the paragraph is passed through
// byte-for-byte instead — see collect's doc comment for why.
const maxContainerDepth = 2

// collect walks n's children (block-level only; a Paragraph's own inline
// children are never visited, since goldmark never nests a Paragraph inside
// another Paragraph's inline tree). inBlockquote is true if n itself, or
// any ancestor already visited, is a *ast.Blockquote. fmEnd is the
// document's front-matter end offset (frontMatterEnd(source), or -1 if
// none) — see build's use of it.
//
// depth counts List and Blockquote ancestors (each entered increments it
// by one; ListItem does not, since a List/ListItem pair is one level of
// nesting). continuationPrefix's column-width-preserving transform is
// verified against every combination this package's own fixtures cover —
// up to two container levels, alternating or repeated — but a fuzz find
// showed it breaking structure for deeper mixed nesting (three-plus levels
// alternating List and Blockquote, e.g. blockquote > list > blockquote):
// re-deriving a correct continuation prefix there depends on exactly how
// goldmark's own nested Continue() checks interact across container-kind
// boundaries, which turned out not to reduce to a simple per-byte
// transform of the first line's prefix the way two-level nesting does.
// Rather than risk corrupting structure at a depth this package cannot
// currently verify itself, paragraphs beyond maxContainerDepth are passed
// through untouched — no reflow is always render-preserving by
// construction. Found by FuzzFormat on "000000000000000000\n>* >! 0"
// (blockquote > list > blockquote, three levels).
func collect(n ast.Node, source []byte, inBlockquote bool, depth int, fmEnd int, zoneAbove map[int]bool, out *[]Paragraph, mkdocs bool) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if cb, ok := c.(*ast.CodeBlock); ok {
			*out = append(*out, admonitionBodies(cb, source, mkdocs)...)
			continue
		}
		switch c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			if depth > maxContainerDepth {
				continue // pass through byte-for-byte; see collect's doc comment
			}
			// Only when directly adjacent (no blank line): a table
			// properly terminated by a blank line before the next
			// paragraph is never ambiguous (see build's doc comment on
			// precededByTable for the instability this guards against,
			// which is specific to a table lazily-adjacent to what
			// follows it) — found necessary by the existing
			// testdata/table.md fixture, whose closing paragraph is
			// separated from the table by a blank line and must still
			// reflow normally.
			precededByTable := false
			if prev := c.PreviousSibling(); prev != nil && prev.Kind() == gfmast.KindTable && !c.HasBlankPreviousLines() {
				precededByTable = true
			}
			// Link-reference-definition adjacency no longer needs an AST
			// sibling check here: the whole zone is judged bluntly, by
			// shape, from build's own raw-byte scan (see
			// inLinkRefDefZone) — design.md's "The link-reference-
			// definition zone: skip bluntly, by shape".
			if pp, skip := build(c, source, inBlockquote, fmEnd, precededByTable, zoneAbove); !skip {
				*out = append(*out, pp)
			}
			continue
		}
		childInBQ := inBlockquote
		childDepth := depth
		switch c.(type) {
		case *ast.Blockquote:
			childInBQ = true
			childDepth++
		case *ast.List:
			childDepth++
		}
		collect(c, source, childInBQ, childDepth, fmEnd, zoneAbove, out, mkdocs)
	}
}

// build derives a Paragraph from p (an *ast.Paragraph or *ast.TextBlock),
// or reports skip == true if a whole-node dialect rule matches p's text.
// fmEnd is the document's front-matter end offset (frontMatterEnd(source),
// or -1 if source has no front matter) — see its use below. precededByTable
// is true when p's immediately preceding sibling in the AST is a GFM
// *ast.Table — see its use below for why.
func build(p ast.Node, source []byte, inBlockquote bool, fmEnd int, precededByTable bool, zoneAbove map[int]bool) (pp Paragraph, skip bool) {
	lines := p.Lines()
	n := lines.Len()
	if n == 0 {
		return Paragraph{}, true // empty paragraph; nothing to emit specially
	}

	trimmed := make([]string, n)
	allBlank := true
	for i := 0; i < n; i++ {
		s := lines.At(i)
		trimmed[i] = strings.TrimSpace(string(s.Value(source)))
		if trimmed[i] != "" {
			allBlank = false
		}
	}
	if allBlank {
		// Fuzz-found degenerate case: goldmark's own blank-line test
		// (space/tab only) is narrower than Go's Unicode-aware
		// strings.TrimSpace, so a "paragraph" whose only content is
		// something like a lone vertical-tab byte is not blank enough to
		// stop goldmark from forming a block, yet trims to nothing here.
		// There is no CommonMark syntax for an "empty but present"
		// paragraph, so joining/re-splitting it as prose (which would
		// produce zero bytes of output) would delete it outright on
		// reparse. Passing it through byte-for-byte — as if no dialect
		// rule matched, since none of them is really about this — is the
		// only content-preserving choice.
		return Paragraph{}, true
	}
	if !allBlank && hasEmptyLine(trimmed) {
		// Fuzz-found idempotency hazard, related to (but distinct from)
		// the allBlank case above: a *single* line trimming to empty
		// inside an otherwise non-blank paragraph should never happen for
		// legitimate content — goldmark's own blank-line test (space/tab
		// only) would already have ended the paragraph there — so when it
		// does happen, it is always some kind of parser artifact (the
		// same family as allBlank's control-character case). Package
		// reflow's join logic already tolerates an empty line by dropping
		// it (see joinClusterLines), but that alone is not enough to
		// guarantee byte-for-byte reproduction on a second pass for every
		// shape a parser artifact can take. Skipping the whole paragraph
		// is the safe, general answer once again.
		return Paragraph{}, true
	}
	if wholeNodeSkip(trimmed) {
		return Paragraph{}, true
	}
	if fmEnd >= 0 && lines.At(0).Start < fmEnd {
		// Front matter's interior lines: excluded from reflow by a pure
		// byte-range test against blockmap's own front-matter pre-scan
		// (frontMatterEnd), not by any goldmark parser hook — see
		// frontmatter.go. Whatever node shape goldmark's stock block
		// parsing happened to make of these lines (Paragraph, a List's
		// TextBlock, ...) is irrelevant: none of them can straddle the
		// closing delimiter line, so this paragraph's own first line
		// starting before fmEnd is sufficient to know it is entirely
		// contained in the front-matter block.
		return Paragraph{}, true
	}
	start0 := lines.At(0).Start
	end := lines.At(n - 1).Stop
	if hasHiddenLineGap(source, lines) {
		// Byte-loss safety net, found by FuzzFormat/944e87ed958d1511 on
		// "[2\nb\n]:0\n\"\"[8]:00\n<A1aA0>": goldmark's link-reference-
		// definition parser can absorb a titleless definition's would-be
		// title (here the bare "\"\"") and chain straight into a second
		// definition ("[8]:00") on the very same source line, leaving a
		// *sibling* LinkReferenceDefinition node whose raw bytes sit
		// entirely between two of THIS paragraph's Lines() segments —
		// physically inside [start0, end) but never a member of any
		// segment. build's Start/End (this paragraph's doc comment) and
		// package reflow's writeParagraph both only ever look at
		// Node.Lines(); neither has any notion of a gap holding real
		// content, so those bytes are silently dropped: the caller writes
		// source[cursor:p.Start] before this paragraph and jumps cursor to
		// p.End after it, and writeParagraph itself walks lines.At(i) only.
		// A legitimate multi-line paragraph's inter-line gap is always
		// just that line's terminator plus container-prefix padding
		// (blockquote '>' markers, list-item indentation — see
		// continuationPrefix's doc comment) — never brackets, quotes, or
		// digits. Any gap containing anything else, or more than one line
		// terminator (an entire physical line skipped over), means some
		// other node's content is hiding in this paragraph's declared
		// span, and reflowing (or even just splicing straight through) is
		// unsafe. The safe general answer, same as every other check in
		// this function: skip the whole paragraph, byte-for-byte.
		return Paragraph{}, true
	}
	if hasControlByte(source[start0:end]) {
		// design.md, "Control-character paragraphs pass through": a C0
		// control byte other than tab/newline/CR inside a paragraph's raw
		// source range is never produced by a text editor — these show up
		// in fuzz inputs, exactly in the grammar corners (indentation
		// width, whitespace-class membership) that differ between parser
		// implementations. Reflowing around them buys nothing for real
		// documents and costs a long tail of corner-case hardening; '\r'
		// stays allowed (CRLF line endings and bare-CR paragraphs keep
		// reflowing, since the '\r' of a CRLF pair is line-ending
		// machinery, not paragraph interior).
		return Paragraph{}, true
	}
	if inLinkRefDefZone(source, trimmed, start0, zoneAbove) {
		// design.md, "The link-reference-definition zone: skip bluntly, by
		// shape": a link reference definition renders nothing (it is URL
		// metadata) and its grammar is the least reflow-compatible
		// construct in CommonMark — label, destination, and title may each
		// span onto following lines, a titleless one-liner can absorb a
		// title from the paragraph after it, and goldmark reorders
		// definition nodes relative to paragraph siblings. A first
		// implementation grew six interlocking adjacency guards (self-
		// completeness reparses via isSelfCompleteLinkRefDef, registry-diff
		// title-absorption checks via lrdReachesInto/lrdTitleless/
		// registeredRefs, a raw-byte title-opener guard via
		// startsWithTitleOpenerUnderLRDShapedLine, a bare-opener guard via
		// precededByBareLinkRefDefLine, and a same-paragraph opener scan via
		// hasPossibleLinkRefDefOpener) and fuzzing kept finding a seventh
		// shape. The lesson: precision here buys reflow of prose that is
		// rare, ambiguity-laden, and worthless to reflow next to invisible
		// metadata. inLinkRefDefZone replaces all of that with one blunt,
		// shape-based predicate — see its own doc comment.
		return Paragraph{}, true
	}
	if hasBacktickInBareURL(trimmed) {
		// A backtick inside a GFM-linkify-eligible bare URL is not a code
		// span delimiter to goldmark — linkify's URL parser consumes it
		// into the link destination before the code-span parser can see
		// it — so every backtick after it pairs one delimiter out of step
		// with what this package's own scanner (segment.CodeSpans, which
		// does not model linkify) computes. The protected no-break spans
		// then cover the wrong bytes and a break can land *inside* a real
		// code span, where a newline is whitespace: found by FuzzFormat on
		// "http://e.m/` ``e`\tg `" (seed 41e98cb4c9e00729, minimized from
		// a 4 KB corpus-derived input), whose real span content "\tg "
		// became " g " once the tab was replaced by the break, which
		// CommonMark then strips at both edges to "g" — a rendered content
		// change.
		//
		// Skipping the paragraph rather than teaching segment.CodeSpans to
		// model linkify is deliberate, and not the usual bluntness trade:
		// mirroring linkify's grammar (scheme and "www." forms, email
		// forms, and its trailing-punctuation trimming rules) is exactly
		// the kind of hand-mirrored grammar this codebase has repeatedly
		// lost to implementation quirks (see isCompleteLinkRefDefLine's
		// history), and here a wrong mirror would misjudge the no-break
		// spans of the very many *ordinary* documents that contain URLs.
		// The skip costs only paragraphs with a backtick inside a bare
		// URL, which is close to nonexistent in real prose.
		return Paragraph{}, true
	}
	masked := maskCodeSpans(trimmed)
	if (hasUnbalancedBracket(masked) && couldFormLinkRefDef(masked)) ||
		hasUnclosedDestParen(masked) ||
		hasUnclosedAngleDestOpener(masked) {
		// Fuzz-found content-loss hazard family, more severe than (and
		// broader than) reflow.isLinkRefDefOpener's per-output-line
		// defense: a CommonMark link reference definition's label,
		// destination, and title can each independently span a soft line
		// break (a label may run up to 999 characters, newlines
		// included; a destination or title left incomplete on one line
		// can complete using a later one), so a "[label]:" opener
		// reachable *anywhere* in a paragraph — not just alone on its
		// own final output line, which is all a per-line check can see —
		// risks quietly consuming everything up to and including
		// whatever later line happens to complete it, once reflow's own
		// line-joining or sentence-splitting changes which lines sit
		// next to which. Confirmed across several distinct shapes, not
		// assumed to generalize from just one:
		//
		//   - "[! [0]:0" (an unclosed "[" on one line, closed on a
		//     later one) swallowed the *entire document* into an
		//     invisible definition once split — escaping the later
		//     line's "[" doesn't help, since an escaped "[" is still
		//     valid, literal content *inside* an already-open label.
		//   - "[0]:\n0\n\"\"0" balances to net-zero open brackets over
		//     the whole paragraph (so a naive final-balance check
		//     misses it) yet still forms the same kind of accidental
		//     definition.
		//   - "[0]:\n0" — a label+colon with *nothing* after it on its
		//     own line — still consumes a destination reachable only
		//     from the very next line, confirmed directly against
		//     goldmark, contradicting the "nothing follows this line,
		//     so it's safe" assumption reflow.isLinkRefDefOpener's
		//     same-line-only check relies on.
		//
		// Correctly placing reflow breaks around this would require
		// tracking link-reference-definition parse state across the
		// whole paragraph before any break is chosen — a different,
		// larger kind of check than the per-cluster no-break-span
		// filtering (segment.NoBreakSpans) already does. Passing the
		// whole paragraph through untouched is the safe, general answer
		// instead: no reflow can never turn inert text into a
		// definition. The cost is not reflowing some paragraphs that
		// were always going to be safe (most "[...]:"-shaped prose is
		// not actually a definition attempt at all), which is an
		// acceptable trade for correctness on a construct this
		// fine-grained to get right per-line.
		return Paragraph{}, true
	}
	if looksLikeUnterminatedTag(trimmed[0]) {
		// The one construct m0-spike-findings.md documents as unrecoverable
		// from the AST alone: an MDX/JSX (or plain HTML) opening tag whose
		// closing '>' is not on the same line goldmark misparses (its
		// closing '>' alone on a later line reads as an empty blockquote,
		// per the spike). design.md and the M2 task both call this out as
		// a documented, deliberately out-of-scope limitation — no raw
		// pre-parse fence is to be built for it. But *reflowing* such a
		// paragraph is actively dangerous, not just imperfect: goldmark
		// treats a paragraph's multi-line inline HTML tag as ordinary
		// inline content (soft breaks count as whitespace inside the tag
		// grammar), so it can render correctly as-is; joining it down to
		// one physical line can turn it into a *complete* tag alone on a
		// line, which CommonMark's HTML-block condition 7 recognizes as a
		// block opener instead — found by FuzzFormat on "<A\nA>", where
		// reflow's own line-joining (not any dialect construct) created
		// the corruption. Skipping the whole paragraph is a safe,
		// AST-only (not a raw source pre-pass) mitigation: no reflow can
		// never corrupt anything, at the cost of not reflowing prose that
		// happens to open with what looks like an unterminated tag.
		return Paragraph{}, true
	}
	if precededByTable {
		// Fuzz-found idempotency hazard: whether a GFM table forms at all
		// for a given header+delimiter-row pair turns out to be sensitive
		// to the *content shape* of whatever paragraph follows the table,
		// not just the table's own lines — confirmed directly, not
		// derived from any spec reading: "0\n\t|-\n--\n0" (a would-be
		// one-cell table, then a separate two-line paragraph "--"/"0")
		// keeps its table on the first pass, but reflowing the paragraph
		// down to one line ("-- 0") makes the *same* header+delimiter
		// pair fail to form a table at all on the very next reparse.
		// Several follow-up fuzz finds after the first fix attempt (a
		// raw-byte backward scan for a delimiter-row-shaped line) showed
		// that approach could not be made both correct and stable no
		// matter how it was tuned: it produced false positives crossing
		// into unrelated preceding content (an ordinary paragraph or
		// list item that merely *looked* delimiter-row-shaped, e.g.
		// "-|\n0\n* 0\n0" and "\n--\n0\n#\n0\n0") and false negatives on
		// genuine pipe-less delimiter rows depending on details
		// (indentation, data row count) the byte-level heuristic had no
		// reliable way to reproduce ("0\n\t-\n0\n0\n="). The robust fix is
		// to stop guessing from raw bytes and ask the AST directly: this
		// paragraph's own immediately preceding sibling, as goldmark
		// itself already parsed it, either is a real *ast.Table or it
		// isn't. Since blockmap has no way to predict whether that
		// table's own recognition is stable across a reparse of *this*
		// paragraph's own reflowed output, the safe general answer is the
		// same one used throughout this file: skip reflowing any
		// paragraph directly after a real table, so its own shape never
		// changes at all.
		return Paragraph{}, true
	}

	boundary := make([]bool, n)
	for i, t := range trimmed {
		boundary[i] = isBoundaryLine(t, inBlockquote)
	}

	contPrefix := continuationPrefix(source, start0)
	if footnoteDefFirstLineRE.MatchString(trimmed[0]) {
		// design.md: "Reflowed footnote-body continuation lines are
		// emitted with a 4-space indent." Renderers disagree about whether
		// an unindented lazy-continuation line belongs to the footnote
		// (GitHub's documented convention is to indent); the indented
		// spelling is the one they all keep inside the footnote, and to
		// mdreflow's own parser it is an ordinary paragraph continuation
		// (indented code cannot interrupt a paragraph), so render
		// preservation is unaffected. The four spaces are appended to
		// whatever container prefix already applies (list/blockquote
		// nesting), not a replacement for it.
		contPrefix += "    "
	}

	return Paragraph{
		Node:       p,
		Start:      start0,
		End:        end,
		ContPrefix: contPrefix,
		Boundary:   boundary,
	}, false
}

// hasControlByte reports whether b contains a C0 control byte other than
// tab, newline, or carriage return. See build's call site and design.md's
// "Control-character paragraphs pass through" for why this triggers a
// whole-paragraph skip.
func hasControlByte(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return true
		}
	}
	return false
}

// hasHiddenLineGap reports whether lines (a paragraph or text block's
// Node.Lines(), already known non-empty) has a gap between two consecutive
// segments that is not fully explained by an ordinary line terminator plus
// container-prefix padding — see its call site in build for why any other
// gap content means bytes are about to be silently dropped.
func hasHiddenLineGap(source []byte, lines *gmtext.Segments) bool {
	n := lines.Len()
	for i := 1; i < n; i++ {
		gap := source[lines.At(i-1).Stop:lines.At(i).Start]
		newlines := 0
		for _, b := range gap {
			switch b {
			case ' ', '\t', '>', '\r':
				// Ordinary container-prefix padding or CRLF's '\r' — see
				// continuationPrefix's doc comment for the allowed
				// per-line prefix shapes.
			case '\n':
				newlines++
			default:
				return true
			}
		}
		if newlines > 1 {
			// More than one line terminator in the gap means an entire
			// physical source line was skipped over without becoming part
			// of this node's Lines() at all.
			return true
		}
	}
	return false
}

// nonCaretLabelBody is the label-content sub-pattern shared by the
// def-shape regexes below: a run of characters excluding
// literal brackets, except that a backslash-escaped bracket ("\[" or "\]")
// is treated as literal label content rather than a boundary — matching
// CommonMark's own backslash-escape handling closely enough to recognize a
// label that spans a raw line boundary via an escaped closing bracket
// embedded in what precedes a paragraph (needed for the "spans the
// boundary" case in inLinkRefDefZone's doc comment; found necessary by
// FuzzFormat/issue#11's "[\]\n]:0" shape, which a plain "no brackets at
// all" class missed). It excludes a leading '^' (a footnote label —
// design.md's "Footnote definitions are exempt and keep reflowing"): the
// caret exemption applies uniformly to every zone check, contains and
// neighbor alike, because verdict stability demands it (see
// defLineOpenerRE's doc comment).
const nonCaretLabelBody = `(?:\\.|[^\^\[\]])(?:\\.|[^\[\]])*`

// nonFootnoteCaretLabelAlt matches the caret-led labels that are NOT
// footnotes to goldmark and so must count as ordinary definition labels
// in the def-shape regexes below: goldmark's footnote extension requires
// at least one non-space label character after the "^" ("[^x]:" and
// "[^^]:" are footnotes; "[^]:" and "[^ ]:" are plain definitions
// labeled "^"/"^ ", confirmed directly). Treating those as
// footnote-shaped exempted from the zone exactly the line goldmark
// treats as a definition: found by FuzzFormat on
// "[0]:\n1\n\"\n\"[0]:0\n\"1\"[^]:0\n\"0\n00\n\"" (seed
// 6042b560f6c7dcd2), where joining the paragraph after "[^]:0" completed
// that definition and turned the paragraph's prose into its title — a
// render corruption, not just an idempotency flip.
const nonFootnoteCaretLabelAlt = `\^(?:[ \t][^\[\]]*)?`

// caretLabelBody is the footnote-shaped label sub-pattern shared by the
// caret-exemption regexes below: "^" followed by at least one non-space,
// non-bracket character — the mirror of nonFootnoteCaretLabelAlt's
// boundary, so every label is classified the same way by the exemption
// checks and the def-shape checks.
const caretLabelBody = `\^[^ \t\[\]][^\[\]]*`

// nonCaretDefShapeRE matches a non-footnote link-reference-definition-
// shaped "[label]:" opener: an optional reflow-escape backslash, "[", an
// optional non-caret label (nonCaretLabelBody), "]:", ANYWHERE in the
// line — deliberately no left-boundary requirement. Two reasons. First,
// a mid-line shape can become a real definition: a preceding definition's
// title scan can consume everything before it ("[0]:0\n\"0\"[00]:0" makes
// "[00]:0" a second definition once the title "0" is absorbed — the
// chained-definition fuzz find). Second, and structurally: this
// paragraph-contains check must be AT LEAST as broad as any shape check
// applied to a NEIGHBOR line (anyDefShapeAnywhereRE below is
// boundary-free), or the verdicts destabilize — a paragraph whose own
// mid-line shape escaped this check would reflow, moving the shape to a
// different line, and flip the zone verdict of the paragraph below it on
// the next pass: found by FuzzFormat on "0[X1]: &Zz!\n  >0%0c) n2e%11"
// (seed f6ab6616a4253b35), where the blockquote deferred to a neighbor
// shape the neighbor itself was allowed to reshape.
var nonCaretDefShapeRE = regexp.MustCompile(`\\?\[(?:` + nonCaretLabelBody + `|` + nonFootnoteCaretLabelAlt + `)?\]:`)

// defLineOpenerRE is nonCaretDefShapeRE's counterpart for judging the
// raw source line directly above a paragraph (see inLinkRefDefZone). It
// excludes caret-led labels, the same exemption as the contains check —
// NOT because an adjacent "[^label]:" line is harmless (to a parser with
// no footnote extension it is an ordinary definition shape), but because
// deferring to a caret line can never be verdict-stable: a footnote body
// is exempt from the zone precisely so it can reflow, and its reflow
// legitimately rewrites the physical line a neighbor would key on — found
// by FuzzFormat on ")B[^1]: 78\n  + ,b X2nx1" (seed 86487504c2bddd82),
// where the list deferred on pass 1 to a caret line the exempt paragraph
// above then split. Caret-shape hazards are owned instead by the
// emission escapes (package reflow's isCompleteLinkRefDefLine and the
// bare-opener escape), the harness's documented caret scope gate, and
// the public convergence backstop — see design.md's zone section.
var defLineOpenerRE = regexp.MustCompile(`^[ \t>]*\\?\[(?:` + nonCaretLabelBody + `|` + nonFootnoteCaretLabelAlt + `)?\]:`)

// defShapeAnywhereRE is defLineOpenerRE's counterpart with no left-
// boundary requirement at all — used only for the "spans the boundary"
// check (see inLinkRefDefZone's (c)): a chain of link reference
// definitions can legitimately continue immediately after a previous one's
// title closes, with no whitespace or line break between them at all (a
// definition only needs to start where the previous block-level construct
// left off, not at a line's start) — found by FuzzFormat on
// "[0]:0\n\"0\"[00]:0\n\"\n\"[0]:0" (seed a651ae68822c7c5c), where a third
// definition's own destination scan reaches into a following paragraph
// that is otherwise just a lone quote character, sitting directly after
// "[00]:0" with no separating whitespace at all. Broader than
// defLineOpenerRE on purpose: the boundary requirement that keeps rules
// (a)/(b) from flagging ordinary prose like "word[key]: text" doesn't hold
// once a definition can start immediately after another already-consumed
// one, so this check accepts some extra false-positive skips as the price
// of staying blunt rather than tracking definition-chain state.
var defShapeAnywhereRE = regexp.MustCompile(`\\?\[(?:` + nonCaretLabelBody + `|` + nonFootnoteCaretLabelAlt + `)?\]:`)

// footnoteDefFirstLineRE matches a footnote definition's own opening
// "[^label]:" marker at the start of a paragraph's first (trimmed) line,
// tolerating package reflow's own escaped spelling ("\[^label]:"): a guard
// keyed on this shape must give the same verdict on the escaped spelling
// it will see on the very next pass, or the paragraph's continuation
// indent (see build's call site) flips width between passes — found by
// FuzzFormat on " [^1]: !Y )9.01" (seed 425ffd537fd28733), whose escaped
// second-pass first line ("\[^1]: !Y") stopped matching an earlier,
// escape-blind version of this regex.
var footnoteDefFirstLineRE = regexp.MustCompile(`^\\?\[` + caretLabelBody + `\]:`)

// bareCaretOpenerRE matches a BARE footnote-shaped opener — "[^label]:"
// with nothing after the colon but whitespace, at a line's end. A bare
// caret opener is not a footnote body (there is no body): to mdreflow's
// footnote-less parser it is an incomplete definition opener that
// completes using the NEXT line as its destination, i.e. exactly the
// multi-line-reach hazard the zone exists for, and it flips join verdicts
// with no typography involved (seeds 02389d5efed7d524 "[^0]:\n0\n\"\"0"
// and ead78c541f590c87). So bare caret openers join the zone — contains
// and neighbor checks alike — while caret openers WITH content after the
// colon (real footnote bodies) stay exempt: a body line with two or more
// tokens after the colon can never itself be a complete definition
// (trailing content disqualifies it), and a single-token "[^1]: word"
// line is already a LinkReferenceDefinition node that never reaches
// reflow as a paragraph.
// orphanDefCloserRE matches a line whose "]:" has no unescaped "[" before
// it on the same line — the orphaned tail of a definition label that
// OPENED on an earlier line ("[\]\n0\n]:0": the escaped bracket keeps the
// label open across two line breaks before "]:" closes it). The zone's
// other shapes all require the opener and closer on one line or in the
// two-line boundary window, so a label spanning three or more lines left
// its closer line invisible to them: found by FuzzFormat on
// "[\]\n0\n]:0\n\"\"0" (seed 0df31d8ad2438ba6), issue #11's shape one
// line deeper, where the closer-line's paragraph joined on pass 1 and the
// whole construct joined on pass 2. A literal "]:"-led line in prose is
// degenerate enough that the over-skip is free.
var orphanDefCloserRE = regexp.MustCompile(`^(?:\\.|[^\[\]\\])*\]:`)

// The trailing class includes "\r": CR is trailing whitespace to
// goldmark (same whitespace-alignment family as reflow's
// bareLinkRefDefOpenerLineRE and isTableDelimiterRowShaped), so a bare
// caret opener with a trailing CR is still a bare caret opener.
//
// There is deliberately NO left-boundary requirement, for exactly the
// reason defShapeAnywhereRE has none: a definition can start immediately
// after a previous one's title closes, with no whitespace between them at
// all. An earlier version required the shape to sit at a line start or
// after whitespace, which missed "\"7\"[^0]:" — a bare opener directly
// against the preceding definition's closing title quote (found by
// FuzzFormat on "[^0]:110\n\"7\"[^0]:\n軠1\n\"\"0", seed
// 39bb3b34cfc62d3d, 93 minutes into a soak). The extra over-skip this
// buys (a footnote body whose line happens to END in "x[^2]:") is the
// same free trade the rest of the zone makes.
var bareCaretOpenerRE = regexp.MustCompile(`\\?\[` + caretLabelBody + `\]:[ \t\r]*$`)

// inLinkRefDefZone reports whether the paragraph whose trimmed lines are
// trimmed, starting at contentStart, sits in design.md's link-reference-
// definition skip zone: design.md, "The link-reference-definition zone:
// skip bluntly, by shape" — any paragraph that contains, or sits directly
// against (no blank line), a line opening with a non-footnote "[label]:"
// shape (original, reflow-escaped, or reflow-joined spelling) passes
// through byte-for-byte. This single blunt, shape-based predicate replaces
// the prior six interlocking adjacency/title-absorption guards (see build's
// call site for the full list) — no parsing, no adjacency analysis.
//
// (a) checks trimmed against nonCaretDefShapeRE directly: TrimSpace only
// removes leading/trailing whitespace, so an interior "[label]:" shape and
// its left-boundary character (whitespace or '>') both survive trimming
// unchanged, and a shape starting at trimmed's own start still matches the
// "^" alternative.
//
// A paragraph whose own first line IS a footnote definition's opener
// (footnoteDefFirstLineRE) is exempt from everything below: it is a
// footnote body, deliberately reflow-eligible per design.md, and treating
// an adjacent (possibly also caret-led) preceding line as hazardous here
// would defeat that exemption for the ordinary back-to-back layout
// ("[^1]: ...\n[^2]: ..." with no blank line between).
//
// (b) checks the raw source line immediately above contentStart with
// anyDefLineOpenerRE (caret-inclusive — see its own doc comment for why):
// a blank line there can never match a "[...]:" shape, so "no blank line
// between" falls out for free.
// (c) checks whether a def shape spans the boundary itself, anywhere in
// the preceding-raw-line-plus-this-paragraph's-own-first-line window, with
// no left-boundary requirement (anyDefShapeAnywhereRE — see its own doc
// comment for why "opens with" alone is not enough): the label can open on
// the preceding raw line and close on this paragraph's own first line
// (found by FuzzFormat/issue#11 on "[\]\n]:0\n\"\"0"), or a definition can
// open immediately after a previous one's title closes with no separating
// whitespace at all (found by FuzzFormat on
// "[0]:0\n\"0\"[00]:0\n\"\n\"[0]:0", seed a651ae68822c7c5c).
func inLinkRefDefZone(source []byte, trimmed []string, contentStart int, zoneAbove map[int]bool) bool {
	for _, t := range trimmed {
		if nonCaretDefShapeRE.MatchString(t) || bareCaretOpenerRE.MatchString(t) || orphanDefCloserRE.MatchString(t) {
			return true
		}
	}
	if len(trimmed) > 0 && footnoteDefFirstLineRE.MatchString(trimmed[0]) {
		return false
	}
	ls := lineStart(source, contentStart)
	if ls == 0 {
		return false
	}
	if zoneAbove[ls] {
		// A def-shaped line anywhere above, in this line's contiguous
		// non-blank run — not just on the immediately preceding line: a
		// definition's title scan can reach the paragraph across any
		// number of intervening machinery lines. See defRunAbove.
		return true
	}
	prevStart := lineStart(source, ls-1)
	prevLine := bytes.TrimRight(source[prevStart:ls], "\r\n")
	if defLineOpenerRE.Match(prevLine) || bareCaretOpenerRE.Match(prevLine) || orphanDefCloserRE.Match(prevLine) {
		// orphanDefCloserRE on the neighbor too, not just this
		// paragraph's own lines: when a multi-line label's "]:" tail is
		// the line directly ABOVE, this paragraph's own first line is
		// the definition's absorbed destination, and joining it with
		// what follows invalidates the whole definition — found by
		// FuzzFormat on "[\]\n]:\n0\n\"\"0" (seed 0767a5cc905fe38b),
		// the mirror of seed 0df31d8ad2438ba6's contains-side case.
		return true
	}
	if len(trimmed) > 0 {
		window := string(prevLine) + "\n" + trimmed[0]
		if defShapeAnywhereRE.MatchString(window) {
			return true
		}
	}
	return false
}

// continuationPrefix derives the container-prefix bytes for every output
// line after a paragraph's first, given contentStart (the byte offset
// where the paragraph's first line of content begins, i.e. Node.Lines()'s
// first segment start).
//
// It reads the paragraph's actual first-line prefix straight from the
// source (the bytes between the start of that physical line and
// contentStart) and transforms it: every '>' byte (a blockquote marker,
// however deeply nested) is kept literally, everything else (list-item
// marker glyphs, digits, '.', ')', plain padding) becomes a space. This
// composes correctly for arbitrary list-in-quote / quote-in-list nesting
// without needing to walk the AST ancestor chain or reconstruct per-level
// marker widths: whatever the original first line's prefix looked like,
// same-width padding with the quote markers preserved is always a valid
// continuation prefix for a paragraph reflowed to more (or fewer) output
// lines than it started with.
//
// This is a deliberate, canonical continuation-indent choice: mdreflow
// already changes where this paragraph's line breaks fall, so it also owns
// the whitespace of the new lines it introduces, rather than trying to
// preserve a per-original-line prefix (which can vary source line-to-line
// under CommonMark lazy continuation, e.g. a blockquote continuation line
// that omits "> ").
func continuationPrefix(source []byte, contentStart int) string {
	ls := lineStart(source, contentStart)
	prefix := source[ls:contentStart]
	var out []byte
	col := 0
	for _, b := range prefix {
		switch b {
		case '>':
			out = append(out, '>')
			col++
		case '\t':
			// A tab advances to the next 4-column tab stop (CommonMark's
			// rule), so it must become that many spaces, not one: a
			// byte-for-byte space kept the prefix's BYTE width but lost
			// columns, so a continuation line under a "*\t>" item landed
			// left of the item's content column and escaped the list
			// entirely on reparse — found by FuzzFormat on
			// "*\t>90x.80( 0" (seed 235abda3112b806e), whose reflowed
			// second line reparsed as a fresh top-level blockquote.
			n := 4 - col%4
			for range n {
				out = append(out, ' ')
			}
			col += n
		default:
			out = append(out, ' ')
			col++
		}
	}
	return string(out)
}

// LineStart returns the byte offset of the start of the physical source
// line containing pos: the position right after the preceding '\n', or 0.
// Exported for package reflow, which needs it to recover a boundary line's
// full raw bytes (container prefix included) for byte-for-byte emission.
func LineStart(source []byte, pos int) int {
	return lineStart(source, pos)
}

func lineStart(source []byte, pos int) int {
	i := pos
	for i > 0 && source[i-1] != '\n' {
		i--
	}
	return i
}

// unterminatedTagStartRE matches a line that opens an HTML/JSX tag (a "<"
// immediately followed by a letter — CommonMark's tag-name-start rule,
// which is also why "<3 hearts" is never mistaken for a tag) but has no
// closing '>' anywhere on that same line.
var unterminatedTagStartRE = regexp.MustCompile(`^<[A-Za-z]`)

// looksLikeUnterminatedTag reports whether firstLineTrimmed opens a tag
// that does not close on the same line — see its call site in build for
// why this triggers a whole-paragraph skip.
func looksLikeUnterminatedTag(firstLineTrimmed string) bool {
	return unterminatedTagStartRE.MatchString(firstLineTrimmed) && !strings.ContainsRune(firstLineTrimmed, '>')
}

// linkifyStartRE matches where GFM's linkify extension turns text into a
// bare link: a scheme-prefixed URL, a "www."-prefixed one, or an email
// address. Same shape as package reflow's linkifyTokenStart (kept as an
// independent copy — the two serve different roles: that one decides
// whether a break may move a token, this one decides whether to skip a
// paragraph) but deliberately UNANCHORED: linkify fires mid-token too,
// e.g. after a "](" that never opened a real link, which is exactly the
// shape seed 41e98cb4c9e00729 carries.
var linkifyStartRE = regexp.MustCompile(
	"(?i)(?:[a-z][a-z0-9+.-]*://|www\\.|[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9-]+(?:\\.[a-z0-9-]+)+)")

// hasBacktickInBareURL reports whether any whitespace-delimited token in
// trimmedLines both starts like a linkify-eligible bare URL and contains
// a backtick — see build's call site for the code-span pairing hazard.
//
// Verdict-stable by construction: a matching paragraph is skipped whole,
// so its own tokens never move, and reflow can never *create* such a
// token in another paragraph (joining lines inserts a space between
// fragments, so two tokens never fuse; splitting only breaks tokens
// apart).
func hasBacktickInBareURL(trimmedLines []string) bool {
	for _, line := range trimmedLines {
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\r'
		}) {
			if strings.Contains(tok, "`") && linkifyStartRE.MatchString(tok) {
				return true
			}
		}
	}
	return false
}

// hasUnbalancedBracket reports whether trimmedLines, taken together (a
// paragraph's lines, in order), ever has a "[" left open at the end of a
// line — including one later closed by a "]" on a *subsequent* line, not
// just one never closed at all: a link label spanning that line boundary
// is the risk (see build's call site), and it exists either way. Checking
// only the *final*, whole-paragraph bracket balance is not enough — found
// by FuzzFormat on "[\n0]:0\n\"\"0", whose "[" (line 1) and "]" (line 2)
// balance out over the paragraph as a whole, net zero, even though the
// same multi-line-label hazard is still there.
func hasUnbalancedBracket(trimmedLines []string) bool {
	return hasUnclosedDelimiterAcrossLine(trimmedLines, '[', ']')
}

// hasUnclosedDestParen is hasUnbalancedBracket's counterpart for the
// paren side of an inline link: the *destination* — "[text](destination)"
// — can span a soft line break exactly the way a link label can (see
// hasUnbalancedBracket's doc comment for the label case), so an open "("
// left unclosed at the end of a line is the same class of hazard, found
// by FuzzFormat on "[](  \n\")0": mdreflow's hard-break-style
// normalization of the trailing two spaces after "[](" changed the
// destination's interior content (whitespace collapsed differently once
// the source's own line break became literal "<br>" marker text instead),
// producing a broken (and, worse, different) link on reparse instead of
// the original's link to a literal '"' character.
//
// Unlike the bracket arm, an arbitrary unclosed "(" is not the hazard
// (issue #16: a plain prose parenthetical spanning a line break opens
// nothing). CommonMark admits no whitespace at all between a link's "]"
// and its "(" — "[text]\n(/dest)" and "[text] (/dest)" parse as zero
// links, checked directly against goldmark — so only a "(" immediately
// preceded by "]" can open a destination. Each open paren therefore
// carries its own armed flag (was it "]("-opened?) on a stack, and only
// an armed paren still open at the end of a non-final line triggers the
// skip. The flag is per-paren, not only for the outermost: the original
// hazard nested inside an ordinary prose parenthetical — "(x [](  \n\")0)"
// — is the same spanning destination (confirmed against goldmark: it
// parses as a link before reflow, and a depth-0-only check reflows it
// into literal text plus a "<br>" marker, a render change reproduced by
// seed issue16-dest-paren-nested).
func hasUnclosedDestParen(trimmedLines []string) bool {
	var open []bool // one entry per currently open "("; true if "]("-opened
	for i, line := range trimmedLines {
		for j := 0; j < len(line); j++ {
			switch line[j] {
			case '(':
				open = append(open, j > 0 && line[j-1] == ']')
			case ')':
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			}
		}
		if i < len(trimmedLines)-1 && slices.Contains(open, true) {
			return true
		}
	}
	return false
}

// hasUnclosedAngleDestOpener reports whether any of trimmedLines contains
// an inline-link angle-destination opener — "](", optional spaces, "<" —
// with no ">" after it on the same line. CommonMark forbids a newline
// inside a "<...>" destination, so such a source is literal text — but
// joining the lines removes the newline, and a ">" on a later line can
// complete the destination, turning literal text into a real link: found
// by FuzzFormat on "[](<)\n0>)" (seed e18cc56eacbb7c92), whose joined
// form "[](<) 0>)" parses "<) 0>" as a valid spaces-allowed angle
// destination. Same crude-and-conservative spanning-construct treatment
// as hasUnbalancedBracket/hasUnbalancedParen above: skip the paragraph.
func hasUnclosedAngleDestOpener(trimmedLines []string) bool {
	for _, line := range trimmedLines {
		rest := line
		for {
			k := strings.Index(rest, "](")
			if k < 0 {
				break
			}
			rest = rest[k+2:]
			j := 0
			for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t') {
				j++
			}
			if j < len(rest) && rest[j] == '<' && !strings.Contains(rest[j:], ">") {
				return true
			}
		}
	}
	return false
}

// hasUnclosedDelimiterAcrossLine reports whether trimmedLines, taken
// together, ever has an "open" byte left structurally unclosed at the end
// of a line (including one later closed by a "close" byte on a
// subsequent line).
//
// This tracks proper (clamped-at-zero) nesting depth character by
// character, not a per-line arithmetic open-minus-close count: a stray,
// unmatched close byte earlier in a line must not appear to "cancel out"
// an unrelated open byte later in the *same* line, since they are not
// actually paired — found by FuzzFormat on ")[](  \n)": line one's
// leading ")" and trailing "(" arithmetically sum to zero (one open, one
// close), which looks balanced, but the "(" is structurally still open at
// the end of the line (the leading ")" is just stray, unmatched text
// before it, not its partner) and does span into line two.

// hasEmptyLine reports whether any of trimmedLines is the empty string.
func hasEmptyLine(trimmedLines []string) bool {
	for _, line := range trimmedLines {
		if line == "" {
			return true
		}
	}
	return false
}

func hasUnclosedDelimiterAcrossLine(trimmedLines []string, open, close byte) bool {
	depth := 0
	for i, line := range trimmedLines {
		for j := 0; j < len(line); j++ {
			switch line[j] {
			case open:
				depth++
			case close:
				if depth > 0 {
					depth--
				}
			}
		}
		if depth > 0 && i < len(trimmedLines)-1 {
			return true
		}
	}
	return depth > 0
}

// maskCodeSpans returns trimmedLines with the interior of every inline
// code span replaced by a filler byte, preserving line structure and byte
// offsets so the delimiter scans above see identical geometry.
//
// A bracket inside a code span is literal: it cannot open a link label, a
// reference definition, or a destination, so it is not the hazard the
// spanning-delimiter guards exist for. It still arms them today, which
// skips paragraphs that merely *document* Markdown or YAML syntax
// ("`runs-on: [self-hosted,` / `<label>]`").
//
// Masking follows CommonMark's code-span rule rather than "text between
// backticks": a backtick run of length N opens a span that only a run of
// exactly N closes, newlines included. A run with no matching closer is
// literal text and is deliberately left unmasked — that is what keeps an
// unclosed "`unclosed [bracket" arming the guard, where the bracket is
// ordinary prose and the paragraph really is hazardous.
//
// ORDERING INVARIANT (issue #28): this pairing does not model GFM
// linkify, so a backtick inside a linkify-eligible bare URL — which
// goldmark consumes into the link destination, never a delimiter — pairs
// one out of step here and masks bytes goldmark treats as live prose,
// including a real "[label]:" opener (which would disarm
// couldFormLinkRefDef on a paragraph that genuinely contains a
// definition). This function is only sound because build's
// hasBacktickInBareURL check returns first and skips every such
// paragraph before masking runs. Anyone narrowing that guard must keep
// this blind spot covered; TestMaskCodeSpansRequiresBareURLGuard pins
// the dependency. The render backstop bounds the damage if this ever
// regresses — a wrongly-disarmed guard becomes a reverted reflow, not
// content loss — but a silently dead guard is still a bug.
func maskCodeSpans(trimmedLines []string) []string {
	joined := strings.Join(trimmedLines, "\n")
	b := []byte(joined)
	out := make([]byte, len(b))
	copy(out, b)
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(b) && b[i+run] == '`' {
			run++
		}
		// Look for a closing run of exactly the same length.
		j := i + run
		closed := -1
		for j < len(b) {
			if b[j] != '`' {
				j++
				continue
			}
			r2 := 0
			for j+r2 < len(b) && b[j+r2] == '`' {
				r2++
			}
			if r2 == run {
				closed = j
				break
			}
			j += r2
		}
		if closed < 0 {
			// No closer: the run is literal text, mask nothing.
			i += run
			continue
		}
		for k := i + run; k < closed; k++ {
			if out[k] != '\n' {
				out[k] = 'x'
			}
		}
		i = closed + run
	}
	return strings.Split(string(out), "\n")
}

// couldFormLinkRefDef reports whether a "]:" appears anywhere in the
// paragraph, the only shape a link reference definition can be built from.
//
// hasUnbalancedBracket exists for one hazard, named in its own doc comment:
// a definition *label* spanning a soft line break, so that reflow's join or
// re-split changes which bytes complete it ("[\n0]:0\n\"\"0"). Every other
// bracket construct tolerates a soft break in its label or text by
// construction. CommonMark allows a newline inside inline-link text,
// reference-link labels, image alt text and footnote labels, and label
// matching collapses internal whitespace, so "[a\nb]" and "[a b]" resolve
// to the same definition. The destination side, where a newline genuinely
// is load-bearing, is guarded separately by hasUnclosedDestParen and
// hasUnclosedAngleDestOpener.
//
// Without a "]:" no definition can form however the lines are rearranged,
// so a spanning bracket is a wrapped link and safe to reflow. This is worth
// more than tidiness: such a paragraph is otherwise a fixed point. It is
// skipped because a link spans a break, and being skipped is exactly what
// stops that break ever being removed, so it keeps its pre-reflow wrapping
// permanently. Adopting sentence-per-line across a 27,848-line docset left
// 60 such lines stranded, recoverable only by joining each link by hand.
func couldFormLinkRefDef(trimmedLines []string) bool {
	for _, line := range trimmedLines {
		if strings.Contains(line, "]:") {
			return true
		}
	}
	// A "]" ending one line and a ":" opening the next is the same shape
	// spread across the break.
	for i := 0; i < len(trimmedLines)-1; i++ {
		if strings.HasSuffix(trimmedLines[i], "]") && strings.HasPrefix(trimmedLines[i+1], ":") {
			return true
		}
	}
	return false
}

// admonitionMarkerRE matches a MkDocs / Python-Markdown admonition marker
// line: "!!! note", "??? warning", "???+ tip", optionally with a quoted
// title. The type word is required, which is what keeps an ordinary
// paragraph merely starting with "!!!" from claiming the block below it.
var admonitionMarkerRE = regexp.MustCompile(`^(?:!{3}|\?{3}\+?)[ \t]+[A-Za-z][\w-]*(?:[ \t]+"[^"]*")?[ \t]*$`)

// admonitionBody reports whether cb is the indented body of a MkDocs
// admonition and, if so, describes it as a reflow-eligible paragraph.
//
// MkDocs and Python-Markdown write an admonition as a marker line followed
// by a blank line and a 4-space-indented body. That body is ordinary prose,
// but no CommonMark parser can know it: an indented block is an indented
// code block, so goldmark hands it over as *ast.CodeBlock and the paragraph
// walk never sees it. On a real MkDocs docset that silently excludes every
// callout from reflow — 17 blocks and 69 prose lines on the 27,848-line
// tree this was measured against.
//
// Two conditions keep the recognition honest. The previous sibling must be
// a paragraph whose only line is an admonition marker, so a genuine
// indented code block after ordinary prose is untouched. And the body must
// contain no fence marker: a fenced block indented inside an admonition is
// literal text to goldmark, so reflowing it would rewrap real code.
func admonitionBodies(cb *ast.CodeBlock, source []byte, mkdocs bool) []Paragraph {
	if !mkdocs {
		return nil
	}
	prev := cb.PreviousSibling()
	if prev == nil || prev.Kind() != ast.KindParagraph || prev.Lines().Len() != 1 {
		return nil
	}
	ps := prev.Lines().At(0)
	if !admonitionMarkerRE.Match(bytes.TrimRight(ps.Value(source), " \t\r\n")) {
		return nil
	}
	lines := cb.Lines()
	if lines.Len() == 0 {
		return nil
	}
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		t := bytes.TrimLeft(seg.Value(source), " \t")
		if bytes.HasPrefix(t, []byte("```")) || bytes.HasPrefix(t, []byte("~~~")) {
			return nil
		}
	}
	first := lines.At(0)
	start := lineStart(source, first.Start)
	contPrefix := string(source[start:first.Start])
	if strings.TrimLeft(contPrefix, " \t") != "" {
		return nil
	}
	// A multi-paragraph body is left alone. goldmark keeps the separating
	// blank lines inside the one code block and package reflow works from
	// Node.Lines() rather than the Start/End range, so every run would be
	// reflowed as a single cluster and two rendered paragraphs would merge
	// into one <p>. Splitting them needs a per-run node, which is more
	// surgery than the recognition itself is worth; a single-paragraph
	// callout is the overwhelmingly common shape.
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		if len(bytes.TrimSpace(seg.Value(source))) == 0 {
			return nil
		}
	}
	last := lines.At(lines.Len() - 1)
	return []Paragraph{{
		Node:       cb,
		Start:      first.Start,
		End:        last.Stop,
		ContPrefix: contPrefix,
		Boundary:   make([]bool, lines.Len()),
	}}
}
