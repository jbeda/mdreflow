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
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"
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
	var out []Paragraph
	fmEnd := frontMatterEnd(source)
	collect(doc, source, false, 0, fmEnd, &out)
	return out
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
// nesting). continuationPrefix's byte-width-preserving transform is
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
func collect(n ast.Node, source []byte, inBlockquote bool, depth int, fmEnd int, out *[]Paragraph) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
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
			// Same adjacency reasoning as precededByTable, for a different
			// construct: a real *ast.LinkReferenceDefinition sibling
			// directly before this paragraph can have consumed its
			// label/destination/title across the boundary between the two
			// — goldmark's own reference-definition scan runs ahead over
			// raw lines independent of where the resulting AST nodes end
			// up, and a multi-line label can close on what becomes this
			// paragraph's own first line (see build's precededByLinkRefDef
			// handling for the concrete shape). Whether that definition
			// still validates on reparse depends on this paragraph's own
			// first line staying exactly as wide as it started, so
			// reflowing it at all is unsafe.
			//
			// This only matters when the definition's own match actually
			// reached past its own opening physical line — the common
			// case, a self-complete one-liner like "[foo]: /url" (label,
			// destination, and an absent-or-closed title all on that one
			// line), consumes nothing from what follows, so the next
			// paragraph is exactly as safe to reflow as if any other block
			// preceded it. isSelfCompleteLinkRefDef checks this directly
			// (does this node's own recorded opening line, reparsed in
			// isolation, still form a complete definition with nothing left
			// over?) rather than assuming every LinkReferenceDefinition
			// sibling is equally dangerous — found necessary by a driver
			// review regression: the original, unconditional version of
			// this check passed through *every* paragraph directly after
			// *any* link reference definition, silently un-reflowing the
			// common "[foo]: /url\nlong prose..." shape that v0.1.2
			// correctly reflowed.
			//
			// Unlike precededByTable, this does not gate on
			// c.HasBlankPreviousLines(): confirmed directly against
			// goldmark that a Paragraph immediately following a
			// LinkReferenceDefinition sibling always reports
			// HasBlankPreviousLines() == true, whether or not a real blank
			// line actually separates them in the source — apparently an
			// artifact of the definition's own raw-line trial scan, which
			// does not update the normal blank-line bookkeeping the way an
			// ordinary block continuation does. build does its own raw-byte
			// blank-line check instead (precededByBlankLine), which is not
			// fooled by this.
			precededByLinkRefDef := false
			if prev := c.PreviousSibling(); prev != nil && prev.Kind() == ast.KindLinkReferenceDefinition &&
				(!isSelfCompleteLinkRefDef(source, prev) || lrdReachesInto(source, prev, c)) {
				precededByLinkRefDef = true
			}
			if pp, skip := build(c, source, inBlockquote, fmEnd, precededByTable, precededByLinkRefDef); !skip {
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
		collect(c, source, childInBQ, childDepth, fmEnd, out)
	}
}

// build derives a Paragraph from p (an *ast.Paragraph or *ast.TextBlock),
// or reports skip == true if a whole-node dialect rule matches p's text.
// fmEnd is the document's front-matter end offset (frontMatterEnd(source),
// or -1 if source has no front matter) — see its use below. precededByTable
// is true when p's immediately preceding sibling in the AST is a GFM
// *ast.Table — see its use below for why. precededByLinkRefDef is true when
// p's immediately preceding sibling is a real *ast.LinkReferenceDefinition
// — see its use below for why.
func build(p ast.Node, source []byte, inBlockquote bool, fmEnd int, precededByTable, precededByLinkRefDef bool) (pp Paragraph, skip bool) {
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
	if hasPossibleLinkRefDefOpener(trimmed) || hasUnbalancedBracket(trimmed) || hasUnbalancedParen(trimmed) {
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
	start0 := lines.At(0).Start
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
	if precededByLinkRefDef && !precededByBlankLine(source, start0) {
		// Fuzz-found idempotency hazard in the same family as
		// precededByTable above, but for link reference definitions:
		// goldmark's own reference-definition scan can consume a label,
		// destination, and/or title that spans past its own recorded
		// Lines() — which only ever reports the definition's own opening
		// physical line, for source-position purposes, even when the
		// actual match reached further — onto what the AST then also hands
		// back as part of *this* paragraph's own first line(s). Confirmed
		// directly (not assumed): source "[\\]\n]:0\n\"\"0" parses as an
		// *ast.LinkReferenceDefinition (label "\]\n", matched across the
		// line break by the escaped "]") immediately followed by this
		// Paragraph, whose own first line is "]:0" — the same "]:0" text
		// the definition's destination scan also consumed to close itself.
		// Reflowing this paragraph's first line at all (even just joining
		// it with what follows, unconditionally true under ModePara) can
		// append trailing content after the position the definition's
		// destination ends at; per the same "[0]:0 !" disqualification
		// rule documented on reflow.linkRefDefOpenerRE, trailing content on
		// that same physical line invalidates the whole definition on
		// reparse — which then folds the previously invisible "[\\]" line
		// back into this paragraph's own visible prose, an idempotency
		// break (verdict flip), not just a content change: found by
		// FuzzFormat on exactly that input. hasPossibleLinkRefDefOpener and
		// hasUnbalancedBracket cannot see this hazard at all, since the "["
		// that opens the label sits entirely outside this paragraph's own
		// Lines() — it belongs to the preceding sibling node's own
		// physical line. Skipping the whole paragraph (the same safe,
		// general answer precededByTable already uses for the analogous
		// table case) guarantees this paragraph's own first line — the one
		// the definition's own scan may depend on — never changes shape.
		return Paragraph{}, true
	}
	if precededByBareLinkRefDefLine(source, start0) {
		// Fuzz-found render-preservation hazard in the same family as
		// this file's other link-reference-definition defenses
		// (hasPossibleLinkRefDefOpener, hasUnbalancedBracket) but
		// invisible to all of them: those checks look at *this
		// paragraph's own* lines, but a successfully-consumed link
		// reference definition is removed from the AST entirely — it
		// becomes a sibling ast.LinkReferenceDefinition node, never part
		// of any Paragraph's own Lines() — so a bare "[label]:" opener
		// immediately before this paragraph is completely invisible to
		// per-paragraph content checks. Found by FuzzFormat on
		// " [0]:\n0\n\"\"0": the leading space still lets "[0]:" form a
		// real definition (label "0", destination "0" from the next
		// line), leaving this package's own paragraph as just "0\n\"\"0"
		// — reflowing that paragraph down to "0 \"\"0" makes the
		// *destination* no longer stand alone on its own line, which
		// (per the same disqualification rule the "[0]:0 !" family
		// already established: trailing content directly after a
		// destination on the *same* line disqualifies the whole
		// definition) makes the preceding "[0]:" fail to form a
		// definition at all on reparse, turning it from invisible
		// metadata into visible text. Skipping any paragraph directly
		// preceded by a bare, unqualified "[label]:" line — regardless
		// of whether *this* paragraph's own content looks dangerous —
		// avoids ever changing a destination's own line shape.
		return Paragraph{}, true
	}

	boundary := make([]bool, n)
	for i, t := range trimmed {
		boundary[i] = isBoundaryLine(t, inBlockquote)
	}

	end := lines.At(n - 1).Stop
	return Paragraph{
		Node:       p,
		Start:      start0,
		End:        end,
		ContPrefix: continuationPrefix(source, start0),
		Boundary:   boundary,
	}, false
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
	out := make([]byte, len(prefix))
	for i, b := range prefix {
		if b == '>' {
			out[i] = '>'
		} else {
			out[i] = ' '
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

// bareLinkRefDefLineRE matches a line consisting solely of a
// link-reference-definition-shaped "[label]:" opener (optionally preceded
// by spaces/tabs and blockquote ">" markers, e.g. a definition nested
// inside a blockquote — found necessary by FuzzFormat on ">[0]:\n0\n\"\"0"
// after a leading-space-only version of this regex missed it) with
// nothing else after the colon.
//
// Unlike possibleLinkRefDefOpenerRE, this one does *not* exclude
// "[^label]:" (footnote-shaped) labels — deliberately, not an oversight:
// this regex only ever runs against a raw source line the *current*
// paragraph does not itself contain (see precededByBareLinkRefDefLine,
// its only caller), meaning whatever "[...]:" it matched was already
// fully consumed out of the visible AST as its own separate node. A real
// footnote definition is never invisible like that — goldmark keeps its
// "[^label]: " text as part of its own paragraph's visible content (as
// package reflow's own linkRefDefOpenerRE, and the golden fixture
// testdata/no-break-spans.md, both rely on) — so anything this function
// finds is already known to be an ordinary link reference definition, not
// a footnote, confirmed directly against goldmark (not assumed) even for
// an otherwise footnote-shaped label with a real, non-empty identifier:
// found by FuzzFormat on "[^0]:\n0\n\"\"0", where "[^0]:" — caret plus a
// real identifier, which an earlier, excluding version of this regex
// protected on the theory that it must be a footnote — turned out to
// still be consumed as a plain LinkReferenceDefinition (Label "^0"),
// confirmed by direct AST inspection.
var bareLinkRefDefLineRE = regexp.MustCompile(`^[ \t>]*\[[^\[\]][^\[\]]*\]:[ \t]*$`)

// isSelfCompleteLinkRefDef reports whether lrd (a real
// *ast.LinkReferenceDefinition, confirmed by its caller) is fully specified
// on its own recorded opening physical line — label closed, destination
// present, and title closed or absent — with nothing needed from any
// following line. Checked empirically, not by re-deriving CommonMark's
// reference-definition grammar by hand: lrd.Lines().At(0) (its own
// recorded line, content-relative — any container prefix such as a
// blockquote "> " is already excluded, the same way it is for a Paragraph's
// own Lines()) is sliced out to its own physical line end and reparsed in
// total isolation. If that reparse still yields exactly one node, itself a
// LinkReferenceDefinition, with nothing left over, the original match could
// not have needed anything past this line either — reference-definition
// recognition depends only on the candidate text itself, not on anything
// outside it, so parsing a self-sufficient prefix in isolation always
// reproduces the same verdict.
//
// This is what narrows precededByLinkRefDef's whole-paragraph skip to the
// cases that actually need it: a self-complete one-liner like
// "[foo]: /url" (or "[foo]: /url \"Title\"") consumes nothing from the
// paragraph that follows, so reflowing that paragraph is exactly as safe as
// if any other block preceded it — only a definition whose match
// genuinely reached past its own opening line (e.g. a label split across a
// line break, as in issue #11's "[\\]\n]:0") puts the following paragraph's
// first line at risk.
func isSelfCompleteLinkRefDef(source []byte, lrd ast.Node) bool {
	line := lrdOpeningLine(source, lrd)
	if line == nil {
		return false
	}
	doc := gm.New().Parser().Parse(text.NewReader(line))
	first := doc.FirstChild()
	return first != nil && first.Kind() == ast.KindLinkReferenceDefinition && first.NextSibling() == nil
}

// lrdOpeningLine returns the definition's own recorded opening line,
// sliced out to its physical line end (line ending included when present),
// or nil if the node records no lines.
func lrdOpeningLine(source []byte, lrd ast.Node) []byte {
	lines := lrd.Lines()
	if lines.Len() == 0 {
		return nil
	}
	start := lines.At(0).Start
	end := start
	for end < len(source) && source[end] != '\n' {
		end++
	}
	if end < len(source) {
		end++ // include the line ending, matching how a real line is scanned
	}
	return source[start:end]
}

// lrdReachesInto reports whether the paragraph's own first line changes
// what goldmark registers for the preceding, otherwise self-complete
// definition. isSelfCompleteLinkRefDef alone is not sufficient (its
// original premise — that recognition depends only on the candidate text —
// fails for titles specifically): a definition that is complete WITHOUT a
// title on its own line can still absorb one from the next line, and
// goldmark will do so even when trailing content makes that same line
// render as paragraph prose too. Found by FuzzFormat on
// " [0]:0\n\"00[0]\"0" (seed 609ac42cd2c93d72): the second line is
// simultaneously the definition's registered title ("00[0]") and visible
// paragraph text, so reflow touching it — typography curling its quotes,
// in the find — silently rewrites the title every "[0]" reference link
// renders with.
//
// Checked empirically against goldmark's own reference registry rather
// than re-deriving the title grammar: parse the definition's opening line
// alone, then with the paragraph's first line appended, and compare the
// registered (label, destination, title) tuples. Any difference means the
// paragraph's first line feeds the definition and the paragraph must pass
// through untouched. Unreadable inputs (no recorded lines) fail
// conservative — reaches-into, skip.
func lrdReachesInto(source []byte, lrd, para ast.Node) bool {
	defLine := lrdOpeningLine(source, lrd)
	plines := para.Lines()
	if defLine == nil || plines.Len() == 0 {
		return true
	}
	// The whole paragraph, not just its first line: a title opened on the
	// paragraph's first line can close on a later one (fuzz find
	// " [0]:0\n\"0[0]\n\"0", seed eaeb2965b3833196 — the registered title
	// spans the paragraph's first two lines), and only the full text
	// answers whether the definition absorbed anything.
	ps := plines.At(0).Start
	pe := plines.At(plines.Len() - 1).Stop
	for pe < len(source) && source[pe] != '\n' {
		pe++
	}
	paraText := source[ps:pe]

	combined := make([]byte, 0, len(defLine)+1+len(paraText))
	combined = append(combined, defLine...)
	if n := len(combined); n == 0 || combined[n-1] != '\n' {
		combined = append(combined, '\n')
	}
	combined = append(combined, paraText...)
	return !slices.Equal(registeredRefs(defLine), registeredRefs(combined))
}

// registeredRefs parses src in isolation and returns goldmark's registered
// link references as comparable label/destination/title tuples.
func registeredRefs(src []byte) []string {
	pc := parser.NewContext()
	gm.New().Parser().Parse(text.NewReader(src), parser.WithContext(pc))
	refs := pc.References()
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, string(r.Label())+"\x00"+string(r.Destination())+"\x00"+string(r.Title()))
	}
	return out
}

// precededByBlankLine reports whether the raw physical source line
// immediately before contentStart is blank (empty, or all spaces/tabs) — a
// real blank-line separator, checked directly against raw bytes rather than
// ast.Node.HasBlankPreviousLines(): see precededByLinkRefDef's call site in
// build for why that method cannot be trusted for this purpose (it always
// reports true immediately after a LinkReferenceDefinition sibling,
// blank line or not). A genuine blank line here means whatever multi-line
// scan a preceding link reference definition ran cannot have reached across
// it — CommonMark block continuation always stops at a blank line — so the
// adjacency hazard precededByLinkRefDef guards against cannot apply.
func precededByBlankLine(source []byte, contentStart int) bool {
	end := lineStart(source, contentStart)
	if end == 0 {
		return false
	}
	start := lineStart(source, end-1)
	line := bytes.TrimRight(source[start:end], "\r\n")
	return len(bytes.Trim(line, " \t")) == 0
}

// precededByBareLinkRefDefLine reports whether the raw source line
// immediately before contentStart looks like a bare "[label]:" link-
// reference-definition opener with no destination on that same line —
// see precededByBareLinkRefDefLine's call site in build for why that
// alone is reason enough to skip the following paragraph.
func precededByBareLinkRefDefLine(source []byte, contentStart int) bool {
	end := lineStart(source, contentStart)
	if end == 0 {
		return false
	}
	start := lineStart(source, end-1)
	line := bytes.TrimRight(source[start:end], "\r\n")
	return bareLinkRefDefLineRE.Match(line)
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

// possibleLinkRefDefOpenerRE matches a link-reference-definition-shaped
// "[label]:" opener anywhere in a line (not anchored to the end, unlike
// package reflow's linkRefDefOpenerRE, and not requiring anything to
// follow — see hasPossibleLinkRefDefOpener for why an unanchored,
// permissive match is the right level of caution here). It excludes a
// "[^..." bracket for the same reason as reflow.linkRefDefOpenerRE: that
// shape is a footnote definition's own legitimate first-line marker.
var possibleLinkRefDefOpenerRE = regexp.MustCompile(`\[(\^\]|[^\^\[\]][^\[\]]*\]):`)

// hasPossibleLinkRefDefOpener reports whether any of trimmedLines contains
// a link-reference-definition-shaped "[label]:" opener anywhere in it.
func hasPossibleLinkRefDefOpener(trimmedLines []string) bool {
	for _, line := range trimmedLines {
		if possibleLinkRefDefOpenerRE.MatchString(line) {
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

// hasUnbalancedParen is hasUnbalancedBracket's counterpart for "(" / ")":
// an inline link's *destination* — "[text](destination)" — can span a
// soft line break exactly the way a link label can (see
// hasUnbalancedBracket's doc comment for the label case), so an open "("
// left unclosed at the end of a line is the same class of hazard, found
// by FuzzFormat on "[](  \n\")0": mdreflow's hard-break-style
// normalization of the trailing two spaces after "[](" changed the
// destination's interior content (whitespace collapsed differently once
// the source's own line break became literal "<br>" marker text instead),
// producing a broken (and, worse, different) link on reparse instead of
// the original's link to a literal '"' character.
func hasUnbalancedParen(trimmedLines []string) bool {
	return hasUnclosedDelimiterAcrossLine(trimmedLines, '(', ')')
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
