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

	"github.com/jbeda/mdreflow/internal/gm"
	"github.com/jbeda/mdreflow/internal/segment"
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
	// EscapeIsContent marks a paragraph that is prose only to the dialect's
	// own renderer, and an indented code block to the CommonMark parser
	// mdreflow reflows against — the MkDocs admonition body admonitionBodies
	// recognizes. A backslash reflow adds to keep a wrapped line from
	// reparsing as a list or a fence is markup to MkDocs and literal text to
	// CommonMark, so the two renders cannot both be preserved. Package
	// reflow backs the whole paragraph out to its source bytes rather than
	// emit an escape here; see writeParagraph.
	EscapeIsContent bool
}

// SkipReason says which guard froze a paragraph — which distinct branch
// in build (or collect's depth cap) decided the paragraph passes through
// byte-for-byte instead of reflowing. SkipNone means the paragraph is
// reflow-eligible. Each reason's meaning is documented at the branch that
// returns it; user-facing wording lives in the root package's Explain.
type SkipReason uint8

const (
	SkipNone SkipReason = iota
	// SkipDeepNesting: nested beyond maxContainerDepth container levels.
	SkipDeepNesting
	// SkipDegenerateBlank: the paragraph (or one of its lines) trims to
	// nothing — a control-character parser artifact, never real prose.
	SkipDegenerateBlank
	// SkipDialectBlock: a dialect whole-node rule matched (front-matter
	// fence, math block, MDX construct, shortcode) — not prose at all.
	SkipDialectBlock
	// SkipFrontMatter: the paragraph sits inside the document's front
	// matter block.
	SkipFrontMatter
	// SkipHiddenLineGap: another node's bytes hide between this
	// paragraph's line segments.
	SkipHiddenLineGap
	// SkipDoubleOwnedLine: a sibling link-reference-definition node owns
	// bytes inside this paragraph's range (duplicate-label extraction).
	SkipDoubleOwnedLine
	// SkipControlBytes: a C0 control byte other than tab/LF/CR in the
	// paragraph's raw range.
	SkipControlBytes
	// SkipLinkRefDefShape: the paragraph itself contains a
	// definition-shaped line (the zone's contains check).
	SkipLinkRefDefShape
	// SkipLinkRefDefNeighbor: definition machinery directly above puts
	// the paragraph inside a definition's reach (the zone's neighbor and
	// defAbove checks).
	SkipLinkRefDefNeighbor
	// SkipRawHTMLDeclOpener: a raw "<?" or "<!" opener outside code spans.
	SkipRawHTMLDeclOpener
	// SkipPossibleLinkRefDef: an unbalanced "[" (with a def-plausible
	// shape) or an unclosed destination that a reflow join could complete
	// into a link reference definition.
	SkipPossibleLinkRefDef
	// SkipUnterminatedTag: the first line looks like an HTML/JSX tag
	// whose closing ">" is on a later line.
	SkipUnterminatedTag
	// SkipTableAdjacency: the paragraph sits directly under a GFM table
	// with no blank line between them.
	SkipTableAdjacency
)

// Skip records one frozen paragraph: the byte range its raw source lines
// span (same convention as Paragraph.Start/End) and the guard that froze
// it.
type Skip struct {
	Start, End int
	Reason     SkipReason
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
	collect(doc, source, false, 0, fmEnd, scanLineFacts(source), &out, nil, mkdocs)
	return out
}

// SkipsForDialect walks doc exactly as ParagraphsForDialect does and
// returns, in source order, every paragraph the walk froze and why —
// the diagnostic mirror of the eligible set (--explain). Skips with no
// reportable source range (an empty paragraph node) and front-matter
// interiors (metadata by construction, not frozen prose) are omitted.
func SkipsForDialect(doc ast.Node, source []byte, mkdocs bool) []Skip {
	var out []Paragraph
	var skips []Skip
	fmEnd := frontMatterEnd(source)
	collect(doc, source, false, 0, fmEnd, scanLineFacts(source), &out, &skips, mkdocs)
	return skips
}

// lineFacts holds the per-physical-line verdicts inLinkRefDefZone needs,
// keyed by the line's start byte offset in scanLineFacts's returned map.
// chainStart, orphanCloser, and bareCaretOpener are the direct regex
// verdicts for the line itself (defChainStartRE, orphanDefCloserRE, and
// bareCaretOpenerRE respectively); defAbove is the transitive seen-above
// bit described below.
type lineFacts struct {
	chainStart, orphanCloser, bareCaretOpener, defAbove bool
}

// scanLineFacts computes, per physical line (keyed by the line's start byte
// offset), the facts inLinkRefDefZone needs about that line and about
// whether a definition-shaped line — the same shapes inLinkRefDefZone
// checks on the immediately preceding line — occurs anywhere ABOVE that
// line within its contiguous run of non-blank lines. A blank line resets
// the run.
//
// The transitive defAbove bit exists because a definition's reach downward
// is not limited to one line: its title alone may span arbitrarily many
// lines, so a paragraph can sit several lines below the "[label]:" opener
// yet still be the next text the definition's own scan touches —
// reflowing that paragraph moves the title's closing boundary and
// re-carves every line in between on the next parse. Found by FuzzFormat
// on "[0]:\n1\n\"\n\"[0]:0\n[1]:0\n\"20\n0\n00\n\"" (seed
// 97329a80dd2cb7d4): the only reflow-eligible paragraph was the two-line
// tail of a title spanning three lines below its def, one line beyond the
// neighbor check. The transitive rule is verdict-stable by construction:
// every paragraph inside a def-containing run is in-zone, so nothing in
// such a run ever reflows, so the run's line layout — and with it every
// verdict keyed on it — cannot change between passes. Computed in one
// top-down pass so the zone check stays O(1) per paragraph.
func scanLineFacts(source []byte) map[int]lineFacts {
	m := make(map[int]lineFacts)
	seen := false
	for ls := 0; ls < len(source); {
		end := ls
		for end < len(source) && source[end] != '\n' {
			end++
		}
		line := bytes.TrimRight(source[ls:end], "\r")
		f := lineFacts{
			chainStart:      defChainStartRE.Match(line) || parsesAsDefLine(line),
			orphanCloser:    orphanDefCloserRE.Match(line),
			bareCaretOpener: bareCaretOpenerRE.Match(line),
			defAbove:        seen,
		}
		m[ls] = f
		if len(bytes.Trim(line, " \t")) == 0 {
			seen = false
		} else if f.chainStart || f.bareCaretOpener || f.orphanCloser {
			seen = true
		}
		ls = end + 1
	}
	return m
}

// containerPrefixRE matches the blockquote/list-marker prefix
// defChainStartRE tolerates before a definition opener. parsesAsDefLine
// strips it so the remainder can be parsed on its own.
var containerPrefixRE = regexp.MustCompile(`^[ \t>]*(?:(?:[-+*]|\d{1,9}[.)])[ \t]+[ \t>]*)*`)

// parsesAsDefLine reports whether line, with any container prefix removed,
// is a complete link reference definition to goldmark itself.
//
// This is the label-shape-agnostic half of the def-chain-start fact, and it
// is what closes issue #58. The regexes beside it exclude caret labels, on
// the reasoning that "[^1]:" is a footnote and a footnote body is prose —
// but no footnote extension is registered in package gm, so goldmark reads
// "[^1]: /url" as an ordinary definition whose title may sit on the line
// below. Sentence mode splitting that paragraph then feeds the definition
// its own first line, which disappears from the rendered page.
//
// Asking the parser rather than widening the regexes is what keeps issue
// #41's coverage: a real footnote body ("[^1]: some ordinary prose") is not
// a definition to goldmark and answers false, so back-to-back footnote
// layouts still reflow. Only caret lines that genuinely are definitions —
// a bare destination, "[^1]: /url" or "[^1]: one" — newly set the fact, and
// those are live definition machinery whatever the label looks like.
//
// The verdict reads only the line above a paragraph, which is inside the
// definition run and so is never reflowed, keeping it stable across passes.
func parsesAsDefLine(line []byte) bool {
	rest := line[len(containerPrefixRE.Find(line)):]
	return gm.IsCompleteLinkRefDefLine(string(rest))
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
func collect(n ast.Node, source []byte, inBlockquote bool, depth int, fmEnd int, facts map[int]lineFacts, out *[]Paragraph, skips *[]Skip, mkdocs bool) {
	// recordSkip reports c's line range under reason to the skips
	// collector (nil when the caller only wants the eligible set). An
	// empty node has no source range to report; front-matter interiors
	// are metadata by construction, not frozen prose — neither is a
	// diagnostic anyone can act on.
	recordSkip := func(c ast.Node, reason SkipReason) {
		if skips == nil || reason == SkipFrontMatter {
			return
		}
		lines := c.Lines()
		if lines.Len() == 0 {
			return
		}
		*skips = append(*skips, Skip{
			Start:  lines.At(0).Start,
			End:    lines.At(lines.Len() - 1).Stop,
			Reason: reason,
		})
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if cb, ok := c.(*ast.CodeBlock); ok {
			*out = append(*out, admonitionBodies(cb, source, mkdocs)...)
			continue
		}
		switch c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			if depth > maxContainerDepth {
				// Pass through byte-for-byte; see collect's doc comment.
				recordSkip(c, SkipDeepNesting)
				continue
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
			if pp, reason := build(c, source, inBlockquote, fmEnd, precededByTable, facts, mkdocs); reason == SkipNone {
				*out = append(*out, pp)
			} else {
				recordSkip(c, reason)
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
		collect(c, source, childInBQ, childDepth, fmEnd, facts, out, skips, mkdocs)
	}
}

// build derives a Paragraph from p (an *ast.Paragraph or *ast.TextBlock),
// or reports the reason p must instead pass through byte-for-byte
// (SkipNone means eligible). fmEnd is the document's front-matter end
// offset (frontMatterEnd(source), or -1 if source has no front matter) —
// see its use below. precededByTable is true when p's immediately
// preceding sibling in the AST is a GFM *ast.Table — see its use below
// for why. mkdocs gates the admonition-marker boundary check below: a
// line starting "!!!"/"???" is only treated as an immovable marker under
// the mkdocs dialect.
func build(p ast.Node, source []byte, inBlockquote bool, fmEnd int, precededByTable bool, facts map[int]lineFacts, mkdocs bool) (pp Paragraph, reason SkipReason) {
	lines := p.Lines()
	n := lines.Len()
	if n == 0 {
		// Empty paragraph; nothing to emit specially. Reported under
		// SkipDegenerateBlank, though recordSkip drops it anyway (no
		// source range to point at).
		return Paragraph{}, SkipDegenerateBlank
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
		return Paragraph{}, SkipDegenerateBlank
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
		return Paragraph{}, SkipDegenerateBlank
	}
	if wholeNodeSkip(trimmed) {
		return Paragraph{}, SkipDialectBlock
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
		return Paragraph{}, SkipFrontMatter
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
		return Paragraph{}, SkipHiddenLineGap
	}
	if overlapsSiblingDef(p, start0, end) {
		// hasHiddenLineGap's sibling case, found by FuzzFormat on
		// "[0]:0\n\"\n\"[0]:0\n[^0]:0\n[^0]:0\n0" (issue #35): goldmark's
		// duplicate-label handling extracts a repeated definition line as a
		// LinkReferenceDefinition node AND leaves the same bytes as this
		// paragraph's first Lines() segment — one physical line owned by two
		// sibling nodes at once. The caret exemption (inLinkRefDefZone) would
		// let such a paragraph reflow as a footnote body, but the line is
		// live definition machinery: joining it with the line below destroys
		// the neighbor's own def shape, so each reparse extracts one fewer
		// definition and the def/paragraph boundary migrates up one line per
		// pass — a treadmill, never a fixpoint. Whenever any sibling
		// definition node's raw lines overlap this paragraph's own byte
		// range, the parse has double-owned bytes and no reflow of them can
		// be stable; skip the whole paragraph, byte-for-byte.
		return Paragraph{}, SkipDoubleOwnedLine
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
		return Paragraph{}, SkipControlBytes
	}
	if zone := inLinkRefDefZone(source, trimmed, start0, facts); zone != SkipNone {
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
		return Paragraph{}, zone
	}
	masked := maskCodeSpans(trimmed)
	if hasRawHTMLDeclOpener(masked) {
		// A "<?" (processing instruction) or "<!" (comment, CDATA,
		// declaration) raw-HTML opener anywhere in the paragraph, outside
		// code spans, makes reflow structurally unstable — found by
		// FuzzFormat on "\r<?0\n000000000000000000000000?>" (seed
		// unterminated_pi_first_line), with the mid-line variants confirmed
		// by hand. Two facts collide. goldmark's inline grammar lets these
		// constructs span soft line breaks, and segment's ask-goldmark walk
		// faithfully protects the whole construct as one no-break span — so
		// a join can put the opener at an output line's start. There, HTML
		// blocks of types 2-5 interrupt an open paragraph from any line
		// position (reflow.blockInterruptTriggers), so emission MUST
		// backslash-escape the opener — and the escaped spelling no longer
		// parses as raw HTML at all, so the next pass computes different
		// no-break spans and different breaks: a parse discontinuity across
		// reflow's own escape, oscillating instead of converging. Inline
		// tags ("<" + letter) don't need this: their guard
		// (segment.htmlTagOpenerSpans) is a raw-text regex that matches the
		// escaped and unescaped spellings alike, so its verdict is
		// escape-stable. The blunt, safe answer is the zone playbook's:
		// pass the whole paragraph through byte-for-byte. Prose that
		// carries a bare PI/declaration opener outside a code span is
		// vanishingly rare, so the coverage cost is negligible.
		return Paragraph{}, SkipRawHTMLDeclOpener
	}
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
		return Paragraph{}, SkipPossibleLinkRefDef
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
		return Paragraph{}, SkipUnterminatedTag
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
		return Paragraph{}, SkipTableAdjacency
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
	if mkdocs && admonitionMarkerPunctRE.MatchString(trimmed[0]) {
		// A MkDocs admonition written without a blank line after its
		// marker is one paragraph here: the indented body is a lazy
		// continuation, not the code block the blank-line spelling
		// produces. Joining the marker into the body destroys the
		// callout, and so does dropping the body's indent, so the marker
		// line is immovable and the body carries the 4-space indent the
		// extension requires. Same treatment as the footnote definition
		// above, for the same reason. Gated on the mkdocs dialect: under
		// GFM a line starting "!!!"/"???" is ordinary prose.
		//
		// The line is pinned on the opening punctuation alone, but only a
		// line that also carries a class word is a marker whose body takes
		// the indent: pinning keeps the verdict stable across passes (see
		// admonitionMarkerPunctRE), while indenting under a non-marker like
		// a bare "!!!" would turn ordinary prose into an indented code
		// block.
		boundary[0] = true
		if admonitionMarkerRE.MatchString(trimmed[0]) {
			contPrefix += "    "
		}
	}

	return Paragraph{
		Node:       p,
		Start:      start0,
		End:        end,
		ContPrefix: contPrefix,
		Boundary:   boundary,
	}, SkipNone
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

// overlapsSiblingDef reports whether any sibling LinkReferenceDefinition
// node's raw lines overlap the half-open byte range [start, end) — see its
// call site in build for why double-owned bytes make a paragraph
// unreflowable. Only siblings under the same parent can share a physical
// line with this paragraph, so the scan does not need to walk the whole
// document.
func overlapsSiblingDef(p ast.Node, start, end int) bool {
	parent := p.Parent()
	if parent == nil {
		return false
	}
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		d, ok := c.(*ast.LinkReferenceDefinition)
		if !ok {
			continue
		}
		dl := d.Lines()
		if dl.Len() == 0 {
			continue
		}
		if dl.At(0).Start < end && dl.At(dl.Len()-1).Stop > start {
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
// defChainStartRE's doc comment).
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

// defChainStartRE is nonCaretDefShapeRE's counterpart for judging whether
// the raw source line directly above a paragraph (see inLinkRefDefZone)
// OPENS a definition chain — used for the chainStart fact (see
// scanLineFacts), which feeds both inLinkRefDefZone's direct
// immediately-preceding-line check and, transitively, the defAbove reach
// across intervening lines.
//
// It excludes caret-led labels, the same exemption as the contains check
// — NOT because an adjacent "[^label]:" line is harmless (to a parser
// with no footnote extension it is an ordinary definition shape), but
// because deferring to a caret line can never be verdict-stable: a
// footnote body is exempt from the zone precisely so it can reflow, and
// its reflow legitimately rewrites the physical line a neighbor would key
// on — found by FuzzFormat on ")B[^1]: 78\n  + ,b X2nx1" (seed
// 86487504c2bddd82), where the list deferred on pass 1 to a caret line
// the exempt paragraph above then split. Caret-shape hazards are owned
// instead by the emission escapes (package reflow's
// isCompleteLinkRefDefLine and the bare-opener escape), the harness's
// documented caret scope gate, and the public convergence backstop — see
// design.md's zone section.
//
// The label may sit after any number of list-marker segments:
// definitions are extracted starting at a paragraph's first content,
// which inside a list item sits after the marker, so "- [a]: /url" opens
// a definition chain exactly as a bare "[a]: /url" line does. The
// marker-segment group repeats to cover nested items
// ("- - [a]: /url"). A marker glyph not followed by whitespace is not a
// marker and stops the prefix match there, so "* *emphasis* [x]:" does
// not match (the "*" before "emphasis" has no trailing whitespace of its
// own to close the marker segment, and once the alternation gives up on
// consuming it as a marker the fixed "[ \t>]*" prefix cannot skip over
// the intervening "*emphasis* " prose either).
var defChainStartRE = regexp.MustCompile(`^[ \t>]*(?:(?:[-+*]|\d{1,9}[.)])[ \t]+[ \t>]*)*\\?\[(?:` + nonCaretLabelBody + `|` + nonFootnoteCaretLabelAlt + `)?\]:`)

// defShapeAnywhereRE is defChainStartRE's counterpart with no left-
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
// defChainStartRE on purpose: the boundary requirement that keeps rules
// (a)/(b) from flagging ordinary prose like "word[key]: text" doesn't hold
// once a definition can start immediately after another already-consumed
// one, so this check accepts some extra false-positive skips as the price
// of staying blunt rather than tracking definition-chain state. A match
// wholly inside the preceding line is only chain-relevant when everything
// to its left on that line is container-prefix-plausible — see
// inLinkRefDefZone's (c) and plausibleDefPrefix — since a mid-line shape
// with prose to its left (e.g. an inline-code error message quoted in a
// bullet) can never be reached by a definition chain, which must start at
// a paragraph's first content.
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
// A paragraph whose own first line IS a footnote definition's opener is a
// footnote body, deliberately reflow-eligible per design.md. That needs no
// exemption from the neighbor checks below: the ordinary back-to-back
// layout ("[^1]: ...\n[^2]: ..." with no blank line between) sets none of
// the facts they consult, while the caret-shaped lines that DO set one
// (bare openers, orphaned closers) are live definition machinery hazardous
// to any paragraph beneath them — see the no-exemption comment in the body
// (#41 and its bare-caret sibling).
//
// (b) checks the raw source line immediately above contentStart with
// anyDefLineOpenerRE (caret-inclusive — see its own doc comment for why):
// a blank line there can never match a "[...]:" shape, so "no blank line
// between" falls out for free.
// (c) checks whether a def shape occurs anywhere in the
// preceding-raw-line-plus-this-paragraph's-own-first-line window
// (defShapeAnywhereRE — see its own doc comment for why "opens with"
// alone is not enough), classified by where the match falls. A match
// spanning the boundary itself fires unconditionally: the label can open
// on the preceding raw line and close on this paragraph's own first line
// (found by FuzzFormat/issue#11 on "[\]\n]:0\n\"\"0"), or a definition can
// open immediately after a previous one's title closes with no separating
// whitespace at all (found by FuzzFormat on
// "[0]:0\n\"0\"[00]:0\n\"\n\"[0]:0", seed a651ae68822c7c5c). A match
// wholly inside the preceding line fires only when everything to its left
// on that line is container-prefix-plausible (plausibleDefPrefix): only
// then could a definition chain have started at that line's own first
// content and reached the shape, since a chain's opener must pass
// defChainStartRE, which requires nothing but markers and padding before
// the label. A mid-line shape with prose to its left — e.g. a bullet
// quoting an inline-code error message like "runnerGroups[0]: ..." — can
// never be reached by a chain and no longer freezes its neighbor (issue
// #37).
func inLinkRefDefZone(source []byte, trimmed []string, contentStart int, facts map[int]lineFacts) SkipReason {
	for _, t := range trimmed {
		if nonCaretDefShapeRE.MatchString(t) || bareCaretOpenerRE.MatchString(t) || orphanDefCloserRE.MatchString(t) {
			return SkipLinkRefDefShape
		}
	}
	// There is deliberately NO footnote-body exemption from the neighbor
	// checks below (there was one; issue #41 removed it). A paragraph
	// whose own first line opens "[^label]:" is a footnote body and must
	// keep reflowing — but that is already guaranteed by the facts
	// themselves: a COMPLETE footnote-body line ("[^1]: body text", the
	// line above in the ordinary back-to-back layout) matches none of
	// them (defChainStartRE and defShapeAnywhereRE exclude caret labels;
	// bareCaretOpenerRE requires a bare colon-at-EOL opener;
	// orphanDefCloserRE requires no "[" before the "]:"), so nothing
	// fires and back-to-back footnotes stay eligible with no special
	// case. The evidence that DOES fire is machinery hazardous to any
	// paragraph below it, footnote-shaped or not: a titleless "[label]:"
	// or bare "[^label]:" line completes its destination from the line
	// below, so joining that paragraph's lines changes whether the
	// definition forms at all — found by FuzzFormat on
	// " [0]:\n [^0]:0\n\"\"0" (non-caret, issue #41) and
	// " [^0]:\n [^0]:0\n\"\"0" (bare caret opener, its sibling found
	// four minutes into the post-fix soak), both reflowed under the old
	// blanket exemption. Every evidence line is itself frozen (the
	// contains check freezes def-shaped and bare-caret-opener paragraph
	// lines; complete defs never reach reflow as paragraphs), so keying
	// on one is verdict-stable.
	ls := lineStart(source, contentStart)
	if ls == 0 {
		return SkipNone
	}
	if facts[ls].defAbove {
		// A def-shaped line anywhere above, in this line's contiguous
		// non-blank run — not just on the immediately preceding line: a
		// definition's title scan can reach the paragraph across any
		// number of intervening machinery lines. See scanLineFacts.
		return SkipLinkRefDefNeighbor
	}
	prevStart := lineStart(source, ls-1)
	prevLine := bytes.TrimRight(source[prevStart:ls], "\r\n")
	pf := facts[prevStart]
	if pf.chainStart || pf.bareCaretOpener || pf.orphanCloser {
		// orphanDefCloserRE on the neighbor too, not just this
		// paragraph's own lines: when a multi-line label's "]:" tail is
		// the line directly ABOVE, this paragraph's own first line is
		// the definition's absorbed destination, and joining it with
		// what follows invalidates the whole definition — found by
		// FuzzFormat on "[\]\n]:\n0\n\"\"0" (seed 0767a5cc905fe38b),
		// the mirror of seed 0df31d8ad2438ba6's contains-side case.
		return SkipLinkRefDefNeighbor
	}
	if len(trimmed) > 0 {
		window := string(prevLine) + "\n" + trimmed[0]
		for _, m := range defShapeAnywhereRE.FindAllStringIndex(window, -1) {
			if m[1] > len(prevLine) {
				// Spans the boundary, or sits wholly inside trimmed[0]:
				// fires unconditionally, as before. (A match wholly
				// inside trimmed[0] is already caught by the contains
				// check (a) above — same pattern applied to the same
				// line — so firing here too is harmless parity.)
				return SkipLinkRefDefNeighbor
			}
			// Wholly inside prevLine: a definition chain can only reach
			// this shape if it could have started at prevLine's own
			// first content, i.e. everything to the shape's left is
			// container-prefix-plausible. Prose to its left (the
			// runnerGroups[0] mid-sentence case) means no chain can
			// reach it, and any chain reaching from further above is
			// already covered by the defAbove check earlier in this
			// function.
			if plausibleDefPrefix(prevLine[:m[0]]) {
				return SkipLinkRefDefNeighbor
			}
		}
	}
	return SkipNone
}

// plausibleDefPrefix reports whether prefix — the bytes of a line to the
// left of a def-shaped match — could plausibly be a container prefix
// (blockquote/list markers and padding) rather than prose, for
// inLinkRefDefZone's (c). It is deliberately loose in the freezing
// direction: it accepts every byte in the set space, tab, '>', '-', '+',
// '*', '0'-'9', '.', ')' , so a non-marker run like "3.5) " still counts
// as plausible and the paragraph below stays frozen — conservatism here
// is free. Only a clearly-prose prefix (letters, quotes, backticks, and
// the like) makes it return false and lets the paragraph unfreeze.
func plausibleDefPrefix(prefix []byte) bool {
	for _, b := range prefix {
		switch {
		case b == ' ' || b == '\t' || b == '>' || b == '-' || b == '+' || b == '*' || b == '.' || b == ')':
		case b >= '0' && b <= '9':
		default:
			return false
		}
	}
	return true
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

// hasRawHTMLDeclOpener reports whether any masked line contains a "<?"
// or "<!" raw-HTML opener — see its call site in build for why either
// triggers a whole-paragraph skip. It runs on code-span-masked lines so
// an opener inside inline code (`<?php ...`, the common legitimate way
// prose mentions one) never costs the paragraph its reflow.
func hasRawHTMLDeclOpener(maskedLines []string) bool {
	for _, l := range maskedLines {
		if strings.Contains(l, "<?") || strings.Contains(l, "<!") {
			return true
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
// Spans come from segment.CodeSpans, which parses the joined lines
// through the same goldmark configuration (including linkify) the
// document render uses (docs/design.md, "No-break spans: ask goldmark,
// not a hand grammar"): a backtick inside a GFM bare URL is destination
// content there, never a delimiter, exactly as goldmark sees it, so there
// is no blind spot to guard against and no ordering dependency on a
// separate skip check. A backtick run with no matching closer opens no
// span and is not reported, so masking leaves it untouched — that is what
// keeps an unclosed "`unclosed [bracket" arming the guard, where the
// bracket is ordinary prose and the paragraph really is hazardous.
//
// Each reported span is delimiter-inclusive (the opening backtick run
// through the closing run); only the interior is masked, so both
// delimiter runs keep their identity for any caller that cares. Backtick
// bytes and '\n' are never overwritten, preserving the geometry the
// delimiter scans above depend on.
func maskCodeSpans(trimmedLines []string) []string {
	joined := strings.Join(trimmedLines, "\n")
	b := []byte(joined)
	out := make([]byte, len(b))
	copy(out, b)
	for _, sp := range segment.CodeSpans(joined) {
		for k := sp.Start; k < sp.End; k++ {
			if out[k] != '`' && out[k] != '\n' {
				out[k] = 'x'
			}
		}
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
// line: "!!!" or "???"/"???+", a whitespace run, then at least one more
// byte. Everything it reads is a bounded prefix; nothing depends on where
// the line ends, which is the property that matters, since reflow is
// exactly what moves a line ending. An end-anchored, type-word-requiring
// version let a wrap cut turn a paragraph's own break point into a marker
// match on the next pass (issue #51).
//
// Deliberately looser than Python-Markdown's own grammar in what may
// follow the whitespace: it accepts any class word, so Material for
// MkDocs's inline modifiers ("!!! note inline end \"Title\"") and
// non-alphabetic types both match. Requiring a specific shape there is
// what made the old pattern miss real admonitions.
//
// The trailing "\S" is load-bearing, not decoration. Without it, shapes
// that are not admonitions at all — "!!!!x", "!!!bang", "???01010", a
// bare "!!!" — claim the indented block below them. When that block is an
// indented code block, treating it as admonition prose reflows and
// escapes its contents, which changes what the document renders to.
//
// Callers gate this on the mkdocs dialect (see admonitionBodies and
// build's use of it); under other dialects a line starting with "!!!" is
// ordinary prose.
var admonitionMarkerRE = regexp.MustCompile(`^(?:!{3}|\?{3}\+?)[ \t]+\S`)

// admonitionMarkerPunctRE matches the opening punctuation of an admonition
// marker without requiring the class word admonitionMarkerRE demands.
//
// A paragraph's first line decides whether the paragraph is the no-blank-
// line admonition form, and reflow can rewrite that line: joining the next
// source line onto a bare "!!!" produces "!!! word", which the next pass
// reads as a marker and indents the body under — output that never settles.
// The first line's opening bytes are the one part of a paragraph reflow
// cannot move, so pinning the line whenever it opens with the punctuation
// keeps the marker verdict identical on every pass.
var admonitionMarkerPunctRE = regexp.MustCompile(`^(?:!{3}|\?{3}\+?)`)

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
		v := seg.Value(source)
		// A line indented past the admonition's own 4 spaces keeps that
		// extra whitespace as code-block content, and reflow re-emits
		// every body line at exactly the 4-space indent — so reflowing
		// such a line deletes bytes that are content to the CommonMark
		// reading of the block. Callout bodies worth reflowing sit flush
		// at 4 spaces; anything deeper is left alone.
		if len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
			return nil
		}
		t := bytes.TrimLeft(v, " \t")
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
		Node:            cb,
		Start:           first.Start,
		End:             last.Stop,
		ContPrefix:      contPrefix,
		Boundary:        make([]bool, lines.Len()),
		EscapeIsContent: true,
	}}
}
