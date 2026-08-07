package mdreflow_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jbeda/mdreflow"
)

// goldmarkMetaErrorComment matches the HTML comment goldmark-meta injects
// (in various message shapes, e.g. "yaml: unmarshal errors: ..." or
// "yaml: line N: could not find expected ':'") when it fails to parse what
// it thinks is a YAML front-matter block. It is an upstream artifact, not
// mdreflow output: goldmark-meta's Trigger fires on any line consisting
// solely of '-' characters (including a bare "-", not just "---") at the
// very start of a document, and if no closing separator line ever
// appears, it swallows the rest of the document as attempted front matter
// and reports a parse error whose text embeds a raw source line number.
// Any edit that shifts line numbers — including a reflow that changes
// line counts, which is mdreflow's entire purpose — changes that embedded
// number, so this comment's exact text is inherently unstable across
// edits and unrelated to reflow correctness. It is stripped before the
// render-preservation comparison below; see the M2 report for the fuzz
// finds that surfaced this (inputs "-\n\n0" and "-\n0: \n0").
var goldmarkMetaErrorComment = regexp.MustCompile(`(?s)<!-- yaml:.*?-->`)

// hasHardBreakAdjacentDelimiter conservatively reports whether src has a
// line ending, or a following line starting (allowing a little slack for
// a blockquote/list container prefix), with a CommonMark/GFM emphasis
// delimiter character ("*", "_", "~").
//
// This gates a documented exception to the render-preservation check
// below: CommonMark's emphasis "flanking" rule — whether a delimiter run
// opens or closes emphasis — depends in part on whether it is adjacent to
// whitespace, and reflow's line-based architecture cannot always keep
// that whitespace's *kind* stable. It first surfaced narrowly, adjacent to
// a hard break, where a hard break counts as whitespace-equivalent for
// flanking when it is a real line-break node but not when it is
// normalized to literal marker text (e.g. "<br>") — see attachMarker's
// doc comment for the two *contradictory* fuzz finds ("!*  \n0*" and
// "*\\\n0*") that showed a local, per-character heuristic fix trades one
// bug for another rather than solving it. It turned out to be broader
// than just hard breaks: an *ordinary* soft line break can also flank
// differently than the literal single space mdreflow's own line-joining
// replaces it with — found by FuzzFormat on ">*0\n>*" (no hard break at
// all, plain paragraph continuation inside a blockquote): "*0\n*" forms
// real emphasis, but the byte-identical-once-rendered "*0 *" does not.
// Precisely replicating CommonMark's flanking algorithm to predict every
// case this can flip is out of scope. A source-level render-check skip
// (this function), rather than post-hoc HTML surgery, is the honest
// choice: an earlier attempt tried stripping <em>/<strong>/<del> tags
// from the rendered HTML before comparing, but emphasis markup consumes
// its delimiter characters from the rendered text entirely rather than
// just wrapping them, so the two sides were never actually comparable
// that way.
//
// This is conservative on purpose (it flags more than the exact narrow
// set of inputs that actually hit the flanking-flip, and the "allowing a
// little slack for a container prefix" check below is a blunt fixed-width
// scan, not real prefix parsing): every other guarantee this harness
// checks for these inputs — no panic, idempotency, structural correctness
// via escapeBlockInterrupt, byte-for-byte pass-through of everything
// outside reflowed prose — is unaffected and still fully enforced; only
// the render-preservation assertion is skipped.
func hasHardBreakAdjacentDelimiter(src []byte) bool {
	lines := bytes.Split(src, []byte("\n"))
	isDelim := func(b byte) bool { return b == '*' || b == '_' || b == '~' }
	for i, line := range lines {
		if i == len(lines)-1 {
			break // nothing joins across the source's last line
		}
		// A trailing "\r" (present when src uses CRLF line endings,
		// since splitting only on "\n" leaves it attached to the
		// previous line) is stripped first so it can't hide a line's
		// real trailing character from every check below — found by
		// FuzzFormat on "*0!**  \r\n0", where the CR sitting after the
		// two hard-break spaces (themselves after the trailing "*") hid
		// that trailing "*" from an unstripped version of this check.
		line = bytes.TrimSuffix(line, []byte("\r"))
		// A trailing backslash run (checked before trimming it away
		// below) is included too: whether a backslash-escape sequence
		// right after a joined boundary gets processed at all can depend
		// on whether that boundary was a real newline or the literal
		// space mdreflow's line-joining replaces it with — the same
		// underlying "newline and space are not always interchangeable
		// for goldmark's inline parser" issue as the emphasis-flanking
		// case above, just a different symptom of it. Found by
		// FuzzFormat on "\\\\\\\n\\!": the source's "\!" right after the
		// line's own newline stays a literal, unprocessed "\!" on
		// render, but the same bytes right after a plain space have
		// their escape processed into a bare "!".
		if bytes.HasSuffix(line, []byte("\\")) {
			return true
		}
		trimmed := bytes.TrimRight(line, " \t\\")
		if len(trimmed) > 0 && isDelim(trimmed[len(trimmed)-1]) {
			return true
		}
		// A container prefix (blockquote ">", list padding, or — as the
		// fuzzer kept demonstrating by growing one past any fixed window
		// this checked — arbitrary junk bytes a malformed nesting
		// attempt produces) can sit between the start of the next line
		// and its real content, of unbounded width; scan the whole line
		// rather than capping how far in to look.
		next := lines[i+1]
		if bytes.ContainsAny(next, "*_~") {
			return true
		}
		if len(next) > 0 && next[0] == '\\' {
			return true
		}
	}
	return false
}

// hasMultilineCodeSpanCandidate conservatively reports whether src has a
// non-last line with an odd number of backtick characters — a plausible
// inline code span opened on that line and continuing onto the next
// (see insideCodeSpanAfterLine in package reflow, which mdreflow's own
// join logic uses to protect exactly this case).
//
// This gates a second narrow, documented render-preservation exception,
// found on the CRLF input "`\r\n0 `": goldmark applies CommonMark's code-
// span "strip one edge space if both edges are space and the content
// isn't all spaces" rule differently depending on whether the interior
// whitespace came from a literal "\r\n" line ending or from equivalent
// literal space characters mdreflow reconstructs after collapsing CRLF to
// LF — confirmed directly against goldmark (not assumed): "`\r\n0 `"
// keeps its edge spaces on render, while both "`\n0 `" and "` 0 `" get
// them stripped, even though CommonMark's own spec says a line ending
// converts to a space with no special CRLF case. This is a goldmark
// implementation nuance mdreflow's line-based architecture — which
// deliberately does not preserve the LF/CRLF distinction inside a
// reconstructed span, since CommonMark itself says not to — cannot
// reproduce, and is far too narrow (a code span spanning a paragraph's
// own line break, in a CRLF file) to be worth preserving that
// distinction for. As with hasHardBreakAdjacentDelimiter, only the
// render-preservation assertion is skipped for matching inputs; every
// other guarantee (no panic, idempotency, structural correctness,
// byte-for-byte pass-through outside reflowed prose) still applies.
func hasMultilineCodeSpanCandidate(src []byte) bool {
	lines := bytes.Split(src, []byte("\n"))
	for i, line := range lines {
		if i == len(lines)-1 {
			break
		}
		if bytes.Count(line, []byte("`"))%2 == 1 {
			return true
		}
	}
	return false
}

// linkRefDefShapedLine matches a line that could plausibly be, or become,
// a link-reference-definition or footnote-definition opener: "[" then
// non-"]" content then "]:". This deliberately does not try to replicate
// reflow.linkRefDefOpenerRE's precise end-anchored "is this exactly
// dangerous" check — it is a much cruder, broader net for the gate below.
var linkRefDefShapedLine = regexp.MustCompile(`^\[[^\]]*\]:`)

// hasLinkRefDefCollisionRisk conservatively reports whether src has two or
// more lines shaped like a link-reference-definition or footnote-
// definition opener.
//
// This gates a third narrow, documented render-preservation exception in
// the same family as hasHardBreakAdjacentDelimiter and
// hasMultilineCodeSpanCandidate: package reflow's escapeBlockInterrupt
// must sometimes backslash-escape a "[label]:"-shaped line reflow itself
// produced (see linkRefDefOpenerRE's doc comment) to stop it from being
// swallowed as an accidental new definition — necessary, and it is a
// structural-correctness fix, not a style choice. But the same leading
// "[" is also what CommonMark's *inline* shortcut-reference-link syntax
// uses, and escaping it defeats that too — so when an earlier definition
// for the same label already exists elsewhere in the document (which, in
// a small fuzz input, a second def-shaped line makes plausible), the
// escape can turn a working "[label]" reference link's resolution off,
// changing rendered content beyond just the definition it was protecting
// against. Found by FuzzFormat on "[0]:0\n[0]:! 0": reflow's escape of
// the (correctly identified as structurally dangerous) second line also
// broke the first line's real "[0]" reference elsewhere in the source.
// Resolving this precisely would require knowing, while escaping, whether
// the label is already defined and referenced elsewhere — genuinely
// document-wide information package reflow's per-paragraph, per-line
// architecture does not have and is not worth threading through for a
// combination this narrow (an accidental-definition collision *and* an
// existing working reference to the same label). As with the other two
// exceptions, only the render-preservation assertion is skipped; no panic,
// idempotency, and structural-correctness (no content silently swallowed)
// are still fully enforced.
// A single occurrence is also now enough to trigger this (originally it
// required two, on the theory that a *collision* specifically needed an
// existing definition to collide with): a further fuzz find,
// "[0]:\n0\n\"\"0", showed even one "[label]:"-shaped opener can produce a
// render mismatch through package blockmap's own whole-paragraph-skip
// safety net (hasPossibleLinkRefDefOpener) — goldmark's real link-
// reference-definition parser backtracks across a failed multi-line
// destination/title attempt in a way blockmap's necessarily-conservative,
// non-backtracking skip doesn't replicate exactly, so passing the whole
// paragraph through can itself land on a different split point than
// goldmark's own (successful, partial) parse would have. This remains the
// right trade (structural safety over exact fidelity) for the reasons in
// the rest of this comment.
func hasLinkRefDefCollisionRisk(src []byte) bool {
	lines := bytes.Split(src, []byte("\n"))
	count := 0
	for _, line := range lines {
		if linkRefDefShapedLine.Match(line) {
			count++
			if count >= 1 {
				return true
			}
		}
	}
	return false
}

// hasIrregularCRRun reports whether src has a bare '\r' anywhere: one not
// immediately followed by '\n' (CommonMark's own definition of a "bare CR"
// line ending — a real, valid, but rare-in-practice spelling — see below),
// or two or more consecutive '\r' bytes (which nothing normal ever
// produces at all, bare-CR or otherwise).
//
// This gates a fourth narrow, documented render-preservation exception:
// CommonMark recognizes three line-ending spellings ("\n", "\r\n", and a
// bare "\r"), and mdreflow's own stripLineEnding (package reflow) strips
// any run of them uniformly, confirmed necessary by an earlier, real
// FuzzFormat-found idempotency bug (a single "\r\n"-only strip left a
// stray literal "\r" byte behind for a mixed-ending input). But goldmark's
// *own* handling of a bare CR line ending has turned out, across more than
// one fuzz find, not to always match how it treats an "\n" or "\r\n" line
// ending in the same position:
//
//   - "\\\r\r\n0" (a backslash, then two bare-CR-style endings run
//     together — content no real editor or tool ever produces) renders
//     its backslash as literal text, not a hard break, while "\\\n0" or
//     "\\\r\n0" do trigger one.
//   - "[0]:\r! 0" (a single, otherwise ordinary paragraph whose only
//     line ending is a bare "\r" mid-line) does not parse as a link
//     reference definition before reflow touches it at all — matching
//     "[0]: ! 0" or "[0]:\n! 0", which also don't — yet mdreflow's own
//     reflow of it (sentence-splitting at "! 0", same as it would for a
//     "\n"-separated equivalent) produced output that *does* get
//     swallowed as one, a content-loss bug distinct from (and this
//     package's blockmap.hasUnbalancedBracket does not cover) the
//     multi-line-label class that function does defend against.
//
// Reproducing goldmark's exact internal handling of bare-CR line endings
// well enough to know exactly when it diverges — content no real Markdown
// document contains, since every real editor or tool emits one consistent
// line-ending spelling throughout a file — is out of scope. As with the
// other exceptions, only the render-preservation assertion is skipped for
// matching inputs.
func hasIrregularCRRun(src []byte) bool {
	for i := 0; i < len(src); i++ {
		if src[i] != '\r' {
			continue
		}
		if i+1 >= len(src) || src[i+1] != '\n' {
			return true // bare CR: not "\r\n"
		}
	}
	return false
}

// hasSplitTaskListMarker conservatively reports whether src has a line
// ending in "[" with the following line starting in "]".
//
// This gates a fifth narrow, documented render-preservation exception,
// the same underlying family as hasHardBreakAdjacentDelimiter and
// hasMultilineCodeSpanCandidate: a GFM task-list checkbox marker
// ("[ ]"/"[x]" as the very first inline content of a list item) must be
// complete on one line to be recognized; found by FuzzFormat on "* [\n]"
// (a list item whose "[" and "]" started on separate lines): mdreflow's
// line-joining put them on the same line, turning inert literal text into
// an actual rendered `<input type="checkbox">`. As with the others, only
// the render-preservation assertion is skipped for matching inputs.
func hasSplitTaskListMarker(src []byte) bool {
	lines := bytes.Split(src, []byte("\n"))
	for i, line := range lines {
		if i == len(lines)-1 {
			break
		}
		if bytes.HasSuffix(bytes.TrimRight(line, " \t"), []byte("[")) {
			next := bytes.TrimLeft(lines[i+1], " \t")
			if bytes.HasPrefix(next, []byte("]")) {
				return true
			}
		}
	}
	return false
}

// hasMultilineLinkLabelRisk conservatively (and very broadly — see below)
// reports whether src might contain a link reference definition whose
// label spans a soft line break.
//
// A sixth, and by far the broadest, exception in the same family as
// hasLinkRefDefCollisionRisk: a CommonMark link label can itself contain
// a soft line break (labels may run up to 999 characters, newlines
// included), so package reflow's linkRefDefOpenerRE — which only ever
// looks at one already-fully-formed output *line* at a time — cannot see
// a label an unclosed "[" on one line and a closing "]:destination" on a
// later line together spell out, even after mdreflow's own escaping of
// the *later* line's "[" (escaping only stops that "[" from opening a
// *new* label — it does nothing about the earlier, still-open one, since
// an escaped "[" is still valid, literal content *inside* an
// already-open label). Found by FuzzFormat on "[! [0]:0", which does not
// parse as anything before reflow (no label ever closes) but produces a
// genuine multi-line link reference definition once sentence-splitting
// puts "[!" and "[0]:0" on separate lines — swallowing the *entire
// document* into an invisible definition, confirmed by "after" rendering
// as nothing at all, not merely losing the definition-shaped fragment.
// Properly defending this would require tracking bracket-nesting state
// across an entire paragraph before ever deciding where sentence breaks
// may land (this package's own no-break-span filtering does exactly that
// for inline code spans and links, but only within a single already-
// joined cluster, not across the boundary-scanning pass that decides
// hard-break clusters in the first place) — a substantially larger change
// than fits the M2 budget for a construct this rare. Any "[" not matched
// by a "]" on the same line, anywhere in the document, triggers this
// exception; it is deliberately broader than the exact risk (label
// content that later contributors a real destination) to stay simple and
// safe, at the cost of also skipping the render check for some inputs
// that would have been fine.
func hasMultilineLinkLabelRisk(src []byte) bool {
	for _, line := range bytes.Split(src, []byte("\n")) {
		if bytes.Count(line, []byte("[")) > bytes.Count(line, []byte("]")) {
			return true
		}
	}
	return false
}

// hasTableAdjacentSetextLine conservatively reports whether src contains
// both a GFM table delimiter row (any line consisting solely of "-", ":",
// "|", spaces, and tabs, with at least one "-", is enough for this crude
// check — a real delimiter row's exact shape varies, and does not even
// need a "|" at all for a single-column table: found by FuzzFormat trying
// "|-", "-|", and finally ":-" with no pipe whatsoever) and a line
// consisting solely of "=" characters.
//
// This gates a seventh narrow, documented render-preservation exception:
// package reflow's escapeBlockInterrupt must backslash-escape a bare "="
// line (a CommonMark setext level-1-heading underline) wherever reflow
// produces one, since it retroactively turns whatever precedes it into a
// heading — necessary, and correct, when that "wherever" is an ordinary
// paragraph (found by FuzzFormat on "0  \n    =": a 4-space-indented "="
// that CommonMark's setext rule, capped at 3, disqualified in the
// original became a real heading once reflow's line-joining removed the
// indentation). But the same escape, applied without knowing what
// precedes a given line, has a side effect when what precedes it is a GFM
// table instead: an unescaped "=" line right after a table apparently
// gets excluded from continuing the table specifically *because* it looks
// like a setext underline (falling through to become a new, separate
// paragraph instead), while the escaped "\=" no longer looks like one, so
// the table extension happily consumes it as one more row — found by
// FuzzFormat on "0\n|-\n=". Distinguishing "the line before this one is a
// Paragraph" from "... is a Table" is not something escapeBlockInterrupt,
// which only ever sees isolated line text with no AST context, can do.
// As with the other exceptions, only the render-preservation assertion is
// skipped for matching inputs.
func hasTableAdjacentSetextLine(src []byte) bool {
	hasTableDelimiter := false
	hasSetextLine := false
	for _, line := range bytes.Split(src, []byte("\n")) {
		dTrimmed := bytes.Trim(line, " \t")
		if len(dTrimmed) > 0 && bytes.ContainsRune(dTrimmed, '-') && len(bytes.Trim(dTrimmed, "-:| \t")) == 0 {
			hasTableDelimiter = true
		}
		trimmed := bytes.Trim(line, " \t")
		if len(trimmed) > 0 && (len(bytes.Trim(trimmed, "=")) == 0 || len(bytes.Trim(trimmed, "-")) == 0) {
			// "-" is included alongside "=": a line of only "-" is a
			// setext level-2 underline / thematic break, the same table-
			// adjacency hazard as "=" — found by FuzzFormat on
			// "0\n|-\n--\n0".
			hasSetextLine = true
		}
	}
	return hasTableDelimiter && hasSetextLine
}

// isTableDelimiterRowShaped reports whether line is shaped like a GFM
// table delimiter row: once trimmed of leading/trailing space/tab, it
// contains only "-", ":", "|", spaces, and tabs, with at least one "-" —
// the same crude check hasTableAdjacentSetextLine already uses (see its
// doc comment for why even a bare "-|" or ":-" qualifies, and why spaces
// are allowed *within* the shape, not just at its edges: a delimiter row
// like "-- |" — dashes, a space, then a pipe — is still GFM-valid, found
// by FuzzFormat on "zJC\x00.\x007 -- |   " in a width-bounded mode, where
// an earlier, stricter version of this check (space/tab only trimmed at
// the edges, not allowed inside the shape) missed it).
func isTableDelimiterRowShaped(line []byte) bool {
	trimmed := bytes.Trim(line, " \t")
	return len(trimmed) > 0 && bytes.ContainsRune(trimmed, '-') && len(bytes.Trim(trimmed, "-:| \t")) == 0
}

// hasFreshTableAdjacency conservatively reports whether b has a
// delimiter-row-shaped line (isTableDelimiterRowShaped) immediately
// preceded by any other non-blank line.
//
// This gates a fourteenth narrow, documented render-preservation
// exception, specific to the width-bounded modes M3 adds: a source line
// with no adjacent lines at all — an ordinary, single physical paragraph
// line, nothing block-shaped about it — can still contain, *inside* its
// own prose, a comma or word boundary positioned such that a width-based
// cut lands right before a delimiter-row-shaped fragment, manufacturing a
// brand-new two-line adjacency (some text, then something that reads as a
// GFM table delimiter row) that never existed in the source at all: found
// by FuzzFormat wrapping "...CclAm) 0a10a -|" into "...CclAm)\n0a10a\n-|"
// in a width-bounded mode, where goldmark's table extension then read
// "0a10a" / "-|" as a real one-column table header and delimiter row,
// changing "<p>...0a10a -|</p>" into a "<p>...</p>" followed by a
// "<table>...". Unlike hasTableAdjacentSetextLine, this is not about an
// existing table's own escaping side effects — it is the width-based cut
// itself manufacturing the adjacency, the same underlying risk as
// hasWrapInducedBlockInterruptRisk (a cut can land at any word/clause
// boundary, unlike a sentence break) applied to GFM's table extension
// specifically rather than escapeBlockInterrupt's own trigger set. As
// with the other exceptions, only the render-preservation assertion is
// skipped for matching inputs.
func hasFreshTableAdjacency(b []byte) bool {
	lines := bytes.Split(b, []byte("\n"))
	for i := 1; i < len(lines); i++ {
		if !isTableDelimiterRowShaped(lines[i]) {
			continue
		}
		if len(bytes.TrimSpace(lines[i-1])) > 0 {
			return true
		}
	}
	return false
}

// hasDeepListContinuationIndent conservatively reports whether src has a
// list-item-shaped first line (a bullet or ordered marker) followed by a
// continuation line indented by 4 or more spaces.
//
// This gates an eighth, final narrow, documented render-preservation
// exception: found by FuzzFormat on "0) - \n    0", where the list item's
// continuation line carries one space of leading indentation *beyond*
// what the marker's own width structurally requires, and that extra
// space turns out to be significant content (preserved as a literal
// leading space in the rendered list item text), not insignificant
// padding. mdreflow's own continuation-line joining
// (reflow.trimLineSpace, used when building a hard-break cluster) treats
// all of a continuation line's leading whitespace as structural padding
// to strip — correct for the overwhelmingly common case, where a
// continuation line's indentation is exactly the container's own prefix
// width and nothing more, but not when an author (or, as here, a fuzzer)
// indents further. Reliably distinguishing "structural" from
// "content-significant" leading whitespace on a continuation line would
// require deriving each container's exact expected indent width from the
// AST (list marker width, blockquote depth) and comparing it against what
// the specific line actually has, rather than mdreflow's current
// approach of reading a canonical prefix once from the paragraph's first
// line (package blockmap's continuationPrefix) and applying it uniformly
// — a real gap, not merely a cosmetic difference, but narrow enough (it
// needs deliberately-over-indented continuation content, which is
// unusual authoring style) that fully closing it is out of scope for
// this milestone.
func hasDeepListContinuationIndent(src []byte) bool {
	listItemStart := regexp.MustCompile(`^[-*+]\s|^\d{1,9}[.)]\s`)
	lines := bytes.Split(src, []byte("\n"))
	for i, line := range lines {
		if i == 0 || !listItemStart.Match(lines[i-1]) {
			continue
		}
		if bytes.HasPrefix(line, []byte("    ")) {
			return true
		}
	}
	return false
}

// hasNestedDashOnlyLine reports whether src has a line ending in a run of
// one or more "-" characters preceded by whitespace or nothing else (so a
// line that is exactly "-" or "--", and a list item whose own content is
// just one of those, e.g. "* -" or "0) --", all match — broadened from a
// single-dash-only version after FuzzFormat found the same goldmark-meta
// artifact class (see blockmap.isAllDashes) triggered by "--" too, not
// just a lone "-").
//
// This gates a ninth, final narrow, documented render-preservation
// exception, an unexpected extension of the already-documented
// goldmark-meta artifact class handled elsewhere (see
// goldmarkMetaErrorComment and blockmap.isUnterminatedFrontMatterArtifact,
// both grounded in earlier fuzz finds on inputs like "-\n\n0"):
// goldmark-meta's block parser triggers on any line consisting solely of
// '-' characters, and blockmap.isUnterminatedFrontMatterArtifact only
// guards the *document's own first line* case. Found by FuzzFormat on
// "* -\n\n\t0" (a list item whose own content is just "-", loosely
// followed by more content): the same trigger fires *inside* a nested
// list-item TextBlock, not just at the document's top level, producing
// the same kind of injected parse-error artifact and swallowed-content
// structural difference — apparently goldmark-meta hooks block parsing
// generically rather than only at the document root, which
// isUnterminatedFrontMatterArtifact's specific "is this the document's
// very first child" check does not anticipate. A fully general fix would
// need to recognize this trigger at every possible block-parse entry
// point, not just the one place it was first found; narrower in scope
// (skipping the render check for the input) is the pragmatic choice here.
//
// The trailing whitespace class is `[\s\v]*`, not bare `\s`: Go's RE2 `\s`
// is `[\t\n\f\r ]` and does not include `\v` (vertical tab, 0x0B), but
// goldmark-meta's own dash-only-line trigger is more lenient than that —
// found by FuzzFormat (in a width-bounded mode) on "-\v\t \x0e!0", where a
// bare `\s`-only version of this regex missed the "-\v"-shaped line
// entirely, so the render check ran and caught the same underlying
// artifact class as an apparent *new* bug when it was really this gate
// under-matching goldmark-meta's actual leniency.
var dashOnlyLineOrItemRE = regexp.MustCompile(`(^|\s)-+[\s\v]*$`)

func hasNestedDashOnlyLine(src []byte) bool {
	for _, line := range bytes.Split(src, []byte("\n")) {
		if dashOnlyLineOrItemRE.Match(line) {
			return true
		}
	}
	return false
}

// bareBrLineRE matches a line whose entire (trimmed) content is a single
// <br> tag.
var bareBrLineRE = regexp.MustCompile(`(?i)^[\s>]*<br\s*/?>[\s]*$`)

// hasBareBrLine reports whether any line in src, trimmed of leading and
// trailing space/tab and a leading blockquote-marker-shaped prefix
// ("[\s>]*", broad enough to cover nesting), is exactly a <br> tag —
// found by FuzzFormat needing that broadening on "><Br\t>".
//
// This gates a tenth, final narrow, documented render-preservation
// exception: a single-line paragraph whose entire content is a bare
// "<br>" tag is already, correctly, an inline hard break in the original
// (rendered inside a real "<p>...</p>"), so
// writeParagraph's HardBreakBr "bare first line" safety fallback (see its
// doc comment) does not apply — there is no following line in the same
// paragraph for a block-vs-inline reparse difference to swallow content
// from. But the residual, narrower risk the fallback was *also*
// incidentally covering remains real on its own: reparsing a *document*
// consisting of nothing but that one "<br>" line can still turn it from
// inline content (wrapped in "<p>") into a bare HTML block (condition 7,
// no "<p>" wrapper at all) — found by FuzzFormat on "<Br\t>". Content is
// fully preserved either way (unlike the case the fallback does still
// handle), so this is accepted as a documented, narrow rendering
// difference rather than fixed further.
func hasBareBrLine(src []byte) bool {
	for _, line := range bytes.Split(src, []byte("\n")) {
		if bareBrLineRE.Match(bytes.Trim(line, " \t")) {
			return true
		}
	}
	return false
}

// tagLineWithTabRE matches an HTML open/close tag (any name) with a tab
// character anywhere in the same line, not just immediately adjacent to
// it — broadened from an earlier, narrower version (tab immediately
// inside the tag or immediately after ">") after FuzzFormat found a
// trailing "space then tab" sequence ("<A> \t") also disqualifies a tag
// from being a block opener in the original, the same way a lone
// trailing tab does; see hasTagLineWithInsignificantTab for the full
// story.
var tagLineWithTabRE = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9-]*[^<>]*>`)

// hasTagLineWithInsignificantTab reports whether src has a line
// containing an HTML tag and a tab character anywhere in that line.
//
// This gates an eleventh, final narrow, documented render-preservation
// exception: package reflow's own line-joining (joinClusterLines) trims
// *trailing* whitespace — space and tab both — from each line's content,
// which is otherwise exactly right (that whitespace is insignificant to
// CommonMark's own rendering). But for a would-be HTML-block-type-7
// opener specifically, goldmark's block-level check for "only whitespace
// follows the tag" (or "only whitespace separates the tag name from its
// attributes") accepts a run of literal spaces in that position but is
// disqualified by a tab appearing *anywhere* in it — confirmed directly
// against goldmark, not assumed, across several positions and
// combinations: "<A >" (space) opens a block, but "<A\t>", "<A>\t", and
// "<A> \t" (a tab, whether alone or preceded by a space, in either of the
// two candidate positions) all stay inline. So mdreflow's own
// trailing-whitespace trim can turn a line that was genuinely safe in the
// original (disqualified from type 7 by a tab anywhere in its trailing
// whitespace) into the literal, now-actually-dangerous "<A>" shape once
// the tab reflow itself discarded is gone — found across "<A\t>", "<A>\t",
// and "<A> \t". escapeBlockInterrupt's resulting escape is the
// structurally *correct* choice (it defends against the block
// reinterpretation the trim just made real), but it does not reproduce
// the original's exact "unescaped inline tag" rendering, since there is
// no way to keep both an insignificant-per-CommonMark trailing tab
// trimmed *and* the tag unescaped without reintroducing the block risk —
// an unavoidable consequence of needing to trim trailing whitespace at
// all, not a bug to chase further. The check here is deliberately broad
// (any tag-containing line with a tab anywhere in it, not trying to
// pinpoint the exact disqualifying position) rather than continuing to
// special-case each exact tab placement fuzzing turns up.
func hasTagLineWithInsignificantTab(src []byte) bool {
	for _, line := range bytes.Split(src, []byte("\n")) {
		if tagLineWithTabRE.Match(line) && bytes.ContainsRune(line, '\t') {
			return true
		}
	}
	return false
}

// unclosedTagAtLineEndRE matches a "<" immediately followed by a letter
// (a tag opener) with no ">" anywhere later on the same line.
var unclosedTagAtLineEndRE = regexp.MustCompile(`<[A-Za-z][^<>]*$`)

// hasMultilineInlineTagCandidate reports whether src has a non-last line
// that opens an HTML tag ("<" + letter) without closing it ("]") on that
// same line.
//
// This gates a twelfth, final narrow, documented render-preservation
// exception: an inline HTML/JSX tag can span a soft *or hard* line break
// — CommonMark's inline tag grammar allows whitespace (including a
// newline) between the tag name and its closing ">", the same way a link
// label or destination can (see hasMultilineLinkLabelRisk and
// blockmap.hasUnbalancedParen) — so a hard-break marker that happens to
// fall *inside* such a tag gets detected and normalized by
// reflow.detectHardBreak without any awareness that it is inside one (the
// insideSpanAfter protection this package's own join logic has for
// inline code spans, segment.CodeSpans-derived, has no equivalent for
// HTML tags). Found by FuzzFormat on "0<A  \nA>": the source's two
// trailing spaces are part of a real multi-line tag's own interior
// whitespace ("<A A>", rendering as one inline tag), not a hard break,
// but mdreflow's hard-break detection has no way to know that and
// normalizes it to "<br>" regardless, corrupting the tag. This is not
// caught by blockmap's own bracket-balance whole-paragraph-skip checks
// (hasUnbalancedBracket, hasUnbalancedParen), which are deliberately
// scoped to "[" and "(" only — "<" and ">" are far more common in
// ordinary prose (e.g. as comparison operators, "x < 10"), so applying
// the same blanket whole-paragraph skip to unbalanced angle brackets
// would refuse to reflow a large and unrelated class of ordinary
// documents; the narrower, test-only fuzz-gate approach used here avoids
// that cost, at the price of not fixing the underlying case in the
// library itself.
func hasMultilineInlineTagCandidate(src []byte) bool {
	lines := bytes.Split(src, []byte("\n"))
	for i, line := range lines {
		if i == len(lines)-1 {
			break
		}
		if unclosedTagAtLineEndRE.Match(line) {
			return true
		}
	}
	return false
}

// wrapInducedBlockTriggerRE conservatively (and broadly, not precisely —
// see hasWrapInducedBlockInterruptRisk) matches the start of text, or a
// whitespace character, followed by text shaped like one of
// reflow.escapeBlockInterrupt's "always applies regardless of line
// position" triggers: an ATX heading, blockquote marker, bullet or
// ordered list marker, a fenced-code opener, a thematic-break/setext-
// underline character run, an HTML comment/PI/tag-name opener, or a
// link-reference-definition-shaped opener.
//
// The "start of text" alternative (not just "preceded by whitespace") is
// needed for a paragraph's own first word: a width-based cut can isolate
// it onto its own line the same way it can any later word, and
// escapeBlockInterrupt's *type-7* HTML check (htmlBlockAnyOpenerRE) only
// applies at a paragraph's first output line in the first place — so a
// tag name outside htmlBlockTagNames's always-triggers list (e.g. "<A>",
// not one of the ~60 recognized block-level names) is completely safe
// mid-line in the original single-line source but becomes a genuine type-
// 7 opener once wrapping isolates it as the *first* output line — found
// by FuzzFormat on "<A> Aa0!1" in a width-bounded mode: the source's
// "<A>" was ordinary safe inline HTML (nothing else on its line to
// disqualify type-7 in the *original*), but cutting right after it
// produced a first line that *is* "just a tag, alone on a line", which
// escapeBlockInterrupt correctly (and necessarily) escapes.
var wrapInducedBlockTriggerRE = regexp.MustCompile(`(^|[ \t])(#{1,6}(\s|$)|>|[-*+](\s|$)|\d{1,9}[.)](\s|$)|` + "`{3,}|~{3,}" + `|[=-]{3,}|<[!?/A-Za-z]|\[[^\]]*\]:)`)

// hasWrapInducedBlockInterruptRisk reports whether src has such a shape
// anywhere mid-line.
//
// This gates a thirteenth narrow, documented render-preservation
// exception, in the same family as the twelve above it but specific to
// the width-bounded modes M3 adds (ModeWrap, and ModeSentence's
// MaxWidth): a block-interrupt trigger — one of the constructs
// escapeBlockInterrupt must backslash-escape wherever it lands at the
// start of a fresh output line, since CommonMark would otherwise
// misparse it as a new block — is normally reached only where a
// *sentence* break can land (right after recognized sentence-terminal
// punctuation), a narrow enough position that the M1/M2 corpus never
// happened to hit this case. A width-based cut has no such restriction:
// it can land at *any* word or clause boundary, so ordinary mid-paragraph
// content that was never anywhere near a line start in the source — e.g.
// inline HTML like "<div ...>" used correctly, mid-sentence, as raw
// inline content — can end up as the first token of a wrapped line.
// escapeBlockInterrupt's escape there is structurally *correct* (leaving
// it unescaped would let the reparsed document swallow following content
// into an accidental HTML block, corrupting structure — a worse outcome
// than a rendering difference), but it does change *this* line's own
// rendered form from raw HTML to literal, escaped text: found by
// FuzzFormat on "aX <div a09s9X1>0Y1*01" in a width-bounded mode, where
// wrapping right before "<div ...>" (originally safe, ordinary mid-
// sentence content) escaped it, changing "<p>...<div a09s9X1>...</p>"
// into "<p>...&lt;div a09s9X1&gt;...</p>". As with the other twelve
// exceptions, only the render-preservation assertion is skipped for
// matching inputs; no panic, idempotency, and structural correctness (no
// content silently swallowed into an accidental block) are still fully
// enforced.
func hasWrapInducedBlockInterruptRisk(src []byte) bool {
	return wrapInducedBlockTriggerRE.Match(src)
}

// declarationOpenerNoCloseRE matches an unclosed HTML-declaration-shaped
// opener: "<!" followed by an ASCII letter, then any run of non-">"
// characters to the end of the line (no ">" yet).
var declarationOpenerNoCloseRE = regexp.MustCompile(`<![A-Za-z][^>\n]*$`)

// hasHardBreakDeclarationRisk conservatively reports whether src has such
// an unclosed opener on a non-final line.
//
// This gates a fifteenth, final narrow, documented render-preservation
// exception — not specific to the width-bounded modes M3 adds, but
// exposed by M3's broader mode/width-diverse fuzzing (the pre-M3 harness
// only ever exercised the zero-value Options{}, and this needs nothing
// mode-specific to trigger — see below): design.md documents hard-break-
// style normalization as a render-preservation exception on the premise
// that it "renders the same <br>, different source" — a pure spelling
// change. That premise silently assumes the normalized spelling can't
// interact with *other* content, which fails for CommonMark's HTML
// "declaration" tag type: "<!" + a letter + any run of non-">" characters
// + ">". A dangling, unclosed "<!A" earlier on the same line as a hard
// break renders as literal, escaped text in the source (no ">" exists
// anywhere to complete the declaration — the line simply ends, and the
// hard break is a structural AST node, not literal text). But
// HardBreakBr (the default style) normalizes that hard break to the
// literal text "<br>", whose own ">" retroactively completes the
// declaration the source never had, turning literal "&lt;!A" into raw,
// unescaped inline HTML: found by FuzzFormat on "0<!A  \n0" (mode
// ModeSentence, default width — no wrapping involved at all), where
// "0<!A<br>\n0" parses "<!A<br>" as one HTML declaration tag instead of
// literal text followed by a break. As with the other exceptions, only
// the render-preservation assertion is skipped for matching inputs.
func hasHardBreakDeclarationRisk(src []byte) bool {
	lines := bytes.Split(src, []byte("\n"))
	for i, line := range lines {
		if i == len(lines)-1 {
			break
		}
		if declarationOpenerNoCloseRE.Match(line) {
			return true
		}
	}
	return false
}

// deriveOptions computes an mdreflow.Options from src's own bytes, so
// FuzzFormat exercises all three modes and a spread of MaxWidth values
// without a second fuzz parameter (which would require re-seeding every
// existing corpus entry with a paired mode/width byte). The derivation is a
// simple, deterministic hash of src: fuzzing mutates src, which mutates the
// derived options right along with the content being formatted, so the
// corpus ends up covering combinations organically. mode/width are always
// derived into a combination Format accepts (MaxWidth forced to 0 whenever
// mode comes out ModePara — see Options.MaxWidth's doc comment), since an
// error here would always be a deriveOptions bug, never a Format bug worth
// failing the fuzz target over.
func deriveOptions(src []byte) mdreflow.Options {
	var h uint32
	for _, b := range src {
		h = h*31 + uint32(b)
	}
	mode := mdreflow.Mode(h % 3)
	width := 0
	if mode != mdreflow.ModePara {
		width = int((h >> 8) % 121) // 0..120: spans "unbounded"/"default" (0) through comfortably wider than most test prose.
	}
	return mdreflow.Options{Mode: mode, MaxWidth: width}
}

// FuzzFormat fuzzes Format across every testdata fixture as seed corpus
// (plus the mode-specific fixtures under testdata/modes/, which target
// para/wrap/MaxWidth edge cases specifically), checking the guarantees
// stated in docs/design.md's Guarantees section: no panic, idempotency,
// and — with the documented exceptions the property test in
// format_test.go already codifies (hard-break style normalization) —
// render preservation. It also checks that Format's output always parses
// without producing a different byte-for-byte reflow of itself. Options
// (mode and MaxWidth) are derived from src itself — see deriveOptions —
// so every mode and a spread of widths are exercised without a second
// fuzz parameter.
//
// Run with: go test -fuzz=FuzzFormat -fuzztime=60s
func FuzzFormat(f *testing.F) {
	matches, err := filepath.Glob("testdata/*.md")
	if err != nil {
		f.Fatal(err)
	}
	if len(matches) == 0 {
		f.Fatal("no testdata/*.md seed files found")
	}
	modeMatches, err := filepath.Glob("testdata/modes/*/*.md")
	if err != nil {
		f.Fatal(err)
	}
	matches = append(matches, modeMatches...)
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	// A handful of small, adversarial hand-picked seeds: malformed
	// dialect markers, unbalanced brackets/backticks, and lone control
	// bytes, aimed at the regex-based skip-list and no-break scanners.
	for _, s := range []string{
		"",
		"\n",
		":::",
		":::\n:::\n:::",
		"$$",
		"$$\n$$",
		"+++\n+++",
		"{{< >}}",
		"{{% %}}",
		"[unterminated",
		"![unterminated",
		"`unterminated",
		"```\nunterminated fence",
		"> [!NOTE]",
		"> [!NOTE]\ntext",
		"- \n- \n",
		"1. a\n10. b\n",
		"\x00\x01\x02",
		"a.\\\n",
		"a.  \n",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		opts := deriveOptions(src)

		out, err := mdreflow.Format(src, opts)
		if err != nil {
			// deriveOptions only ever produces combinations Format
			// accepts (see its doc comment); an error here is a bug.
			t.Fatalf("Format returned an error for opts %+v: %v", opts, err)
		}

		twice, err := mdreflow.Format(out, opts)
		if err != nil {
			t.Fatalf("Format(Format(x)) returned an error: %v", err)
		}
		if !bytes.Equal(twice, out) {
			t.Fatalf("Format is not idempotent.\nopts: %+v\nsrc:  %q\nonce: %q\ntwice: %q", opts, src, out, twice)
		}

		// Render preservation, with the harness's documented exceptions
		// (the same ones for every mode: which lines a paragraph splits
		// onto does not change which of these narrow rendering quirks
		// apply, only where reflow's own line boundaries happen to fall
		// — see each guard function's doc comment, and hasRenderRiskyShape
		// for why each is checked against both src and out). HardBreakBr
		// is the input's own style only when the input already used <br>;
		// for the other two syntaxes normalization intentionally changes
		// the source's hard-break spelling while preserving render — see
		// TestHardBreakStyleMatrix. To keep the fuzz oracle simple and
		// still meaningful, compare with the same whitespace
		// normalization format_test.go uses.
		if !hasRenderRiskyShape(src) && !hasRenderRiskyShape(out) {
			before := normalizeWhitespace(stripGoldmarkMetaError(renderHTML(t, src)))
			after := normalizeWhitespace(stripGoldmarkMetaError(renderHTML(t, out)))
			if before != after {
				t.Fatalf("rendered HTML changed.\nsrc: %q\nout: %q\n--- before ---\n%s\n--- after ---\n%s", src, out, before, after)
			}
		}
	})
}

// hasRenderRiskyShape ORs together all fifteen documented, narrow
// render-preservation exceptions above. It is checked against *both* src
// and out (see FuzzFormat's call site), not just src: the first twelve
// exceptions were all found and documented against a pre-existing shape
// already present in the *source*, but a width-based cut (ModeWrap, and
// ModeSentence's MaxWidth) can land at any word or clause boundary and so
// can freshly *create* one of these same dangerous shapes in the output
// where the source never had it — e.g. wrapping "-- x0" into "--\nx0"
// manufactures a line that is nothing but dashes, tripping the same
// goldmark-meta front-matter-artifact trigger hasNestedDashOnlyLine
// already guards against for a source that had one to begin with (found
// by FuzzFormat on exactly that input, in a width-bounded mode). Checking
// both sides catches this without needing a fourteenth, output-specific
// copy of every existing check.
func hasRenderRiskyShape(b []byte) bool {
	return hasHardBreakAdjacentDelimiter(b) ||
		hasMultilineCodeSpanCandidate(b) ||
		hasLinkRefDefCollisionRisk(b) ||
		hasIrregularCRRun(b) ||
		hasSplitTaskListMarker(b) ||
		hasMultilineLinkLabelRisk(b) ||
		hasTableAdjacentSetextLine(b) ||
		hasDeepListContinuationIndent(b) ||
		hasNestedDashOnlyLine(b) ||
		hasBareBrLine(b) ||
		hasTagLineWithInsignificantTab(b) ||
		hasMultilineInlineTagCandidate(b) ||
		hasWrapInducedBlockInterruptRisk(b) ||
		hasFreshTableAdjacency(b) ||
		hasHardBreakDeclarationRisk(b)
}

// stripGoldmarkMetaError removes goldmark-meta's injected parse-error
// comment; see goldmarkMetaErrorComment's doc comment.
func stripGoldmarkMetaError(html string) string {
	return goldmarkMetaErrorComment.ReplaceAllString(html, "")
}
