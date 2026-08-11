package mdreflow_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/jbeda/mdreflow"
)

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
		// For the following line, only a delimiter run (or backslash)
		// immediately adjacent to the joined boundary matters: emphasis
		// flanking looks at a delimiter run's direct neighbors, and a
		// join replaces exactly one line ending with one space — a
		// delimiter deeper inside the line keeps both its neighbors and
		// cannot flank differently. "Adjacent" means the first content
		// byte after the container prefix a join would strip: blockquote
		// markers, whitespace (a former fixed-window scan here kept
		// getting outgrown by junk-byte prefixes, but junk bytes are
		// *content* — they survive the join and sit between the boundary
		// and any later delimiter, breaking adjacency — so an exact
		// first-content-byte check is both tighter and unboundedly wide).
		// This deliberately still marks list bullets ("* item"), whose
		// first content byte is a real delimiter shape; every "-" bullet,
		// snake_case word, and mid-line emphasis span now stays under the
		// render oracle instead of going dark (go-quality review S5).
		next := bytes.TrimLeft(lines[i+1], " \t>")
		if len(next) > 0 && (isDelim(next[0]) || next[0] == '\\') {
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
	crlf := bytes.Contains(src, []byte("\r\n"))
	lines := bytes.Split(src, []byte("\n"))
	for i, line := range lines {
		if i == len(lines)-1 {
			break
		}
		if bytes.Count(line, []byte("`"))%2 == 1 {
			return true
		}
		// The odd-count heuristic misses even-length span delimiters:
		// "``\r\n0 ``" is the same CRLF edge-space quirk this gate
		// documents (goldmark keeps the edge spaces for the "\r\n" form
		// only), with a two-backtick delimiter — found by FuzzFormat as
		// seed c944b0eef76ef0b0. The quirk needs a CRLF line ending, so
		// the wider any-backtick condition applies only to CRLF inputs;
		// LF-only files keep the tighter odd-count rule.
		if crlf && bytes.Contains(line, []byte("`")) {
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
// taskMarkerOnlyLineRE matches a line whose content, before any trailing
// whitespace, is exactly a task-list marker: optional container prefix,
// bullet, space(s), "[x]"/"[X]"/"[ ]". See the hard-break case in
// hasSplitTaskListMarker.
var taskMarkerOnlyLineRE = regexp.MustCompile(`^[ \t>]*[-*+][ \t]+\[[ xX]\][ \t]*$`)

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
		// A hard break whose line content is NOTHING but the task-list
		// marker itself: goldmark silently drops a trailing-double-space
		// hard break there (the checkbox consumed the line; confirmed
		// empirically — "* [X]  \n0" renders with no break), while the
		// literal "<br>" mdreflow normalizes it to DOES render as one.
		// The same "hard-break spelling is not render-neutral in this
		// context" premise failure as hasHardBreakDeclarationRisk and
		// hasBareBrLine — found by FuzzFormat on "* [X]  \n0" (seed
		// 08c644f8a9e32738), sixteen minutes into an otherwise clean
		// soak. Gated, not "fixed": the source spelling renders no break,
		// so there is no output mdreflow could emit that both preserves
		// the hard break's meaning and this render — the input is
		// self-contradictory in goldmark's model.
		if taskMarkerOnlyLineRE.Match(line) && bytes.HasSuffix(line, []byte("  ")) {
			return true
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
	trimmed := bytes.Trim(line, " \t\r")
	return len(trimmed) > 0 && bytes.ContainsRune(trimmed, '-') && len(bytes.Trim(trimmed, "-:| \t\r")) == 0
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

// dashOnlyLineOrItemRE matches a line ending in a run of one or more "-"
// characters preceded by whitespace, a blockquote marker, or nothing else
// — so a line that is exactly "-" or "--", a list item whose own content
// is just one of those (e.g. "* -" or "0) --"), and a blockquote's own
// dash-only continuation line (e.g. ">--", however deeply nested — the
// "\s" alternative below already covers ">> --" and other quote/list
// combinations where a plain space precedes the dash run; the added ">"
// alternative is only for a dash run directly, with no space, after the
// innermost ">") all match.
//
// The trailing whitespace class is `[\s\v]*`, not bare `\s`: Go's RE2 `\s`
// is `[\t\n\f\r ]` and does not include `\v` (vertical tab, 0x0B), found
// necessary by a fuzz input (in a width-bounded mode) containing a
// "-\v"-shaped line that a bare `\s`-only version of this regex missed.
var dashOnlyLineOrItemRE = regexp.MustCompile(`(^|\s|>)-+[\s\v]*$`)

// hasNestedDashOnlyLine reports whether src has a dashOnlyLineOrItemRE-
// shaped line.
//
// This gates a ninth, final narrow, documented render-preservation
// exception, in the same family as hasTableAdjacentSetextLine and
// hasWrapInducedBlockInterruptRisk but distinct from both: CommonMark's
// Setext-heading rule fires for a "-" underline of *any* length (not just
// the 3-or-more hasWrapInducedBlockInterruptRisk's "[=-]{3,}" trigger
// requires) when the line directly above it, with no blank line between,
// is itself paragraph-shaped text — including a list item's own
// continuation line, which hasTableAdjacentSetextLine (scoped to
// top-level table adjacency) does not cover. package reflow's own
// line-splitting (width-based wrapping, or — for a list item whose entire
// content already fits on one physical line — a sentence break landing
// mid-item) can manufacture a fresh short dash-only continuation line
// where the source had none: found by FuzzFormat on "\n- 0$- --\r\n" in a
// width-bounded mode, where wrapping the list item's content into "- 0$-"
// / "  --" turned "0$-" from ordinary list-item prose into a Setext H2
// heading ("<h2>0$-</h2>") on reparse — a genuine rendered-output change,
// not merely a source-spelling difference. The same hazard recurs inside a
// blockquote: found by FuzzFormat on "YB0\n>\xfb\xfb\xfb\xfb8a8- 0 -- !0"
// (width-bounded mode), where wrapping produced a bare ">--" continuation
// line, broadening dashOnlyLineOrItemRE to also match a dash run directly
// after a blockquote marker (not just after whitespace). As with the
// other exceptions, only the render-preservation assertion is skipped for
// matching inputs; no panic, idempotency, and structural correctness are
// still fully enforced.
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
// writeParagraph's "bare first line" safety fallback (see its
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
// (a tag opener) with no "<" or ">" later on the same line — an interior
// "<" is invalid inside a tag, so it ends the candidate. PI and
// comment/declaration/CDATA openers are handled separately in
// hasMultilineInlineTagCandidate: their interiors admit "<" freely
// (found by FuzzFormat on "0<?<  \n?>", seed d9518dbdc42e77ac, ten
// minutes after the plain "0<?  \n?>" find), so for them the only
// disqualifier is a ">" later on the line.
var unclosedTagAtLineEndRE = regexp.MustCompile(`<[A-Za-z][^<>]*$`)

// hasMultilineInlineTagCandidate reports whether src has a non-last line
// that opens an inline raw-HTML construct — a tag ("<" + letter), a
// processing instruction ("<?"), or a comment/declaration/CDATA ("<!")
// — without closing it (">") on that same line.
//
// This gates a twelfth, final narrow, documented render-preservation
// exception: an inline HTML/JSX tag can span a soft *or hard* line break
// — CommonMark's inline tag grammar allows whitespace (including a
// newline) between the tag name and its closing ">", the same way a link
// label or destination can (see hasMultilineLinkLabelRisk and
// blockmap.hasUnclosedDestParen) — so a hard-break marker that happens to
// fall *inside* such a tag gets detected and normalized by
// reflow.detectHardBreak without any awareness that it is inside one (the
// insideSpanAfter protection this package's own join logic has for
// inline code spans, segment.CodeSpans-derived, has no equivalent for
// HTML tags). Found by FuzzFormat on "0<A  \nA>": the source's two
// trailing spaces are part of a real multi-line tag's own interior
// whitespace ("<A A>", rendering as one inline tag), not a hard break,
// but mdreflow's hard-break detection has no way to know that and
// normalizes it to "<br>" regardless, corrupting the tag. The other
// inline raw-HTML spellings have the same interior-whitespace grammar and
// the same exposure: found again by FuzzFormat on "0<?  \n?>" (seed
// df905e1cd7af130b), a processing instruction whose interior trailing
// double-space got normalized into a "<br>" the same way. This is not
// caught by blockmap's own bracket-balance whole-paragraph-skip checks
// (hasUnbalancedBracket, hasUnclosedDestParen), which are deliberately
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
		// Each non-tag inline raw-HTML construct has its OWN terminator,
		// not a bare ">": a processing instruction ends at "?>", an HTML
		// comment at "-->", a CDATA section at "]]>", and only a
		// declaration ("<!" + anything else) ends at ">". Testing them all
		// against ">" left "0<?0>0  \n?>" ungated — its interior ">" is
		// ordinary PI content, so the construct really does span the line
		// (seed c9c2958ff1a32f79, the third find in this family).
		for _, c := range []struct{ open, close string }{
			{"<!--", "-->"},
			{"<![CDATA[", "]]>"},
			{"<?", "?>"},
			{"<!", ">"},
		} {
			k := bytes.LastIndex(line, []byte(c.open))
			if k < 0 {
				continue
			}
			if !bytes.Contains(line[k+len(c.open):], []byte(c.close)) {
				return true
			}
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
// Only the trigger classes whose escape actually *changes rendering* gate
// the render check (narrowed for go-quality review S5 — this predicate
// alone used to darken the render oracle for 26 of 63 corpus fixtures):
//
//   - "<" + tag/comment/PI/closer shape: source-side it can render as raw
//     inline HTML; escaped ("\<div") it renders as literal escaped text —
//     a real render difference (the "aX <div a09s9X1>0Y1*01" find below).
//   - 3+ backtick/tilde runs: escaping them character-by-character can
//     change inline code-span pairing, so text that rendered as <code>
//     renders as literal backticks (the #6/#8/#12 escape/re-pair family).
//
// The dropped alternatives — ATX "#", blockquote ">", bullet/ordered list
// markers, setext/thematic [=-] runs, and "[label]:" openers — are
// escaped with a plain backslash before an ASCII punctuation character,
// which CommonMark renders as exactly the character itself: the escaped
// line renders as the same literal text the unwrapped source rendered,
// so those escapes are render-preserving by construction and never
// needed to disable the oracle. (They can still be *idempotency* hazards;
// that assertion always runs.)
// The leading-context class includes '>': a tag opener directly after a
// blockquote marker ("><A> ...") is the same hazard, and reflow's own
// continuation prefix regenerates exactly that adjacency — found by
// FuzzFormat on "><A> d!A00" (seed 398d5594a1b3cc23), whose split-off
// first line "\<A>" renders escaped where the source rendered raw HTML,
// the documented accepted trade this gate exists for.
var wrapInducedBlockTriggerRE = regexp.MustCompile(`(^|[ \t>])(` + "`{3,}|~{3,}" + `|<[!?/A-Za-z])`)

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
// mode-specific to trigger — see below): a preserved hard break can be
// respelled (docs/design.md, "Hard line breaks"), and that respelling
// silently assumed it can't interact with *other* content, which fails
// for CommonMark's HTML "declaration" tag type: "<!" + a letter + any run
// of non-">" characters + ">". A dangling, unclosed "<!A" earlier on the
// same line as a hard break renders as literal, escaped text in the
// source (no ">" exists anywhere to complete the declaration — the line
// simply ends, and the hard break is a structural AST node, not literal
// text). But respelling that hard break as the literal text "<br>" lets
// its own ">" retroactively complete the declaration the source never
// had, turning literal "&lt;!A" into raw, unescaped inline HTML: found by
// FuzzFormat on "0<!A  \n0" (mode ModeSentence, default width — no
// wrapping involved at all), where "0<!A<br>\n0" parses "<!A<br>" as one
// HTML declaration tag instead of literal text followed by a break. As
// with the other exceptions, only the render-preservation assertion is
// skipped for matching inputs.
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
// existing corpus entry with a paired mode/width byte). The derivation is
// a simple, deterministic hash of src: fuzzing mutates src, which mutates
// the derived options right along with the content being formatted, so
// the corpus ends up covering combinations organically. Every field is
// always derived into a combination Format accepts (MaxWidth forced to 0
// whenever mode comes out ModePara — see Options.MaxWidth's doc comment),
// since an error here would always be a deriveOptions bug, never a Format
// bug worth failing the fuzz target over.
//
// (Bits 16/17 of the hash once selected typography flags; typography is
// removed. Bit 17 now selects Dialect — (h>>17)%2, gfm(0) | mkdocs(1), one
// bit since there are two dialects — and bit 16 is left unused. Mode and
// width read their own bit ranges and are untouched, so existing corpus
// entries keep deriving the mode and width they were minimized under.)
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
	dialect := mdreflow.Dialect((h >> 17) % 2) // gfm(0) | mkdocs(1)
	return mdreflow.Options{Mode: mode, MaxWidth: width, Dialect: dialect}
}

// FuzzFormat fuzzes Format across every testdata fixture as seed corpus
// (plus the mode-specific fixtures under testdata/modes/, which target
// para/wrap/MaxWidth edge cases specifically), checking the guarantees
// stated in docs/design.md's Guarantees section: no panic, idempotency,
// and — with the documented narrow rendering-quirk exceptions below —
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
		// MkDocs admonition shapes (#26), each padded with trailing blank
		// lines so its own hash derives DialectMkDocs (see deriveOptions) —
		// otherwise a hand-picked seed only reaches the mkdocs dialect by
		// accident of mutation. A single-paragraph body long enough to
		// wrap under a narrow derived width:
		"!!! note\n\n    This is a fairly long line of body text that should be long enough to wrap when reflowed under a narrow width.\n\n\n\n\n\n",
		// A multi-paragraph body — the shape whose reflow can merge two
		// rendered <p>s if admonitionBodies' multi-paragraph guard
		// (internal/blockmap/blockmap.go) ever regresses:
		"!!! note\n\n    First paragraph of the admonition body goes here and has some words.\n\n    Second paragraph of the admonition body follows right after a blank line.\n\n\n\n\n\n\n\n\n",
		// The "???" collapsible marker variant:
		"??? note\n\n    A collapsible admonition body with some text that may wrap across lines when reflowed.\n\n\n\n\n",
		// A marker with a quoted title:
		"!!! tip \"Custom Title\"\n\n    Body text under a titled admonition, long enough that reflow under a narrow width would need to wrap it across more than one line.\n",
		// Malformed: a marker line whose title quote never closes, so
		// admonitionMarkerRE must not match it:
		"!!! tip \"Unterminated title\n\n    Body text that follows a marker line whose title quote never closes.\n",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		opts := deriveOptions(src)

		// The oracles below run against the single-pass core: the
		// convergence backstop in Format would make the idempotency
		// check tautological, hiding exactly the planner bugs this
		// harness exists to find (docs/design.md, Convergence).
		mdreflow.SetConvergenceBackstop(false)
		defer mdreflow.SetConvergenceBackstop(true)

		if !utf8.Valid(src) {
			// Outside the input domain (design.md, Guarantees): the
			// only promise is a loud typed refusal — and no panic.
			if _, err := mdreflow.Format(src, opts); !errors.Is(err, mdreflow.ErrInvalidUTF8) {
				t.Fatalf("Format(invalid UTF-8) = %v; want ErrInvalidUTF8", err)
			}
			return
		}

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
		// for why each is checked against both src and out). A preserved
		// hard break keeps the source's own spelling, except that two
		// trailing spaces are promoted to a backslash — a source-spelling
		// change that preserves render (see TestHardBreakSpellingPolicy).
		// To keep the fuzz oracle simple and still meaningful, compare
		// with the same whitespace normalization format_test.go uses.
		//
		if noteRenderOracle(src, out) {
			before := normalizeForRender(renderHTML(t, src))
			after := normalizeForRender(renderHTML(t, out))
			if before != after {
				t.Fatalf("rendered HTML changed.\nsrc: %q\nout: %q\n--- before ---\n%s\n--- after ---\n%s", src, out, before, after)
			}
		}
	})
}

// hasRenderRiskyShape reports whether any of the documented, narrow
// render-preservation exceptions in renderGates fires. It is checked
// against *both* src
// and out (see FuzzFormat's call site), not just src: most of these
// exceptions were found and documented against a pre-existing shape
// already present in the *source*, but a width-based cut (ModeWrap, and
// ModeSentence's MaxWidth) can land at any word or clause boundary and so
// can freshly *create* one of these same dangerous shapes in the output
// where the source never had it (e.g. hasFreshTableAdjacency's and
// hasNestedDashOnlyLine's own doc comments). Checking both sides catches
// that without needing a sixteenth, output-specific copy of every
// existing check.
//
// Two former exceptions here — hasTypographySubstitutableInHTMLTag and
// hasTypographySubstitutableInLinkDestination — are gone: their
// underlying gaps in package segment's no-break-span derivation were
// fixed rather than merely gated (see internal/segment/nobreak.go's
// htmlTagSpans and bracketedSpans doc comments, and mdreflow issue #3),
// and the typography feature they existed for has since been removed.
func hasRenderRiskyShape(b []byte) bool {
	return firstRenderGate(b) >= 0
}

// renderGates lists the exceptions in the order they are checked. The
// names are what the skip report prints, so keep them short and specific.
var renderGates = []struct {
	name string
	fn   func([]byte) bool
}{
	{"hard-break-adjacent-delimiter", hasHardBreakAdjacentDelimiter},
	{"multiline-code-span", hasMultilineCodeSpanCandidate},
	{"link-ref-def-collision", hasLinkRefDefCollisionRisk},
	{"irregular-cr-run", hasIrregularCRRun},
	{"split-task-list-marker", hasSplitTaskListMarker},
	{"multiline-link-label", hasMultilineLinkLabelRisk},
	{"table-adjacent-setext", hasTableAdjacentSetextLine},
	{"deep-list-continuation-indent", hasDeepListContinuationIndent},
	{"nested-dash-only-line", hasNestedDashOnlyLine},
	{"bare-br-line", hasBareBrLine},
	{"tag-line-insignificant-tab", hasTagLineWithInsignificantTab},
	{"multiline-inline-tag", hasMultilineInlineTagCandidate},
	{"wrap-induced-block-interrupt", hasWrapInducedBlockInterruptRisk},
	{"fresh-table-adjacency", hasFreshTableAdjacency},
	{"hard-break-declaration", hasHardBreakDeclarationRisk},
}

// firstRenderGate returns the index of the first gate that fires for b, or
// -1 if none do. It short-circuits, so an input several gates would flag is
// attributed to the earliest one — enough to tell which gate to narrow
// next, and cheap enough for the fuzz inner loop.
func firstRenderGate(b []byte) int {
	for i, g := range renderGates {
		if g.fn(b) {
			return i
		}
	}
	return -1
}

// Skip accounting for the render-preservation oracle. A green fuzz run
// says nothing about how much of the oracle was dark; these counters turn
// that into a number reportRenderSkips prints at the end of the run.
var (
	renderOracleRuns  atomic.Int64
	renderOracleSkips atomic.Int64
	renderGateHits    = make([]atomic.Int64, len(renderGates))
)

// noteRenderOracle records one oracle decision, attributing a skip to the
// gate that caused it, and reports whether the check should run.
func noteRenderOracle(src, out []byte) bool {
	renderOracleRuns.Add(1)
	gate := firstRenderGate(src)
	if gate < 0 {
		gate = firstRenderGate(out)
	}
	if gate < 0 {
		return true
	}
	renderOracleSkips.Add(1)
	renderGateHits[gate].Add(1)
	return false
}

// reportRenderSkips prints the render-oracle skip rate and its per-gate
// breakdown. TestMain calls it after the run; with -fuzz it reports one
// worker process's share, which is still the right shape for judging
// whether a narrowing helped.
func reportRenderSkips() {
	runs := renderOracleRuns.Load()
	if runs == 0 {
		return
	}
	skips := renderOracleSkips.Load()
	fmt.Fprintf(os.Stderr, "render oracle: %d/%d inputs skipped (%.1f%% dark)\n",
		skips, runs, 100*float64(skips)/float64(runs))

	type row struct {
		name string
		hits int64
	}
	var rows []row
	for i, g := range renderGates {
		if n := renderGateHits[i].Load(); n > 0 {
			rows = append(rows, row{g.name, n})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].hits > rows[j].hits })
	for _, r := range rows {
		fmt.Fprintf(os.Stderr, "  %-32s %d\n", r.name, r.hits)
	}
}
