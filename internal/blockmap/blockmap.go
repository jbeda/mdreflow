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
	"strings"

	"github.com/yuin/goldmark/ast"
	gfmast "github.com/yuin/goldmark/extension/ast"
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
	collect(doc, doc, source, false, 0, &out)
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
// any ancestor already visited, is a *ast.Blockquote. doc is the document
// root, passed through unchanged, so build can special-case a document's
// very first child (see isUnterminatedFrontMatterArtifact).
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
func collect(doc, n ast.Node, source []byte, inBlockquote bool, depth int, out *[]Paragraph) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			isDocFirstChild := n == doc && c == doc.FirstChild()
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
			if pp, skip := build(c, source, inBlockquote, isDocFirstChild, precededByTable); !skip {
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
		collect(doc, c, source, childInBQ, childDepth, out)
	}
}

// build derives a Paragraph from p (an *ast.Paragraph or *ast.TextBlock),
// or reports skip == true if a whole-node dialect rule matches p's text.
// isDocFirstChild is true when p is the very first block-level child of
// the document. precededByTable is true when p's immediately preceding
// sibling in the AST is a GFM *ast.Table — see its use below for why.
func build(p ast.Node, source []byte, inBlockquote bool, isDocFirstChild bool, precededByTable bool) (pp Paragraph, skip bool) {
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
		// same family as allBlank's control-character case, or — found by
		// FuzzFormat on "* -\n\t  \n\t0" — a side effect of goldmark-meta
		// consuming an earlier "-"-only line invisibly, leaving a
		// TextBlock whose own first line is a bare, zero-width soft-break
		// segment, combined with tab-stop padding on a later line whose
		// Value() synthesizes leading space bytes that do not exist at
		// its own raw [Start,Stop) — package reflow's join logic already
		// tolerates an empty line by dropping it (see joinClusterLines),
		// but that alone was not enough to make this exact input
		// reproduce byte-for-byte on a second pass. Skipping the whole
		// paragraph is the safe, general answer once again.
		return Paragraph{}, true
	}
	if wholeNodeSkip(trimmed) {
		return Paragraph{}, true
	}
	if hasInjectedArtifactChild(p) {
		// The direct, general fix for the whole goldmark-meta-artifact
		// family isAllDashes and isUnterminatedFrontMatterArtifact chase
		// by content shape: every fuzz find in this family (document
		// root, nested inside a tight list item, nested inside a
		// blockquote, with or without a blank line or trailing
		// whitespace nearby) shares one concrete AST signature — an
		// *ast.String child goldmark-meta injects, containing its own
		// "yaml: ..." parse-error text, directly inside the
		// Paragraph/TextBlock whose content it also silently altered.
		// Checking for that signature directly, instead of continuing to
		// guess at which surface byte pattern (a lone "-", a run of
		// them, a specific container, specific whitespace) triggers it
		// this time, is the fix that actually generalizes — found
		// necessary after FuzzFormat kept finding new shapes the
		// content-based checks missed, most recently ">-\n>#0\n>00" (a
		// blockquote, where the dash-only line is consumed leaving a
		// Paragraph whose own first line is "#0", not a dash, so
		// isAllDashes never even sees anything to match). ast.KindString
		// is a generic node kind that *could*, in principle, appear for
		// some unrelated reason in a future goldmark version or
		// extension; skipping reflow on it regardless is still always
		// safe (just conservative), matching this file's general
		// philosophy of preferring "don't touch it" over risking
		// content loss.
		return Paragraph{}, true
	}
	if isAllDashes(trimmed[0]) {
		// Fuzz-found content-loss hazard, a nested-context relative of
		// isUnterminatedFrontMatterArtifact below: goldmark-meta's block
		// parser triggers on any line consisting solely of "-"
		// characters — any *run* of them, not just a single "-" (its own
		// isSeparator test trims the line then checks every remaining
		// byte is '-'; found broadened from an earlier, single-dash-only
		// version of this check by FuzzFormat on "0) --\n   0  ": an
		// ordered list item whose own first line is "--" hit the same
		// artifact a lone "-" does) — and it turns out this fires
		// wherever such a line opens *any* block's first line, not only
		// the document's own root (which is all
		// isUnterminatedFrontMatterArtifact, below, actually guards).
		// Originally found by FuzzFormat on "* -\n\t  \n\t0": a tight
		// list item whose own first line (after its "* " marker, which
		// is container prefix and not part of Lines() content) is just
		// "-" triggered the same injected-parse-error / silently-
		// altered-content artifact inside a nested TextBlock, corrupting
		// output (confirmed as a real idempotency break, not merely a
		// cosmetic render difference this package's fuzz-harness
		// exceptions would normally absorb). Skipping any paragraph/
		// text-block whose own first line is nothing but dashes avoids
		// ever reflowing content goldmark-meta might reinterpret, at the
		// cost of not reflowing the vanishingly rare legitimate prose
		// that opens with a bare dash run and nothing else on its first
		// line.
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
	if isDocFirstChild && isUnterminatedFrontMatterArtifact(source, start0) {
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

// isUnterminatedFrontMatterArtifact reports whether source's very first
// line consists solely of '-' characters (goldmark-meta's own separator
// test: any run of dashes, not just "---", triggers it — see
// goldmark-meta's isSeparator).
//
// goldmark-meta's block parser triggers on such a line and then reads
// every following line, up to a matching closing separator or EOF, as
// attempted YAML. If no closing separator ever appears (malformed input —
// real front matter always closes), it swallows the rest of the document
// and either (a) removes it from the AST if the collected text happens to
// parse as YAML, or (b) leaves a fallback *ast.TextBlock with the original
// content when it doesn't. Case (b) is a fuzz-discovered hazard: since
// mdreflow would otherwise treat that fallback TextBlock as ordinary
// reflow-eligible prose, joining its lines can change whether the result
// happens to parse as valid YAML — flipping a document from "malformed
// front matter, content visible" to "well-formed front matter, content
// silently removed" purely as a side effect of reflow, which is a real
// (if exceedingly narrow — no real front-matter convention uses an
// unterminated dash fence) content-preservation violation. Treating any
// document-first-child paragraph/text-block preceded by a dash-only line
// as a whole-node skip avoids it entirely, mirroring how the TOML rule
// already treats a front-matter-shaped block as non-prose. See the M2
// report for the fuzz input that found this ("-\n0: \n0").
//
// contentStart must be the candidate paragraph's own first-line content
// start offset. This is required, not just "is it the document's first
// child": a *successfully* parsed "---"-delimited YAML front matter block
// is entirely removed from the AST by goldmark-meta, so the real prose
// paragraph that follows it also becomes the document's first remaining
// child — and would otherwise be misidentified as this same artifact,
// since its preceding line (now gone from the tree but still present in
// source) is also dash-only. The distinguishing fact is proximity: the
// fallback TextBlock in the failure case starts capturing on the line
// immediately after the dash line, with nothing removed in between, so
// contentStart must equal the byte right after that line's newline.
func isUnterminatedFrontMatterArtifact(source []byte, contentStart int) bool {
	nl := bytes.IndexByte(source, '\n')
	if nl < 0 || contentStart != nl+1 {
		return false
	}
	t := strings.TrimSpace(string(source[:nl]))
	if t == "" {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != '-' {
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
// isAllDashes reports whether s is non-empty and consists solely of '-'
// characters — goldmark-meta's own isSeparator test, which any such line
// satisfies regardless of length (a single "-" and a run like "---" are
// equally a match).
// hasInjectedArtifactChild reports whether p has any descendant of kind
// ast.KindString — see hasInjectedArtifactChild's call site in build for
// what this detects and why.
func hasInjectedArtifactChild(p ast.Node) bool {
	for c := p.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Kind() == ast.KindString || hasInjectedArtifactChild(c) {
			return true
		}
	}
	return false
}

func isAllDashes(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			return false
		}
	}
	return true
}

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
