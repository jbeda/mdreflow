// Package reflow implements mdreflow's reflow pipeline: given a parsed
// goldmark document and its source bytes, join each reflow-eligible
// paragraph's prose lines, find sentence boundaries, and splice the result
// back into the verbatim source. Everything outside a reflowed paragraph —
// including dialect marker lines inside a paragraph, per package blockmap —
// is copied byte-for-byte.
//
// Hard line breaks (trailing double-space, trailing backslash, `<br>`) are
// immovable: the pipeline never joins across one. Preserved hard breaks are
// normalized to Options.HardBreaks's configured style.
package reflow

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/blockmap"
	"github.com/jbeda/mdreflow/internal/gm"
	"github.com/jbeda/mdreflow/internal/segment"
	"github.com/jbeda/mdreflow/internal/typography"
)

// Segmenter is the subset of mdreflow.Segmenter the pipeline needs. A
// mdreflow.Segmenter value satisfies this interface directly since
// mdreflow.Span is a type alias for segment.Span.
type Segmenter interface {
	Breaks(text string) []segment.Span
}

// HardBreakStyle mirrors mdreflow.HardBreakStyle (an internal package
// cannot import the root package, which imports this one). format.go
// converts between the two; the iota values are kept in lockstep.
type HardBreakStyle int

// HardBreak style constants, in the same order as mdreflow.HardBreakStyle.
const (
	HardBreakBr HardBreakStyle = iota
	HardBreakSpaces
	HardBreakBackslash
)

// Mode mirrors mdreflow.Mode (an internal package cannot import the root
// package, which imports this one). format.go converts between the two;
// the iota values are kept in lockstep.
type Mode int

// Mode constants, in the same order as mdreflow.Mode.
const (
	ModeSentence Mode = iota
	ModePara
	ModeWrap
)

// defaultWrapWidth is ModeWrap's effective width when Options.MaxWidth is
// 0, matching the CLI's --max-width default for wrap mode (docs/design.md's
// CLI table). This is the one place a zero MaxWidth does not mean
// "unbounded" — see Options.MaxWidth's doc comment.
const defaultWrapWidth = 80

// Options configures the reflow pipeline. It mirrors the subset of
// mdreflow.Options the pipeline needs.
type Options struct {
	// Mode selects the break-point strategy; see mdreflow.Mode.
	Mode Mode
	// MaxWidth bounds line length in runes; see mdreflow.Options.MaxWidth
	// for its full, mode-dependent semantics. The caller (format.go) is
	// responsible for rejecting MaxWidth != 0 with ModePara before this
	// package ever sees it.
	MaxWidth int
	// Typography selects the opt-in span-level prose substitutions
	// (smart quotes, ellipsis); 0 is off. Unlike Mode and
	// HardBreakStyle, this needs no local mirror type: package
	// typography is internal too, so both this package and format.go
	// can name its type directly.
	Typography typography.Typography
	// HardBreaks selects the normalized hard-break style every preserved
	// hard break is rewritten to.
	HardBreaks HardBreakStyle
	// StripSentenceTerminalBreaks treats a trailing double-space
	// immediately after sentence-terminal punctuation as an accidental
	// hard break and removes it (the line rejoins its neighbor as an
	// ordinary soft break instead). Only the double-space syntax is
	// eligible; backslash and <br> hard breaks are always respected.
	StripSentenceTerminalBreaks bool
}

// Format reflows every eligible paragraph in doc (see package blockmap) and
// returns the full document bytes, splicing reflowed prose into the
// otherwise-untouched source.
func Format(source []byte, doc ast.Node, seg Segmenter, opts Options) []byte {
	paras := blockmap.Paragraphs(doc, source)

	var out bytes.Buffer
	cursor := 0
	for _, p := range paras {
		out.Write(source[cursor:p.Start])
		writeParagraph(&out, p, source, seg, opts)
		cursor = p.End
	}
	out.Write(source[cursor:])
	return out.Bytes()
}

// outLine is one line of writeParagraph's output.
type outLine struct {
	text string
	// verbatim is true for a dialect-marker boundary line reproduced from
	// the raw source (container prefix included): ContPrefix must not be
	// prepended to it, since it already carries its own real prefix.
	verbatim bool
	// noEscape is true for a dialect-marker boundary line whose content is
	// emitted from the paragraph's own first line (verbatim handles the
	// i > 0 case). A boundary line's bytes ARE its verdict: blockmap
	// re-derives "immovable marker" from them on the next parse, so
	// escapeBlockInterrupt must never touch them — found by FuzzFormat on
	// "#\n:::-\n0" (seed 8430eba8c33c2dd7), where the first-line marker
	// ":::-" also happens to be table-delimiter-row-shaped and the
	// heading above made prevLineNonBlank true, so the delimiter-row
	// trigger backslash-escaped it; the next pass no longer saw a marker
	// and joined the cluster, an idempotency flip. Escaping is also never
	// NEEDED here: the line's bytes existed at this exact line start in
	// the source and parsed as paragraph content, so identical bytes at
	// the identical position reparse identically.
	noEscape bool
}

// writeParagraph joins p's prose lines into hard-break clusters — stopping
// at both hard breaks and dialect-marker boundary lines (blockmap.Paragraph
// .Boundary) — sentence-splits each cluster, and writes the result to buf,
// re-indenting every output line after the first with p.ContPrefix.
func writeParagraph(buf *bytes.Buffer, p blockmap.Paragraph, source []byte, seg Segmenter, opts Options) {
	lines := p.Node.Lines()
	n := lines.Len()

	// firstLinePrefix is the raw source bytes on p's own first physical
	// line, before p.Start — a list item's "- "/"1. " marker, a
	// blockquote's "> ", or "" for a paragraph with no container prefix
	// at all. Format's caller already writes these bytes verbatim (see
	// Format's doc comment on Paragraph.Start), so writeParagraph never
	// touches them directly, but escapeBlockInterrupt still needs to know
	// them — see its firstLinePrefix parameter doc comment.
	firstLinePrefix := string(source[blockmap.LineStart(source, p.Start):p.Start])

	// rawContents[i] is line i's content (line ending stripped, hard-break
	// marker bytes not yet stripped). insideSpanAfter[i] reports whether
	// the boundary right after line i's content — where a hard-break
	// marker would sit, and where a cluster join would otherwise insert a
	// fresh separator — falls inside a *genuine* inline code span: one
	// that goldmark's own inline grammar would actually match open-to-
	// close, computed via segment.CodeSpans over the whole paragraph's
	// lines joined by "\n" (matching how goldmark's own inline parser sees
	// a paragraph: one continuous stream, soft breaks and all). This is
	// deliberately not a per-line running-parity guess: an opening
	// backtick run with no later matching close is not a span at all, and
	// a naive per-line parity check cannot tell the difference — found by
	// FuzzFormat on "`\\\n0" (one backtick, never closed: the "insideSpan"
	// heuristic must not fire here, even though the backtick count so far
	// is odd).
	rawContents := make([]string, n)
	lastLineHasNewline := false
	for i := 0; i < n; i++ {
		lineSeg := lines.At(i)
		c, hasNewline := stripLineEnding(lineSeg.Value(source))
		rawContents[i] = c
		if i == n-1 {
			lastLineHasNewline = hasNewline
		}
	}
	insideSpanAfter := insideCodeSpanAfterLine(rawContents)

	var outLines []outLine
	var curLines []lineFrag

	flush := func(marker string, lastCluster bool) {
		if len(curLines) == 0 && marker == "" {
			return
		}
		text := joinClusterLines(curLines, marker != "")
		curLines = nil
		if marker == "" {
			// The join itself can manufacture a hard-break spelling no
			// source line carried: a multi-line inline tag like
			// "<Br\n/>" joins to "<Br />", which IS the self-closing
			// br-marker form the next pass's per-line detection will
			// recognize and normalize — an idempotency flip (found by
			// FuzzFormat on "0<Br\n/>", seed 731b45747c153106, thirty
			// minutes into a soak; sibling of the "<Br >" find, but this
			// spelling is legitimate and cannot be dropped from
			// hardBreakBrRE). Run the same detection pass 2 will run on
			// this joined line, so pass 1 already speaks with pass 2's
			// voice. The cluster boundary a non-final flush
			// creates means a following line exists in the emitted
			// paragraph whenever the manufactured marker could matter.
			// lastCluster threads the same isLastLine semantics the
			// per-line calls use: a paragraph-final backslash or double
			// space is NOT a break (a lone "\" document must stay a lone
			// "\" — seed 577e36abd20bf697 pinned the regression from an
			// earlier hardcoded false here), while the br-tag form
			// normalizes even paragraph-finally, exactly as pass 2's
			// per-line pass would. insideSpan=false matches the per-line
			// call for a line whose span context the join has resolved.
			if m, rest := detectHardBreak(text, opts, lastCluster, false); m != "" {
				marker = m
				text = rest
			}
		}
		// Typography substitution happens here — on the whole joined
		// cluster, *before* computeLines segments or wraps it — not on
		// the per-line output afterwards. Two reasons:
		//
		//   - Idempotency of the ellipsis. Sentence segmentation must
		//     see the same terminal punctuation on every pass. Pass 1
		//     would segment against literal "..." and pass 2 against an
		//     already-substituted "…"; substituting first makes both
		//     passes segment the identical text. (segment.terminalRun
		//     recognizes both spellings regardless, so the two agree —
		//     but only doing the substitution up front makes that
		//     agreement structural rather than incidental.)
		//   - One mental model. Quote directionality is decided from
		//     local raw-text context, which is unaffected by where a
		//     later line break lands, so quotes could go either way;
		//     keeping both substitutions in the same place, on the same
		//     text, is simpler than splitting them.
		//
		// The protected ranges are computed on this same
		// pre-substitution text, which is what lets Apply consult them
		// by original-text byte position while a quote substitution
		// grows from 1 byte to 3 (see typography.Apply).
		if opts.Typography != 0 {
			text = typography.Apply(text, segment.NoBreakSpans(fenceEscapeNeutralize(text)), opts.Typography)
		}
		// A fence-opener-shaped cluster is pre-escaped here, before
		// computeLines ever measures or wraps it — not left for
		// escapeBlockInterrupt to handle only once a final output line is
		// chosen, the way every other block-interrupt trigger is. This
		// cluster's own leading bytes always become its first *output*
		// line's leading bytes (computeLines/wrapRanked never introduce a
		// break before position 0), so the run is always eventually
		// escaped regardless; doing it now instead means every width
		// decision from here on (fitLen, wordBreaks, wrapRanked's
		// candidate measurement) operates on the exact bytes that will
		// actually be emitted and exactly what the next reformat pass
		// will reparse as its own raw source — not on a backtick-shaped
		// stand-in whose *escaped* width and canonical-collapsing
		// behavior can differ from the real thing in ways a canonical-
		// plus-delta estimate (escapeDeltaMax) cannot safely bound, since
		// the escape only touches the run itself and leaves the rest of
		// the cluster's real, uncollapsed bytes untouched.
		//
		// This replaced an earlier attempt at teaching widthMeasurer to
		// compute a fence candidate's *exact* real (uncollapsed) width
		// instead of estimating it: that fixed the immediate
		// under-estimate but introduced a *worse* problem — a candidate
		// it rejected because the raw suffix didn't fit forced an earlier
		// cut, but that cut's own leftover, being an ordinary (no hard
		// break) continuation line, gets rejoined by joinClusterLines on
		// the *next* pass and re-measured there canonically (since it no
		// longer starts with a backtick once escaped, so the *generic*
		// path governs it then) — a different rule on each side of the
		// escape, with no local, per-candidate fix able to predict what
		// the far side will decide. Found by FuzzFormat on ModeSentence,
		// MaxWidth 17, " \r```  b 0" and " \r``` Z C00": whichever
		// candidate pass 1 picked, pass 2's rejoin-and-recanonicalize of
		// the leftover chose differently. Pre-escaping first sidesteps
		// the whole class: computeLines runs the *same*, unmodified,
		// already-idempotent canonical algorithm pass 2 will *also* run
		// on this same content, because by the time either pass reaches
		// it, it is no longer fence-shaped at all.
		text = escapeFenceOpenerRun(text)
		clusterLines := computeLines(text, seg, opts)
		for i, s := range clusterLines {
			if i == len(clusterLines)-1 {
				s = attachMarker(s, marker)
			}
			outLines = append(outLines, outLine{text: s})
		}
	}

	for i := 0; i < n; i++ {
		lineSeg := lines.At(i)
		content := rawContents[i]

		if p.Boundary[i] {
			flush("", false)
			text := content
			verbatim := false
			if i > 0 {
				// Not the paragraph's first line: reproduce the line's
				// real raw bytes, container prefix included, since no
				// pass-through copy will supply that prefix for us.
				full := source[blockmap.LineStart(source, lineSeg.Start):lineSeg.Stop]
				text, _ = stripLineEnding(full)
				verbatim = true
			}
			outLines = append(outLines, outLine{text: text, verbatim: verbatim, noEscape: true})
			continue
		}

		marker, rest := detectHardBreak(content, opts, i == n-1, insideSpanAfter[i])
		if i == 0 && i < n-1 && trimLineSpace(rest) == "" && marker == "<br>" {
			// Fuzz-found hazard: a paragraph's first output line, with no
			// prose preceding a HardBreakBr marker, would consist solely
			// of the bytes "<br>" — which, on a line that opens a fresh
			// block (nothing precedes it, so it cannot be a paragraph
			// lazy-continuation line, which is the only context immune to
			// this), CommonMark's HTML-block condition 7 recognizes as an
			// HTML block opener, not inline content. Reparsing the
			// output would then swallow following lines into that HTML
			// block, corrupting structure. Two trailing spaces are not a
			// safe fallback here either: a line of only spaces is itself
			// a blank line per CommonMark, which would make this the
			// paragraph's first line vanish (blank) and lose the break
			// entirely on reparse — verified empirically, not merely
			// assumed. A lone trailing backslash is safe: it is not a
			// blank line and is not any block-opening trigger, so it is
			// always the fallback for this one narrow position,
			// regardless of the configured style.
			//
			// The "i < n-1" guard (there is a following line to worry
			// about) matters: without it, a single-line paragraph whose
			// *entire* content is a bare "<br>" tag (e.g. a real "<Br\t>"
			// — already, correctly, an inline hard break in the original,
			// not just a marker with nothing else around it) had this
			// fallback replace that content outright with a lone
			// backslash — total content loss, worse than the narrower
			// block-vs-inline rendering risk it was guarding against,
			// found by FuzzFormat on "<Br\t>". With nothing following
			// this line at all, there is nothing for a block
			// reinterpretation to swallow, so the fallback is not needed
			// (the residual, much narrower risk — this line rendering as
			// a bare HTML block instead of inline "<br>" inside a
			// paragraph — is accepted, not fixed further).
			marker = "\\"
		}
		curLines = append(curLines, lineFrag{
			text:              rest,
			leadingProtected:  i > 0 && insideSpanAfter[i-1],
			trailingProtected: insideSpanAfter[i],
		})
		if marker != "" || i == n-1 {
			flush(marker, i == n-1)
		}
	}

	// precededByNonBlankLine reports whether the raw source line
	// immediately before this paragraph's own first physical line is
	// non-blank — the one piece of context escapeBlockInterrupt's
	// table-delimiter-row check needs for the paragraph's first output
	// line (i == 0) that it cannot derive from that line alone, the same
	// way firstLinePrefix supplies isThematicBreak's joint context. See
	// escapeBlockInterrupt's prevLineNonBlank parameter doc comment for
	// why this is needed at all and why it is safe to compute once, from
	// the unmodified source, rather than per output line.
	precededByNonBlankLine := false
	if physStart := blockmap.LineStart(source, p.Start); physStart > 0 {
		prevStart := blockmap.LineStart(source, physStart-1)
		prevLine, _ := stripLineEnding(source[prevStart:physStart])
		precededByNonBlankLine = trimLineSpace(prevLine) != ""
	}

	for i, ol := range outLines {
		if i > 0 {
			buf.WriteByte('\n')
			if !ol.verbatim {
				buf.WriteString(p.ContPrefix)
			}
		}
		if !ol.verbatim && !ol.noEscape {
			// Applied to every non-verbatim output line, not just
			// continuation lines: a paragraph's very first output line is
			// only ever the unmodified start of the source's own first
			// line when that line was never itself split — but sentence
			// segmentation can split *within* an originally-safe first
			// line (found by FuzzFormat on "```! 0`0": the original
			// single line was not a valid fence opener, since a backtick
			// fence's info string may not contain a backtick, but
			// splitting it into "```!" and "0`0" produces a first output
			// line whose info string no longer does). This is always safe
			// to apply unconditionally: a genuine, never-split first line
			// can never itself match a trigger, since if it did, goldmark
			// would already have parsed it as that block instead of a
			// paragraph in the first place. That reasoning holds only for
			// the line's own leading bytes, though — see
			// escapeBlockInterrupt's firstLinePrefix parameter doc comment
			// for the container-marker-plus-wrapped-content case it
			// doesn't cover, and its prevLineNonBlank parameter doc
			// comment for the table-delimiter-row case, which depends on
			// the *previous* line's content instead.
			prefix := ""
			prevNonBlank := precededByNonBlankLine
			if i == 0 {
				prefix = firstLinePrefix
			} else {
				prevNonBlank = trimLineSpace(outLines[i-1].text) != ""
			}
			ol.text = escapeBlockInterrupt(ol.text, i == 0, prefix, prevNonBlank)
		}
		buf.WriteString(ol.text)
	}
	if lastLineHasNewline {
		buf.WriteByte('\n')
	}
}

// computeLines computes one hard-break cluster's output lines according to
// opts.Mode — the one place the three modes' break-point strategies
// diverge (docs/design.md: "All modes are one pipeline — join lines,
// compute break points, emit"). Everything else (joining, hard-break
// handling, container re-indentation, block-interrupt escaping) is shared
// and mode-independent.
func computeLines(text string, seg Segmenter, opts Options) []string {
	switch opts.Mode {
	case ModePara:
		// Join to a single line, unconditionally: no further splitting.
		// format.go rejects MaxWidth != 0 with ModePara before this is
		// ever reached.
		return []string{text}
	case ModeWrap:
		width := opts.MaxWidth
		if width == 0 {
			width = defaultWrapWidth
		}
		if fitLen(text) <= width {
			return []string{text}
		}
		return wrapRanked(text, width, filterUnsafeLineEnds(text, wordBreaks(text)), nil)
	default: // ModeSentence
		sentences := splitSentences(text, seg)
		if opts.MaxWidth <= 0 {
			return sentences
		}
		out := make([]string, 0, len(sentences))
		for _, s := range sentences {
			if fitLen(s) <= opts.MaxWidth {
				out = append(out, s)
				continue
			}
			out = append(out, wrapRanked(s, opts.MaxWidth, filterUnsafeLineEnds(s, clauseBreaks(s)), filterUnsafeLineEnds(s, wordBreaks(s)))...)
		}
		return out
	}
}

// filterUnsafeLineEnds removes any candidate break whose Start position
// would leave a dangerous pattern at the end of the line that ends there:
//
//   - An otherwise-literal single trailing backslash, or a literal
//     <br>-shaped tag already present in the prose. Once a newline lands
//     right after either, CommonMark (and detectHardBreak, on the next
//     reformat) reads it as a real hard break — content the source author
//     never intended, and something no sentence break can ever trigger (a
//     sentence break only ever lands after recognized sentence-terminal
//     punctuation), but a width-based cut can, since it may land after any
//     word or clause boundary in the text. Found by FuzzFormat wrapping a
//     line right after a literal mid-prose backslash in ModeWrap, turning
//     an inert character into a real hard break on reparse.
//   - A trailing bare '\r': goldmark's own line scanner does not treat a
//     lone '\r' (one not immediately followed by '\n') as a line ending at
//     all — it can sit as ordinary literal content in the middle of what
//     goldmark still considers a single physical line (confirmed directly,
//     not assumed: stripLineEnding's own doc comment already documents
//     this quirk for a *trailing* run of them). A width-based cut landing
//     right after such a '\r' would place mdreflow's own injected '\n'
//     immediately after it, accidentally spelling out a real "\r\n" line
//     ending that was never there in the source — found by FuzzFormat on
//     an input with a lone mid-prose '\r', wrapped in ModeWrap, which lost
//     content on reparse once the accidental "\r\n" changed how the line
//     was split.
//   - A *following* line that would itself open with a fenced-code-block
//     opener shape (3+ backticks/tildes): escapeBlockInterrupt backslash-
//     escapes every backtick/tilde of such a run individually wherever it
//     lands at a line start (see its own doc comment for why every one,
//     not just the first). That per-character escaping can retroactively
//     change what segment.CodeSpans considers a *matched* code span
//     elsewhere in the very same cluster text: an unmatched, dangling
//     single backtick earlier in the text — one with no real partner
//     anywhere, so codeSpans correctly does not protect anything around
//     it — can spuriously "close" against one of the escaped run's
//     individual backticks once they exist as separate length-1 runs,
//     manufacturing a code span (and its no-break protection) that was
//     never there when this candidate was first evaluated. Found by
//     FuzzFormat on ModeSentence/MaxWidth input
//     "!Xb0A!`C0B7“\t```0" (an unmatched backtick early on, then a
//     fence-opener-shaped "```0" later): cutting at the tab was safe on
//     the *pre-escape* text (no code span spanned it, since the fence
//     run's length-3 backtick run didn't match the earlier length-1
//     unmatched one), but the very act of that cut placed "```0" at a
//     fresh line start, which escapeBlockInterrupt then escaped into
//     three separate length-1 backtick runs — one of which reparsed as
//     spuriously matching the earlier dangling backtick, newly protecting
//     the space this same cut relied on being unprotected, so a second
//     reformat pass no longer found any safe candidate at all and left
//     the whole line unwrapped. Refusing to cut immediately before a
//     fence-opener-shaped suffix in the first place sidesteps the
//     escape-driven code-span reshuffling entirely, rather than trying to
//     predict its effect on unrelated backticks elsewhere in the text.
//
// All three are instances of the same underlying risk: a width-based cut
// can land anywhere a word or clause boundary exists, unlike a sentence
// break (which only ever lands after recognized sentence-terminal
// punctuation) or the original source's own line breaks (which mdreflow
// never introduces mid-word).
//
// text must be the same string breaks's Start/End positions index into.
func filterUnsafeLineEnds(text string, breaks []segment.Span) []segment.Span {
	if len(breaks) == 0 {
		return breaks
	}
	// Precompute every prefix length that ends with a <br>-shaped tag plus
	// optional trailing whitespace — the positions where the $-anchored
	// hardBreakBrRE would match text[:p]. Running that regex per candidate
	// rescans the whole prefix each time (quadratic, and the dominant cost
	// of width-constrained modes once wrapRanked stopped rescanning); one
	// pass with the unanchored tag core is equivalent: a prefix satisfies
	// hardBreakBrRE exactly when it ends inside [tagEnd, tagEnd+wsRun] for
	// some tag match (the pattern's leading [ \t]* never affects whether a
	// match ending at $ exists).
	var brEnds []segment.Span // prefix lengths in [Start, End] end with a <br> tag
	for _, m := range brTagCoreRE.FindAllStringIndex(text, -1) {
		e := m[1]
		j := e
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		brEnds = append(brEnds, segment.Span{Start: e, End: j})
	}
	bi := 0
	out := breaks[:0:0]
	for _, b := range breaks {
		for bi < len(brEnds) && brEnds[bi].End < b.Start {
			bi++
		}
		if bi < len(brEnds) && brEnds[bi].Start <= b.Start {
			continue // prefix ends with a <br> tag (+ trailing ws)
		}
		before := text[:b.Start]
		if trailingBackslashCount(before) == 1 || strings.HasSuffix(before, "\r") {
			continue
		}
		after := text[b.End:]
		if backtickFenceStart.MatchString(after) || tildeFenceStart.MatchString(after) {
			continue
		}
		out = append(out, b)
	}
	return filterLineStartHazards(text, out)
}

// brTagCoreRE is hardBreakBrRE's tag shape without the surrounding
// whitespace or the $ anchor; see filterUnsafeLineEnds for how it stands
// in for the anchored form.
var brTagCoreRE = regexp.MustCompile(`(?i)<br[ \t]*/?>`)

// linkifyTokenStart matches the start of a token GFM's linkify extension
// turns into a bare link with no delimiters at all: a scheme-prefixed
// URL, a "www."-prefixed one, or an email address.
var linkifyTokenStart = regexp.MustCompile(
	"(?i)^(?:[a-z][a-z0-9+.-]*://|www\\.|[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9-]+(?:\\.[a-z0-9-]+)+)")

// filterLineStartHazards removes candidate breaks whose *following* text
// would mean something different once it sits at the start of a fresh
// line than it does mid-line. Two such hazards exist, and both apply to
// sentence breaks as well as width-based cuts — unlike the rules in
// filterUnsafeLineEnds, which concern what a cut leaves at a line's
// *end* and which only a width-based cut can reach.
//
// # Dialect markers
//
// A line starting ":::", "{{<", "$$", "+++", "{expr}" or a GitHub alert
// marker is a skip-list boundary (or a whole-node skip) per package
// blockmap, so relocating such text to a line start changes the
// paragraph structure the *next* formatting pass sees — an idempotency
// break. See blockmap.MarkerLineStart for the fuzz find.
//
// A sentence break can reach this too, not just a width cut: a sentence
// break requires the following character to plausibly start a sentence,
// and "[" (the GitHub alert marker's first byte) qualifies.
//
// # GFM linkify
//
// A candidate break that would move a linkify-eligible token (see
// linkifyTokenStart) from a position where GFM's linkify extension does
// *not* recognize it to the start of a fresh line, where it does, turns
// inert literal text into a real link.
//
// goldmark's linkify parser only fires at a line start or immediately
// after one of a small set of trigger bytes (space, "*", "_", "~", "("),
// confirmed directly rather than assumed: "07 a@b.co" linkifies the
// address, "07\ta@b.co" (a tab instead of the space) does not, and
// "a@b.co" alone on a line does. mdreflow's break candidates consume a
// whole run of spaces/tabs/bare-CRs (see wordBreaks), so a run ending in
// a tab or a bare CR — a byte that was *suppressing* linkification — can
// be replaced by a newline that enables it. Found by FuzzFormat on
// "07\tAA91AA@A001AA.0", where cutting at the tab produced a mailto link
// the source never had.
//
// The test is deliberately one-sided: a cut always puts the token at a
// line start, where linkify always fires, so the only possible flip is
// off -> on, and it happens exactly when the byte the cut consumes last
// is not itself a trigger. Checking for a literal space covers that: a
// break run's final byte is always a space, tab, or bare CR, and only the
// space is a trigger. A sentence break can reach this too: it consumes
// the whitespace run after the terminal punctuation, and that run ends in
// a tab just as easily ("Foo.\tBar@b.co" splits, since "B" starts a
// plausible sentence).
func filterLineStartHazards(text string, breaks []segment.Span) []segment.Span {
	if len(breaks) == 0 {
		return breaks
	}
	out := breaks[:0:0]
	for _, b := range breaks {
		after := text[b.End:]
		if blockmap.MarkerLineStart(after) {
			continue
		}
		if b.End > b.Start && text[b.End-1] != ' ' && linkifyTokenStart.MatchString(after) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// canonicalRunRE matches a maximal run of spaces, tabs, and/or bare '\r'
// bytes — the same shape wordBreaks and clauseBreaks scan (see
// isWrapRunByte). Used by canonicalizeForWidth to measure width
// consistently with how such a run actually behaves once a cut lands
// there; canonicalizeForWidth itself applies the same "must contain a
// real space/tab" rule wordBreaks does before treating a match as
// collapsible (a pure '\r' run is ordinary, unbreakable literal content,
// not one-rune-wide padding).
var canonicalRunRE = regexp.MustCompile(`[ \t\r]+`)

// canonicalizeForWidth returns text with every canonicalRunRE match
// collapsed to a single space, except inside a no-break span
// (segment.NoBreakSpans) — e.g. a run of spaces inside inline code is
// significant, literal content, not padding it would be safe to measure
// as narrower than it really is. It is used only by fitLen, purely to
// decide *whether and where* a cut is needed; it never produces real
// output text (see fitLen's doc comment for why the actual, uncollapsed
// bytes must reach wrapRanked instead).
//
// This measurement-only indirection — not literally rewriting the text
// wrapRanked cuts — is required, not just a simplification: an earlier
// version of this pipeline *did* rewrite the real cluster text before
// wrapping (to fix exactly the width-consistency problem described
// below), which in turn broke a different, narrower case — a cluster
// ending in a multi-character run immediately before a hard-break marker
// gets attached (see attachMarker): rewriting that trailing run to a
// literal ' ' changed how the marker re-attached on the *next* reformat
// (hardBreakBrRE only skips leading space/tab before "<br>", never a bare
// '\r'), an idempotency break found by FuzzFormat on a ModeWrap input
// whose trailing whitespace run (2 spaces, a bare '\r', 2 more spaces)
// sat directly before a hard break. Measuring width against a canonical
// form while cutting the real, unmodified text sidesteps that: wordBreaks
// already fully consumes whatever a chosen cut's run actually contains
// (see its own doc comment), so the *real* text only ever loses a run
// that is genuinely cut, never one left to become a line's trailing edge
// through a rewrite that never needed to happen there.
//
// The width-consistency problem itself: a raw source line can carry a
// literal multi-character whitespace run mid-line that mdreflow never had
// a reason to touch before — joinClusterLines only normalizes whitespace
// *between* joined lines (to exactly one space), never within a single
// one. That is fine as long as nothing else in the pipeline measures
// distances across it. But wrapRanked's cuts do: any multi-character run
// wrapRanked happens to choose as a cut point already collapses to a
// single space once rejoined (an inter-line boundary, per
// joinClusterLines) — while a *different*, uncut run elsewhere in the
// same cluster text stays exactly as wide as it started. On reparse, the
// first run is gone (it is now between two separate output lines, joined
// back to one space) but the second is still there, so the two passes
// would measure different rune widths for the same logical content and
// could land a cut in a different place — found by FuzzFormat on a
// ModeSentence/MaxWidth input with two multi-space runs, one at the
// eventual cut point and one not. Measuring every non-protected run as
// canonically one rune wide, everywhere, removes the asymmetry: both
// passes then measure the same effective width, regardless of how many
// literal whitespace bytes either pass's real text happens to have there.
func canonicalizeForWidth(text string) string {
	noBreak := segment.NoBreakSpans(text)
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range canonicalRunRE.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		b.WriteString(text[last:start])
		hasCore := strings.ContainsAny(text[start:end], " \t")
		if hasCore && end-start > 1 && !spanContains(noBreak, start) {
			b.WriteByte(' ')
		} else {
			b.WriteString(text[start:end])
		}
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

// widthMeasurer answers fitLen-shaped width queries for slices of one
// cluster text in O(1)-ish time after an O(n) setup, replacing the
// per-candidate re-parse that made wrapRanked roughly cubic (30s on a
// 2000-word paragraph). It computes segment.NoBreakSpans and a prefix-sum
// table of canonicalized rune widths ONCE for the whole text, so a
// candidate line's canonical width is a subtraction rather than a rescan.
//
// Measurement semantics differ from fitLen in one deliberate way: run
// protection is decided by the CLUSTER-GLOBAL no-break spans, not by
// re-parsing each truncated candidate substring. The two agree everywhere
// a construct's closing delimiter falls on the same side of the cut as
// its opener — and a cut can never land inside a global span, since
// candidates are filtered against those same spans before wrapRanked
// runs — so disagreement is confined to link-shaped text whose
// interpretation depends on bytes beyond the truncation point (e.g. an
// unbalanced "](…" that a truncation happens to balance). Global spans
// are also the *consistent* choice: both format passes measure identical
// widths for identical content, which is what idempotency wants.
// Verified against the previous implementation by a full-corpus
// differential run (fixtures + fuzz seeds, byte-identical) plus a fuzz
// soak.
type widthMeasurer struct {
	text    string
	noBreak []segment.Span
	prefix  []int // prefix[i] = canonical rune width of text[:i]
}

func newWidthMeasurer(text string) *widthMeasurer {
	m := &widthMeasurer{
		text:    text,
		noBreak: segment.NoBreakSpans(text),
		prefix:  make([]int, len(text)+1),
	}
	w := 0
	fillLiteral := func(from, to int) {
		for i := from; i < to; {
			_, size := utf8.DecodeRuneInString(text[i:])
			for j := i + 1; j <= i+size; j++ {
				m.prefix[j] = w + 1
			}
			w++
			i += size
		}
	}
	last := 0
	for _, match := range canonicalRunRE.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		fillLiteral(last, start)
		hasCore := strings.ContainsAny(text[start:end], " \t")
		if hasCore && end-start > 1 && !spanContains(m.noBreak, start) {
			// Collapsible run: measures as a single space, exactly as
			// canonicalizeForWidth rewrites it.
			w++
			for j := start + 1; j <= end; j++ {
				m.prefix[j] = w
			}
		} else {
			fillLiteral(start, end)
		}
		last = end
	}
	fillLiteral(last, len(text))
	return m
}

// width returns the canonical rune width of text[a:b]. Exact whenever a
// and b sit on the run/rune boundaries wrapRanked queries (cut ends, run
// starts, len(text)).
func (m *widthMeasurer) width(a, b int) int { return m.prefix[b] - m.prefix[a] }

// canonSlice is canonicalizeForWidth for text[a:b], with run protection
// decided by the global spans (see the type comment). a and b fall on
// run boundaries for every wrapRanked query, so no run straddles either
// end.
func (m *widthMeasurer) canonSlice(a, b int) string {
	sub := m.text[a:b]
	var sb strings.Builder
	sb.Grow(len(sub))
	last := 0
	for _, match := range canonicalRunRE.FindAllStringIndex(sub, -1) {
		start, end := match[0], match[1]
		sb.WriteString(sub[last:start])
		hasCore := strings.ContainsAny(sub[start:end], " \t")
		if hasCore && end-start > 1 && !spanContains(m.noBreak, a+start) {
			sb.WriteByte(' ')
		} else {
			sb.WriteString(sub[start:end])
		}
		last = end
	}
	sb.WriteString(sub[last:])
	return sb.String()
}

// escapeDeltaMax bounds how many runes escapeBlockInterrupt can add to
// any line starting at byte a: a fence-opener-shaped line gains, per
// character of its leading backtick/tilde run (see the escape loop in
// escapeBlockInterrupt), one rune for a tilde ("~" -> "\~") or four for a
// backtick ("`" -> "&#96;", 1 rune growing to 5); every other trigger gains
// exactly one. The leading run depends only on the line's start, so the
// bound does too.
func (m *widthMeasurer) escapeDeltaMax(a int) int {
	n := 0
	for i := a; i < len(m.text) && (m.text[i] == '`' || m.text[i] == '~'); i++ {
		if m.text[i] == '`' {
			n += 4
		} else {
			n++
		}
	}
	return max(n, 1)
}

// fits reports whether the line text[a:b] fits within maxWidth once
// escaped: the prefix-table width answers all but the ambiguous band
// [maxWidth-escapeDeltaMax, maxWidth], where the real escape is simulated
// exactly as fitLen would.
//
// A fence-opener-shaped [a:b) is not specially handled here, unlike an
// earlier version of this function: see writeParagraph's flush, which
// pre-escapes a hard-break cluster's own leading fence-opener run before
// computeLines (hence wrapRanked, hence this) ever sees it, and
// filterUnsafeLineEnds's fence-suffix rule, which stops any *other*
// position from ever becoming one via a width cut. So text[a] is never
// '`' or '~' for any (a, b) this function is actually asked about, and
// escapeDeltaMax's fence-aware bound (kept for defense in depth) never
// exercises its interesting case.
func (m *widthMeasurer) fits(a, b, maxWidth int) bool {
	w := m.width(a, b)
	if w > maxWidth {
		return false
	}
	if w+m.escapeDeltaMax(a) <= maxWidth {
		return true
	}
	return runeLen(escapeBlockInterrupt(m.canonSlice(a, b), true, "", true)) <= maxWidth
}

// lastFit returns the largest i >= from with fits(a, cands[i].Start), or
// -1. Canonical width is strictly increasing across candidates (any two
// are separated by at least one non-run rune), so the width cutoff is
// found by binary search and only the escape-delta band below it — at
// most escapeDeltaMax+1 candidates — needs exact evaluation.
func (m *widthMeasurer) lastFit(cands []segment.Span, from, a, maxWidth int) int {
	hi := sort.Search(len(cands)-from, func(k int) bool {
		return m.width(a, cands[from+k].Start) > maxWidth
	}) + from - 1
	dmax := m.escapeDeltaMax(a)
	for i := hi; i >= from; i-- {
		w := m.width(a, cands[i].Start)
		if w+dmax <= maxWidth {
			return i
		}
		if runeLen(escapeBlockInterrupt(m.canonSlice(a, cands[i].Start), true, "", true)) <= maxWidth {
			return i
		}
	}
	return -1
}

// wrapRanked repeatedly cuts text into lines no wider than maxWidth runes,
// consuming a break candidate (rather than emitting it) at each cut —
// matching how a sentence break or a wrapped word boundary both discard the
// whitespace/punctuation-space they cut at, replacing it with a newline.
// primary and secondary are both candidate-break lists, sorted by Start and
// non-overlapping; primary is preferred whenever a primary candidate fits
// within the current line's width budget, and secondary is only consulted
// when no primary candidate does (this is how ModeSentence's MaxWidth
// prefers a clause boundary over a plain word boundary — see computeLines).
//
// Each candidate line's width is measured *as if escapeBlockInterrupt will
// escape it* (widthMeasurer.fits — fitLen's semantics answered from a
// per-cluster prefix table, see widthMeasurer's doc comment — not a plain
// rune count): the render loop in
// writeParagraph backslash-escapes any output line that would otherwise be
// misparsed as a new block on reparse (e.g. a line reflow made start with
// "* ", which would misparse as a bullet marker) — an escape that adds a
// byte *after* this function has already decided where to cut. Budgeting
// for that up front, rather than measuring the pre-escape text, is what
// keeps wrapping idempotent: found by FuzzFormat on ":::\n*\n0" in
// ModeWrap at width 3, where the pre-escape cluster text "* 0" fit
// exactly at the limit and was kept on one line, but the escaped output
// "\* 0" (one byte longer) no longer fit on reparse, so the second pass
// wrapped it differently than the first. Simulating the escape with
// isFirstLine always true (see fitLen) is deliberately the conservative
// direction — it can only make a line's simulated width an over-estimate
// of its real one, never an under-estimate, so a line this function
// accepts as fitting is guaranteed to actually fit once truly escaped,
// on every pass, regardless of that line's real position in the
// paragraph.
//
// When nothing in either list fits within maxWidth (a single word, or
// no-break span, already wider than the limit — design.md: "overflow"),
// the earliest remaining candidate from either list is used as a forced
// cut, so the line does not grow without bound merely because it already
// overflowed once; if no candidate remains at all, the rest of text is
// emitted as one final (possibly overlong) line.
func wrapRanked(text string, maxWidth int, primary, secondary []segment.Span) []string {
	m := newWidthMeasurer(text)
	var out []string
	lineStart := 0
	pi, si := 0, 0
	for {
		for pi < len(primary) && primary[pi].Start < lineStart {
			pi++
		}
		for si < len(secondary) && secondary[si].Start < lineStart {
			si++
		}

		if m.fits(lineStart, len(text), maxWidth) {
			// Everything left already fits on one line: no more cuts are
			// needed, regardless of how many break candidates remain.
			break
		}

		bestPrimary := m.lastFit(primary, pi, lineStart, maxWidth)
		bestSecondary := m.lastFit(secondary, si, lineStart, maxWidth)

		if bestPrimary >= 0 {
			cut := primary[bestPrimary]
			out = append(out, text[lineStart:cut.Start])
			lineStart = cut.End
			pi = bestPrimary + 1
			continue
		}
		if bestSecondary >= 0 {
			cut := secondary[bestSecondary]
			out = append(out, text[lineStart:cut.Start])
			lineStart = cut.End
			si = bestSecondary + 1
			continue
		}

		// Nothing fits within maxWidth: force a cut at the earliest
		// remaining candidate from either list, if any.
		fromPrimary := pi < len(primary)
		fromSecondary := si < len(secondary)
		switch {
		case fromPrimary && (!fromSecondary || primary[pi].Start <= secondary[si].Start):
			cut := primary[pi]
			out = append(out, text[lineStart:cut.Start])
			lineStart = cut.End
			pi++
		case fromSecondary:
			cut := secondary[si]
			out = append(out, text[lineStart:cut.Start])
			lineStart = cut.End
			si++
		default:
			// No candidates left at all: the remainder overflows as one
			// final line; stop, the post-loop append below emits it.
			return appendTail(out, text, lineStart)
		}
	}
	return appendTail(out, text, lineStart)
}

// appendTail appends text[lineStart:] to out as one final line, unless a
// cut already consumed all the way to the end of text (lineStart ==
// len(text)): appending an empty string there would add a spurious,
// content-free trailing line that then absorbs a hard-break marker
// (attachMarker attaches to wrapRanked's *last* returned line) instead of
// the real last word — an idempotency break found by FuzzFormat on
// "1BACA9 07+  \r" in ModeWrap at width 2, where the final forced cut's
// span reached exactly to len(text), and the resulting spurious empty
// line took the "<br>" marker meant for "07+", producing a paragraph's
// worth of lines that a second reformat pass consolidated differently.
func appendTail(out []string, text string, lineStart int) []string {
	if lineStart == len(text) {
		return out
	}
	return append(out, text[lineStart:])
}

// wordBreaks returns text's word-boundary break candidates: every maximal
// run of one or more ASCII spaces and/or tabs not inside a no-break span.
// A run, not a single space byte, is required: joinClusterLines collapses
// all *inter-line* whitespace in a hard-break cluster's joined text to
// exactly one space, but whitespace *within* a single original source line
// passes through untouched, so a real source line like "a  b" (two spaces)
// still reaches here with its double space intact. Treating each space
// byte in that run as its own one-byte break candidate — an earlier
// version of this function did — cuts inside the run instead of consuming
// all of it, leaving a leftover leading space on the next line: an
// idempotency break found by FuzzFormat on "a%  BBa2y*x)00" in ModeWrap.
//
// Tabs are included in the run for the same reason: CommonMark strips a
// paragraph continuation line's entire leading run of spaces/tabs as
// insignificant, however it is spelled, so a cut that left a literal tab
// as the first byte of a new line would silently lose that tab on reparse
// — an idempotency break found by FuzzFormat on ModeWrap input containing
// a " \t" run at a cut point, where the space was consumed as the break
// but the tab, not being ' ', was left as the next line's leading byte and
// then vanished when goldmark reparsed the output.
//
// A break's run may also contain bare '\r' bytes (ones not immediately
// followed by '\n') interleaved with the space/tab, even though a bare
// '\r' is not itself insignificant CommonMark whitespace: goldmark's own
// line scanner already treats a lone '\r' as ordinary literal content
// wherever it doesn't interact with a line boundary (see
// filterUnsafeLineEnds's doc comment), but it behaves unpredictably once
// mdreflow's own injected '\n' lands immediately next to one — found by
// FuzzFormat on ModeWrap input with a " \r" run at a cut point: the space
// was consumed as the break, leaving '\r' as the next line's leading
// byte, and that leading '\r' silently vanished when goldmark reparsed
// the output (apparently absorbed as part of a multi-byte line-ending
// sequence with mdreflow's own adjacent '\n', the same quirky
// concatenation stripLineEnding's doc comment already documents for a
// *trailing* run of these).
//
// A run must contain at least one real space or tab to count as a break
// at all — a run of *only* '\r' characters is not a real word boundary
// and must not be treated as one: found by FuzzFormat on "T h\r\rucraZ"
// (an earlier version of this function treated bare '\r' as a break
// character in its own right) treating "\r\r" between "h" and "ucraZ" —
// content with no actual separating whitespace at all — as a break,
// silently discarding it as if it were insignificant padding and gluing
// "h" and "ucraZ" together with nothing between them, which a second
// reformat pass then rejoined with a real inserted space instead. And the
// run must be the *maximal* contiguous stretch of space/tab/'\r' bytes,
// not just a space/tab core plus CR touching only one identified core: an
// earlier version that only extended a discovered space/tab run outward
// over adjacent '\r' bytes missed a run like " \r " (space, CR, space) —
// the extension reached the CR but stopped there, leaving the second
// space to start a *separate* one-byte break candidate, which (if
// wrapRanked picked the first candidate as its cut) left that second
// space dangling as the next line's leading byte — a byte CommonMark
// again treats as insignificant continuation-line whitespace and so
// silently drops on reparse, found by FuzzFormat on "B00CA077 \r 80...".
// Scanning the whole run in one pass (isWrapRunByte) up front avoids
// needing to reconcile adjacent candidates after the fact.
//
// This is ModeWrap's break-point strategy, and ModeSentence's MaxWidth
// fallback when no clause boundary is available.
func wordBreaks(text string) []segment.Span {
	noBreak := segment.NoBreakSpans(text)
	var out []segment.Span
	i := 0
	for i < len(text) {
		if !isWrapRunByte(text[i]) {
			i++
			continue
		}
		start := i
		hasCore := false
		for i < len(text) && isWrapRunByte(text[i]) {
			if text[i] == ' ' || text[i] == '\t' {
				hasCore = true
			}
			i++
		}
		if !hasCore || spanContains(noBreak, start) {
			continue
		}
		out = append(out, segment.Span{Start: start, End: i})
	}
	return out
}

// isWrapRunByte reports whether b is one of the bytes a wordBreaks or
// clauseBreaks whitespace run may contain: an ASCII space, a tab, or a
// bare '\r' — see wordBreaks's doc comment for why a bare '\r' is
// included, and why a run needs at least one real space/tab to actually
// count as a break.
func isWrapRunByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r'
}

// clauseBreaksRE matches a comma or semicolon followed by a maximal run
// of one or more spaces, tabs, and/or bare '\r' bytes: the clause-
// boundary candidates ModeSentence's MaxWidth prefers over a plain word
// boundary (docs/design.md's Modes table). The punctuation itself stays
// at the end of the preceding line; the entire following run is consumed
// as the break, provided it contains at least one real space or tab (see
// clauseBreaks) — a single space is not enough on its own to justify a
// dedicated regex capture, for the same reasons given in wordBreaks's doc
// comment (a source line can carry more than one literal whitespace byte
// here, untouched by joinClusterLines, and a run left partly uncut can
// silently misbehave on reparse).
var clauseBreaksRE = regexp.MustCompile(`[,;][ \t\r]+`)

// entityRefTailRE matches an HTML entity or numeric character reference
// ending exactly at the string's end ("&" then digits/letters then ";")
// — used to recognize when a semicolon clauseBreaksRE matched is markup
// syntax, not clause-terminal punctuation.
var entityRefTailRE = regexp.MustCompile(`&#?[0-9A-Za-z]+;$`)

// clauseBreaks returns text's clause-boundary break candidates, excluding
// any match whose run is pure '\r' (see wordBreaks's doc comment — that
// is not a real clause boundary), that lands inside a no-break span (e.g.
// a comma inside inline code), or whose semicolon terminates an HTML
// entity/numeric character reference rather than actual prose punctuation
// (entityRefTailRE) — e.g. "&#96;", never real clause punctuation despite
// ending in ';'.
//
// The last exclusion matters beyond ordinary prose containing a literal
// reference like "caf&eacute;, more text" (a pre-existing quirk of no
// consequence there, since the same text is present and read the same way
// on every pass): escapeBlockInterrupt's fence-opener branch emits
// "&#96;" for every escaped backtick, so a paragraph whose pre-escape text
// has no semicolon at all can gain one, mid-cluster, purely from that
// escape — one this function would otherwise treat as a *new* clause
// break unavailable to the pre-escape planning pass. Since a clause break
// is preferred over a plain word break whenever both fit
// (docs/design.md's Modes table, computeLines), the spurious candidate
// can steer ModeSentence's MaxWidth wrapping to a different cut than the
// pre-escape pass chose — an idempotency break. Found by FuzzFormat
// (ModeSentence, MaxWidth 17) on " \r``` Z C00": pass 1, still working
// from unescaped text, has no semicolon to prefer and wraps after "Z"
// ("``` Z" / "C00"); pass 2, reparsing the escaped "&#96;&#96;&#96; Z
// C00", finds the third entity's terminating ';' immediately before a
// space and treats it as a preferred clause break, wrapping right after
// the run instead ("&#96;&#96;&#96;" / "Z C00").
func clauseBreaks(text string) []segment.Span {
	noBreak := segment.NoBreakSpans(text)
	var out []segment.Span
	for _, m := range clauseBreaksRE.FindAllStringIndex(text, -1) {
		punct, start, end := m[0], m[0]+1, m[1]
		if !strings.ContainsAny(text[start:end], " \t") || spanContains(noBreak, start) {
			continue
		}
		if text[punct] == ';' && entityRefTailRE.MatchString(text[:start]) {
			continue
		}
		out = append(out, segment.Span{Start: start, End: end})
	}
	return out
}

// spanContains reports whether pos falls inside any of spans.
func spanContains(spans []segment.Span, pos int) bool {
	for _, sp := range spans {
		if pos >= sp.Start && pos < sp.End {
			return true
		}
	}
	return false
}

// runeLen returns the rune count of s: mdreflow measures MaxWidth in
// runes, not bytes or Unicode grapheme clusters / East-Asian display width
// — a pragmatic simplification documented on mdreflow.Options.MaxWidth,
// the same choice every shipping line-wrap tool starts from. Full
// grapheme-cluster/width-aware measurement is a documented future
// refinement, not implemented here.
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// fitLen returns the rune count s would effectively have once (a) any
// whitespace run measures as canonicalizeForWidth would measure it — see
// its doc comment for why *measuring* a run as one rune wide, without
// literally rewriting the real text to match, is required — and (b) the
// result is emitted as a paragraph output line and escapeBlockInterrupt
// escapes it. isFirstLine is always passed as true: that is the more
// conservative of escapeBlockInterrupt's two behaviors (it only ever
// escapes a strict superset of what isFirstLine=false does, via the extra
// htmlBlockAnyOpenerRE check), so the simulated length here is always >=
// the real, final escaped length of any line s could become, whatever its
// actual position in the paragraph turns out to be. Never an
// under-estimate, so a candidate this accepts is guaranteed to still fit
// after the real escaping pass.
//
// firstLinePrefix is passed as "" here, unlike the real output pass in
// writeParagraph: this helper has no way to know, for a candidate cut
// under consideration, what container marker (if any) will actually sit
// before it once a final cut is chosen, so it cannot simulate
// escapeBlockInterrupt's firstLinePrefix-aware thematic-break check
// precisely. This narrows (does not violate) the "never an
// under-estimate" guarantee above only for that one rare trigger — a
// wrapped first line landing right after a container marker such that
// the two jointly form a thematic break — where a candidate this
// function accepts could end up one byte longer once the real prefix is
// known and escapeBlockInterrupt reacts to it. That is a line-quality
// nicety, not a correctness one: the real escaping pass in writeParagraph
// always runs with the true prefix regardless of what fitLen estimated,
// so no idempotency or render-preservation guarantee depends on this
// function's precision here.
//
// Deliberately stays canonical (not exact-fence-aware, unlike
// widthMeasurer.exactFenceEscapedWidth) even for a fence-opener-shaped s:
// this measures the "keep the whole cluster as one line" verdict, and
// canonical measurement of that verdict is a stable fixpoint across
// passes in a way an exact one is not. If this text is kept together
// (verdict: fits), it is emitted unchanged and re-read on the next pass
// as ordinary, no-longer-fence-shaped text (the fence branch only ever
// escapes the leading run) — whose own fitLen also canonicalizes the same
// whitespace, arriving at the identical number. If instead the text gets
// split later (wrapRanked, via widthMeasurer), the pieces are ordinary
// paragraph continuation lines with no hard break between them, so the
// *next* pass's joinClusterLines rejoins them with a single space before
// that pass's own fitLen ever runs on them again — which is exactly what
// canonical measurement already assumes happens to any uncut interior
// run, fence or not. An exact/raw verdict here does not enjoy either
// fixpoint: it can force a split canonical measurement alone would not
// have needed, and that split's own rejoin-and-recanonicalize on the next
// pass can undo it — found by constructing exactly that shape (ModeWrap,
// MaxWidth 17, a hard-break cluster's second line "```  b" indented 4+
// spaces in the source, i.e. ordinary paragraph continuation text until
// reflow's own dedent exposes the leading backticks): an exact fitLen
// here forces a split into "```" + "b", which the next pass — no longer
// seeing a fence shape once escaped — rejoins and re-measures as fitting
// on one line after all, disagreeing with pass 1. Leaving fitLen
// canonical accepts the pre-existing, already-documented tradeoff instead
// (a kept-together real line can land a few runes past MaxWidth when it
// contains an interior uncut whitespace run — see canonicalizeForWidth),
// which is a real, independent gap but not one this function's escape
// awareness should try to close by itself; widthMeasurer.fits/lastFit's
// exact fence check (used only once a split is already required) is the
// safe place for exactness, since candidates it rejects fall back to
// *earlier* candidates within the very same wrapRanked call rather than
// to a decision a whole reformat pass later has to reconcile against.
func fitLen(s string) int {
	return runeLen(escapeBlockInterrupt(canonicalizeForWidth(s), true, "", true))
}

// splitSentences takes one cluster's already-joined prose, asks seg for
// candidate breaks, drops any that land inside a no-break span (inline
// code, links, images, footnote refs, ...), and cuts text at what remains.
// Each returned string is one output line: whitespace-trimmed, never
// containing the break whitespace itself.
func splitSentences(text string, seg Segmenter) []string {
	if text == "" {
		return []string{""}
	}
	breaks := filterLineStartHazards(text, filterBreaks(seg.Breaks(text), segment.NoBreakSpans(text)))

	out := make([]string, 0, len(breaks)+1)
	prev := 0
	for _, b := range breaks {
		out = append(out, text[prev:b.Start])
		prev = b.End
	}
	out = append(out, text[prev:])
	return out
}

// filterBreaks removes any break overlapping a no-break span.
func filterBreaks(breaks, noBreak []segment.Span) []segment.Span {
	if len(noBreak) == 0 {
		return breaks
	}
	out := breaks[:0:0]
	for _, b := range breaks {
		blocked := false
		for _, nb := range noBreak {
			if b.Start < nb.End && b.End > nb.Start {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, b)
		}
	}
	return out
}

// blockInterruptTriggers matches text that, if it landed at the very start
// of a fresh line, CommonMark would recognize as a new block that
// interrupts an open paragraph: an ATX heading, a blockquote marker, or a
// list item marker, or an HTML-block opener. Every trigger here starts
// with ASCII punctuation, which backslash-escapes cleanly (see
// escapeBlockInterrupt); the ordered-list marker is handled separately by
// orderedListRE, since its trigger starts with a *digit*, which
// CommonMark does not allow backslash-escaping at all. Thematic breaks
// and fenced code openers are also handled separately (isThematicBreak,
// isFenceOpener): thematic breaks need a backreference ("same character
// repeated") Go's RE2 engine cannot express, and a fenced code opener
// needs to inspect the rest of the line (a backtick fence's info string
// may not itself contain a backtick — a plain regex anchored only at the
// start over-fires on that, found by FuzzFormat on a triple-backtick run
// followed by "00" and a trailing double-backtick; neither constraint
// fits a single linear regex alternative here. (Setext heading underlines
// share the "-"/"=" triggers already covered by isThematicBreak.) A link
// reference definition's "[label]: destination" opener is also handled
// separately (isCompleteLinkRefDefLine), not folded in here: unlike every other
// trigger in this list, it needs an end anchor as well as a start anchor
// — see that variable's doc comment for why.
//
// The HTML-block alternative here only covers CommonMark's "types 1-6"
// openers (script/pre/style/textarea; "<!--"; "<?"; "<!" + uppercase;
// "<![CDATA["; and a fixed list of ~60 block-level tag names,
// htmlBlockTagNames): per spec, HTML blocks of types 1-6 can interrupt an
// open paragraph from *any* line position, the same as the other
// triggers in this list. Type 7 (any *other* tag name, as a complete
// open/close tag alone on a line) is different — it explicitly *cannot*
// interrupt a paragraph — and is handled separately by
// htmlBlockAnyOpenerRE, applied only at a paragraph's first output line
// (see escapeBlockInterrupt): found by FuzzFormat that folding type 7
// into this always-applies set breaks *legitimate* inline "<br>" (and any
// other non-type-1-6 tag) used mid-paragraph, e.g. as a hard-break
// marker's own text — "<br>" itself matches "<" + letter, and
// escaping it there defeats the very hard break it was supposed to be.
//
// goldmark's actual leniency for type 7 turned out broader than the
// spec's wording suggests, confirmed directly (not derived from reading
// the spec): a line starting "<" followed immediately by "/" or *any*
// ASCII letter reliably opens an HTML block at a fresh block-start
// position regardless of whether the tag name is a recognized one or the
// rest of the line is even valid tag syntax (e.g. "<A B>" and "<div foo"
// — an unrecognized tag name and an incomplete attribute list,
// respectively — both still triggered it). Found by FuzzFormat on
// "</\nA>": joining "</" and "A>" produced "</ A>", which CommonMark's own
// condition 7 doesn't strictly license (a closing tag's grammar has no
// space between "</" and the tag name) but goldmark accepts anyway,
// turning an escaped, inert paragraph ("<p>&lt;/ A&gt;</p>") into a raw,
// unescaped HTML block. The same "</" + space leniency also applies to a
// *known* tag name (types 1/6, not just type 7): "<(/ *)?(...)" allows
// optional spaces between "/" and the tag name — found by FuzzFormat on
// "</\nP", whose joined form "</ P" opens a block because "p" is in
// htmlBlockTagNames, the same leniency confirmed separately for
// htmlBlockAnyOpenerRE's unrecognized-name case.
var blockInterruptTriggers = regexp.MustCompile(
	`(?i)^(#{1,6}(\s|$)` + // ATX heading
		`|>` + // blockquote
		`|[-*+](\s|$)` + // bullet list marker
		`|<!` + // HTML comment / doctype / CDATA (types 2, 4, 5)
		`|<\?` + // processing instruction (type 3)
		`|<(/ *)?(` + htmlBlockTagNames + `)(\s|/?>|$))`, // known tag name (types 1, 6)
)

// htmlBlockTagNames is CommonMark's HTML-block "type 6" list of
// block-level tag names, merged with the "type 1" special tag names
// (script, pre, style, textarea) — see blockInterruptTriggers's doc
// comment for why both are folded into one always-applies set.
const htmlBlockTagNames = `address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul|script|pre|style|textarea`

// htmlBlockAnyOpenerRE matches a line that is, in its entirety (only
// trailing spaces/tabs allowed after), a complete open or close tag of
// any tag name — CommonMark HTML-block type 7, which can only ever matter
// at a fresh block-start position (a paragraph's own first output line);
// see blockInterruptTriggers's doc comment for why type 7 is handled
// separately from types 1-6.
//
// The whole-line anchoring (both "^" and "$") is required, not just a
// leading "<" check: type 7 specifically needs the tag to be *complete*
// with *only* whitespace after it — found by FuzzFormat on the
// genuinely unmodified, single-line paragraph "<A>0": an earlier,
// prefix-only version of this regex ("^<[/A-Za-z]") matched it and
// escaped it, even though this exact source, untouched by any reflow
// split or join, was never actually a type-7 candidate at all (goldmark
// parses it as an ordinary paragraph, "<A>" as inline HTML followed by
// literal "0" — there is no "only whitespace after the tag" here). The
// closing-tag alternative allows whitespace between "</" and the tag
// name ("</\s*name"), not just after it: confirmed against goldmark (not
// assumed from the spec's stricter closing-tag grammar) that it accepts
// that lenient shape too — see htmlBlockAnyOpenerRE's use, and the
// "</\nA>" fuzz find that first motivated this whole check, whose joined
// form is "</ A>".
//
// The separator directly after an open tag's name (before any
// attribute-like content) is a literal " " only, not "\s" (which would
// also match tab): confirmed directly against goldmark, not assumed, that
// "<A >" (space) opens a block but "<A\t>" (tab) does not, in the exact
// same position — found by FuzzFormat on the genuinely unmodified,
// single-line paragraph "<A\t>", which an earlier "\s"-based version of
// this regex wrongly flagged. The trailing whitespace allowed *after* a
// complete tag is the same story: a trailing space still counts as
// "nothing but whitespace to end of line" (blocks), but a trailing tab
// does not (stays inline) — confirmed on "<A>\t" (an unmodified,
// single-line paragraph an earlier "[ \t]*$"-based version of this regex
// wrongly flagged the same way). A tab is disqualifying *anywhere* in the
// tag, not just in those two specific positions: "<A \t>" (a space then a
// tab before ">", inside what would otherwise be a valid attribute
// section) also stays inline — confirmed on that exact, unmodified input,
// which an earlier version of this regex (permitting tabs inside the
// interior "any non-'<>' character" attribute class) also wrongly
// flagged. So the whole regex uses literal " " throughout, never "\s" or
// a character class that admits '\t', including inside the attribute
// section and the closing tag's internal whitespace.
//
// The closing-tag alternative also allows arbitrary non-"<>" content
// between the tag name and the final ">", the same way the open-tag
// alternative already does for attributes: a closing tag is not required
// to contain *only* the name and whitespace before ">", confirmed
// directly against goldmark, not assumed from CommonMark's own (stricter)
// closing-tag grammar — found by FuzzFormat on "</ A\nA>", whose joined
// form "</ A A>" opens a block despite the extra "A" between the tag name
// and ">", which an earlier, stricter version of this alternative
// (allowing only trailing whitespace there) did not match at all.
var htmlBlockAnyOpenerRE = regexp.MustCompile(`(?i)^(<[A-Za-z][A-Za-z0-9-]*( [^<>\t]*)?/?>|</ *[A-Za-z][A-Za-z0-9-]*( [^<>\t]*)?/?>) *$`)

// The empirical notes below were accumulated on linkRefDefOpenerRE, the
// hand-mirrored-grammar predecessor of isCompleteLinkRefDefLine (which
// answers the same question by parsing the line with goldmark itself).
// They remain the record of WHY the question is subtle:
//
// linkRefDefOpenerRE matched a link-reference-definition-shaped
// "[label]: destination" line that is *complete on this line alone*: the
// whole remainder after "[label]:" is (optional space) one bare
// destination token (no ASCII whitespace, doesn't start with "<") and
// (optional space) — nothing else. This must match the *entire* line
// (anchored at both ends), not just the opening: whether "[label]:..." is
// actually dangerous turns out to depend on precisely this, confirmed
// empirically (not assumed) against three FuzzFormat finds:
//
//   - "[0]:!" — bare destination "!", nothing else on the line — *is*
//     consumed as a complete definition (confirmed even as a paragraph's
//     own sole, final line, with nothing at all following it: the
//     destination is self-sufficient, it does not need a following line).
//     Splitting "[0]:! 0" into "[0]:!" and "0" (found by FuzzFormat)
//     triggers exactly this.
//   - "[0]:0 !" — destination "0" already completes the definition
//     grammar, but "!" trails it on the *same* line where only
//     whitespace-or-title-start is allowed — this *disqualifies the
//     whole line*, definition included, falling through to an ordinary
//     paragraph regardless of what follows on later lines (confirmed:
//     appending more lines after it changes nothing). Escaping it anyway
//     (an earlier, wrong version of this check tried gating on line
//     position instead of this) defeated a *working* "[0]" reference link
//     resolved against an earlier real definition — found by FuzzFormat on
//     "[0]:0 \"\"\n[0]:0 ! 00000000".
//   - "[0]:" — nothing at all after the colon — incomplete, so this
//     regex correctly does not match it (no token for `[^\s<][^\s]*`).
//     "Incomplete" is NOT "safe", though: an emitted line consisting of
//     only a bare opener completes using the NEXT line as its
//     destination, so it needs its own escape — see
//     bareLinkRefDefOpenerLineRE below. (For non-caret labels blockmap's
//     whole-paragraph net usually skips such paragraphs first; the
//     caret-label variant reaches emission, and a narrow width cut can
//     isolate the opener — found by FuzzFormat on " [^(]: !y )9.20",
//     seed f0699d6787522cd5, whose MaxWidth cut left " [^(]:" alone and
//     the reparse swallowed "!y" as the definition's destination.)
//
// Footnote-shaped "[^label]:" openers are INCLUDED (an earlier version
// excluded them as "a footnote definition's own legitimate marker"):
// mdreflow's goldmark configuration does not enable the footnote
// extension at all, so "[^label]:" is nothing special to the parser this
// package must stay consistent with — a line-start "[^label]: dest" that
// completes on its own line is consumed as an ordinary link reference
// definition with a caret-leading label (confirmed by AST dump), exactly
// the vanishing-content hazard this escape exists for. Found by
// FuzzFormat on " [^1]: !Y )9.01" (seed 425ffd537fd28733): the source
// line is a plain paragraph (the trailing ")9.01" disqualifies the
// definition), but the sentence split's shorter first line " [^1]: !Y"
// reparses as a real definition and the text vanishes from the paragraph
// flow. The fear the old exclusion encoded — escaping a real footnote
// body's own marker and severing its reference link — cannot fire
// through this regex: a footnote body with prose after the marker never
// matches the complete-on-this-line-alone anchoring (multi-word
// remainder), which is also why testdata/no-break-spans.md's footnote
// fixture reflows identically with the exclusion gone; and a
// single-token "[^1]: word" source line is already an LRD node to this
// parser, passed through byte-for-byte, never reaching this check.
//
// The label body also excludes a raw "[" (not just "]"): a real
// CommonMark link label cannot contain an unescaped "[" either, so
// "[[]: http://0.a" is not actually a link reference definition — and
// escaping it anyway, besides being unnecessary, changed how the rest of
// the line's autolink recognition behaved, found by FuzzFormat on exactly
// that input.
// The `[ \t]{0,3}` leading-indent and `[ \t\f\v]` whitespace classes track
// goldmark's actual acceptance, wider than the spec's space/tab: a
// definition indented up to three columns is still a definition, and a
// form feed after the colon still separates the destination — found by
// FuzzFormat on " [^X]:\f!y 00" (seed 617a8c27848709db), whose split-off
// first line " [^X]:\f!y" reparsed as a complete definition that this
// regex, anchored at column 0 with space/tab-only runs, failed to escape.

// bareLinkRefDefOpenerLineRE matches an emitted line that is ONLY a
// link-reference-definition opener — "[label]:" plus optional trailing
// whitespace, no destination. Such a line is incomplete rather than
// inert: CommonMark completes the definition using the following line as
// its destination, so leaving one at a line start lets the reparse
// swallow the next line's text (see the notes above isCompleteLinkRefDefLine's
// history block,
// third bullet, for the fuzz find). Escaping the bracket renders as the
// same literal text the paragraph showed before.
// The trailing class includes "\r": CR is trailing whitespace to
// goldmark's definition parser ("[0]: \r" followed by a destination line
// still forms a definition, confirmed directly), the same
// goldmark-whitespace-alignment family as isTableDelimiterRowShaped's CR.
var bareLinkRefDefOpenerLineRE = regexp.MustCompile(`^[ \t]{0,3}\[[^\[\]]*\]:[ \t\f\v\r]*$`)

// backtickFenceStart and tildeFenceStart match a fenced-code-block opener's
// leading run of 3+ backticks or tildes, so isFenceOpener can inspect what
// follows.
var (
	backtickFenceStart = regexp.MustCompile("^`{3,}")
	tildeFenceStart    = regexp.MustCompile("^~{3,}")
)

// isFenceOpener reports whether line, placed at the start of a fresh line,
// would open a fenced code block. A tilde fence's info string may contain
// anything (including backticks); a backtick fence's info string must not
// itself contain a backtick — CommonMark disqualifies it as a fence
// opener otherwise, falling back to ordinary text, so escaping it would be
// both unnecessary and (mixed with escapeBlockInterrupt's other triggers)
// arguably a normalization mdreflow otherwise avoids.
func isFenceOpener(line string) bool {
	if m := backtickFenceStart.FindStringIndex(line); m != nil {
		return !strings.ContainsRune(line[m[1]:], '`')
	}
	return tildeFenceStart.MatchString(line)
}

// fenceOpenerRunLen returns the length of line's leading run of backticks
// and/or tildes — the run isFenceOpener recognizes and that
// escapeBlockInterrupt's fence-opener branch (and fenceEscapeNeutralize)
// both walk. Factored out so the two stay in lockstep.
func fenceOpenerRunLen(line string) int {
	i := 0
	for i < len(line) && (line[i] == '`' || line[i] == '~') {
		i++
	}
	return i
}

// escapeFenceOpenerRun returns line with its leading fence-opener run
// (isFenceOpener) replaced by exactly the bytes escapeBlockInterrupt's
// fence-opener branch would produce for it — a backtick as the HTML
// character reference "&#96;" (never a backtick byte, so it can never
// pair as a code-span delimiter on any reparse), a tilde as "\~" — with
// the rest of line untouched. If line is not fence-opener-shaped, line is
// returned unchanged. Shared by escapeBlockInterrupt itself and by
// writeParagraph's flush (which pre-escapes a hard-break cluster's own
// leading run before computeLines ever measures or wraps it — see flush's
// call site) so the two can never drift apart.
func escapeFenceOpenerRun(line string) string {
	if !isFenceOpener(line) {
		return line
	}
	n := fenceOpenerRunLen(line)
	var b strings.Builder
	b.Grow(len(line) + 4*n)
	for _, c := range line[:n] {
		if c == '`' {
			b.WriteString("&#96;")
		} else {
			b.WriteByte('\\')
			b.WriteRune(c)
		}
	}
	b.WriteString(line[n:])
	return b.String()
}

// fenceEscapeNeutralize returns text with its leading fence-opener run's
// backtick characters (if any) replaced by tildes, matching the same
// byte length so that a Span computed against the result still indexes
// correctly into the real, unmodified text.
//
// This exists because a hard-break cluster's whole joined text (what flush
// calls this with) always becomes its first *output* line's own leading
// bytes — computeLines never introduces a break before position 0 — so a
// fence-opener-shaped text is always, eventually, run through
// escapeBlockInterrupt's fence-opener branch. That branch runs much later
// (per output line, after wrapping), well after this cluster's
// typography.Apply/segment.NoBreakSpans have already decided what to
// protect from the *pre*-escape text. If the leading run is itself one
// side of a genuine code span reaching further into the cluster,
// pre-escape text answers a question about a span that will not survive
// to the emitted output: escaping the run's backticks (whichever escape
// form is used, backslash or the current HTML-entity form — see
// escapeBlockInterrupt) removes them from ever pairing as a delimiter
// again, so the span's *other* side goes unmatched, and whatever it used
// to protect (e.g. a quote typography would otherwise leave alone)
// reparses completely differently on the very next pass.
//
// Found by FuzzFormat (issue #8, see issues_test.go's
// issue8-fence-escape-codespan-pairing-smartquotes for the exact repro
// bytes: ModeWrap, SmartQuotes, a tilde-fence-shaped leading run whose
// trailing backtick pair genuinely pairs with a later, matching backtick
// pair further into the line): the leading run's backtick pair pairs
// with that later one on the pre-escape text, so typography leaves the
// apostrophe between them straight — but escaping the leading run to
// defeat the tilde-fence
// block trigger destroys that pairing, so a second pass (parsing the
// first pass's own output fresh) finds no code span there at all and
// curls it: an idempotency break, and incidentally a render-preservation
// one too (the original source genuinely renders that apostrophe inside
// a <code> span; escaping the fence run unavoidably loses it either way,
// but the decision should agree with that loss immediately, not one pass
// late). Neutralizing the run here — before NoBreakSpans ever sees it —
// makes this cluster's *own* protection decision agree with what its
// output will actually reparse as, so there is nothing left for a second
// pass to disagree about.
//
// The placeholder is '~', not deletion or any other rewrite, specifically
// to keep every later byte offset identical to the real text: the caller
// passes this function's result to segment.NoBreakSpans only, and applies
// the resulting spans against the real, unescaped text directly — no
// offset remapping needed. '~' is never itself scanned by
// segment.codeSpans (backtick-only) and is inert to every other
// NoBreakSpans sub-scanner (brackets, autolinks, HTML tags, ...), none of
// which treats a bare run of tildes specially.
func fenceEscapeNeutralize(text string) string {
	// Second instance of the same mismatch, for the link-ref-def escapes
	// instead of the fence escape: a cluster that will itself be emitted
	// def-opener-shaped gets its leading "[" backslash-escaped at
	// emission, which destroys any bracketed span NoBreakSpans would
	// otherwise derive from that "[" — and with it, e.g., smart-quote
	// protection for a quote inside the brackets, which then curls one
	// pass late. Found by FuzzFormat on "[^\\]:\"1A0]" in ModePara with
	// SmartQuotes (seed 38cbdf400862101d). Neutralize the leading "["
	// (length-preserving, same rationale as the fence case below) so the
	// protection decision matches the escaped output's reparse.
	if isCompleteLinkRefDefLine(text) || bareLinkRefDefOpenerLineRE.MatchString(text) {
		// The opener bracket may sit after up to 3 columns of indent
		// (up to 3 columns of indent); neutralize the bracket itself.
		b := []byte(text)
		i := 0
		for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
			i++
		}
		if i < len(b) && b[i] == '[' {
			b[i] = '~'
		}
		return string(b)
	}
	if !isFenceOpener(text) {
		return text
	}
	n := fenceOpenerRunLen(text)
	if !strings.ContainsRune(text[:n], '`') {
		return text // pure tilde run: nothing here for codeSpans to see anyway
	}
	b := []byte(text)
	for i := 0; i < n; i++ {
		if b[i] == '`' {
			b[i] = '~'
		}
	}
	return string(b)
}

// orderedListRE matches an ordered-list marker: 1-9 digits followed by "."
// or ")" and then a space or end of line. Capture group 1 is the
// delimiter, the character escapeBlockInterrupt must escape (not the
// leading digit — see its doc comment).
var orderedListRE = regexp.MustCompile(`^\d{1,9}([.)])(\s|$)`)

// escapeBlockInterrupt inserts a backslash into line, if line placed at
// the start of a fresh line would otherwise be misparsed as a new block on
// reparse instead of continuing the paragraph it belongs to.
//
// This is necessary because reflow moves sentence text to new line
// positions a paragraph's original source never had it at: mdreflow only
// ever moves *where* a paragraph's line breaks fall (never its block
// structure), but CommonMark decides block structure per physical line,
// so a line that only ever existed mid-sentence in the original source can
// — purely as a byproduct of where reflow chose to put the next break —
// become a fresh line whose leading characters CommonMark would otherwise
// treat as starting a new block. A backslash defeats every one of these
// triggers (block structure is determined from raw source bytes before
// backslash-escape processing, which is an inline-level concern), found by
// FuzzFormat on input "0000000000  \n    #" (a hard break followed by an
// indented bare "#"): mdreflow's own reflow removed the leading
// indentation that had been keeping that "#" below the 4-space threshold
// ATX headings require to be disqualified, so re-parsing the output split
// it into a paragraph plus an empty heading.
//
// For every trigger except the ordered-list marker, the backslash goes
// immediately before the line's first byte and renders identically to the
// unescaped character, since CommonMark backslash-escapes any ASCII
// punctuation character to its literal self. The ordered-list marker is
// different and needs its own case: its first byte is a *digit*, and
// CommonMark backslash-escaping only recognizes punctuation — "\1" is not
// an escape sequence at all, it is a literal backslash followed by "1",
// which would leave a visible backslash in the rendered output (a second,
// independent fuzz find, on top of "1." itself no longer even working as
// the intended escape). The delimiter after the digits ("." or ")") is
// punctuation, so escaping that instead ("1\.") defeats the list-marker
// trigger just as well — the raw bytes are no longer "digit(s) immediately
// followed by a delimiter" — while rendering identically.
//
// Applied to every non-verbatim output line, including a paragraph's very
// first — see writeParagraph's call site for why the first line is not
// exempt from the position-independent triggers.
//
// isFirstLine gates one trigger specifically: htmlBlockAnyOpenerRE
// (CommonMark HTML-block type 7 — any tag name other than
// htmlBlockTagNames's list, as a complete tag alone on a line), which per
// spec cannot interrupt an already-open paragraph, so it is only actually
// dangerous when this line is a fresh block start, not a continuation of
// one. Every other trigger here can interrupt a paragraph from any
// position and is checked regardless of isFirstLine. Getting this
// distinction wrong in the other direction — treating type 7 as always
// dangerous — was a real regression, not a hypothetical one: found by
// FuzzFormat regressing on legitimate inline "<br>" text (a hard-break
// marker mdreflow itself emits) once a first draft of the type-7 handling
// folded it into the always-applies set.
//
// firstLinePrefix is the raw source bytes writeParagraph's caller (package
// reflow's own Format) already copied byte-for-byte immediately before a
// nested paragraph's first line — a list item's "- "/"1. " marker or a
// blockquote's "> ", for instance — and is empty for every other call
// (continuation lines, and any paragraph not nested in a container at
// all). It matters only to isThematicBreak, and only for the paragraph's
// first output line (every other caller passes ""): a thematic break's
// three-instance-of-the-same-character rule can be satisfied *jointly* by
// the container marker's own leading byte plus wrapped content that, on
// its own, does not reach the threshold. Found by FuzzFormat on
// `-\n- -- -02\n* ` under ModeWrap at width 1: the list item's original,
// unsplit content "-- -02" was never thematic-break-shaped (the trailing
// digits disqualify it), so goldmark had already parsed it as ordinary
// list-item prose — but wrapping split it, landing "--" alone on the
// item's own first output line. Checked in isolation "--" is only two
// dashes, not a break; checked as CommonMark actually would ("- " marker
// + "--" content = "- --"), the marker's own dash joins the run to make
// three, and CommonMark's own rule that a thematic break wins over a list
// item on genuine ambiguity reinterprets the *whole item* as a thematic
// break instead on the very next parse — different structure from pass
// one, an idempotency break, not just a render-preservation one.
// Checking line alone here would have missed it, since line alone was
// never wrong; only line as it will actually be *placed* was.
//
// prevLineNonBlank is a second, narrower piece of joint context, needed for
// the same reason firstLinePrefix is but from the *other* side: whether
// this line, once trimmed, is shaped like a GFM table delimiter row
// (isTableDelimiterRowShaped) is dangerous only jointly with whatever line
// immediately precedes it — a delimiter row directly under a non-blank
// "header" line is exactly GFM's own table-recognition rule, and reflow
// can manufacture that adjacency purely as a byproduct of where a
// width-based cut (or a sentence break) happens to land, the same class of
// hazard filterLineStartHazards already guards against for dialect markers
// and linkify, just discovered one line later than a candidate-break
// filter can see: the *break* that creates this line's start is safe in
// isolation (nothing about landing "-:" at a line start is inherently
// wrong), and what makes it unsafe only exists once the *previous* line's
// content is also known. Found by FuzzFormat in ModeSentence at a small
// MaxWidth on "\f -:" (issue #13) and "\v -|-|-|-|-|-|-" (issue #5): both
// single physical source lines, split by a forced width cut into a first
// line ("\f"/"\v", non-blank) and a second, delimiter-row-shaped line
// ("-:"/"-|-|-|-|-|-|-"), which the very next parse reads as a table
// header and delimiter row instead of two lines of one paragraph. Callers
// pass the previous *output* line's own blankness for a continuation line
// (i > 0, computed directly from writeParagraph's already-built outLines)
// and the previous *source* line's blankness for the paragraph's own first
// output line (i == 0, since nothing in this package's own output exists
// yet to consult there — see writeParagraph's precededByNonBlankLine).
// Width-estimation callers (fitLen, widthMeasurer) always pass true, the
// same conservative-overestimate choice already documented for
// isFirstLine: a table-delimiter escape can only make a candidate line's
// simulated width larger, never smaller, than its real final width,
// whatever this line's real neighbor turns out to be.
func escapeBlockInterrupt(line string, isFirstLine bool, firstLinePrefix string, prevLineNonBlank bool) string {
	if line == "" {
		return line
	}
	if m := orderedListRE.FindStringSubmatchIndex(line); m != nil {
		delimStart := m[2] // start of capture group 1 (the "." or ")")
		return line[:delimStart] + "\\" + line[delimStart:]
	}
	if isFenceOpener(line) {
		// Escaping only the run's first backtick/tilde (the generic
		// "\\" + line case below) defeats the *block*-level fence
		// trigger — which only cares about the raw leading byte run —
		// but leaves the remaining 2+ backticks/tildes as a real,
		// unescaped run, which can still pair up as an *inline* code
		// span with some other same-length run later in the (reflowed,
		// possibly rejoined) line — found by FuzzFormat on "```! 0``":
		// escaping just the first backtick of the leading run left "``"
		// behind, which then paired with the trailing "``" to form a
		// code span spanning content that was never inside one. Escaping
		// every backtick/tilde in the run individually removes all of
		// them from participating in any run-length match at all as an
		// *opener* — fixing that hazard — and still renders identically
		// (each is a literal escaped punctuation character either way).
		//
		// A backslash-escaped backtick, though, only defeats the opener
		// half: it does not stop the same backtick from acting as a
		// *closer* for some unrelated, genuinely open backtick run
		// earlier in the same paragraph. Confirmed directly against
		// goldmark, not assumed: "`\`" (a lone opening backtick, later a
		// backslash-escaped one) renders as `<code>\</code>` — the
		// backslash does not prevent the second backtick from closing
		// the span, because CommonMark's code-span closing search
		// matches any same-length backtick run regardless of what
		// precedes it (backslash-escape processing is a lower-precedence
		// pass that never gets a chance to run inside what codeSpans
		// finds delimits a span). This is a live hazard here: an earlier
		// line in the same cluster/paragraph can carry a real, dangling
		// (unmatched, so segment.CodeSpans correctly does not protect
		// anything around it, and mdreflow's own decisions were made on
		// that basis) single backtick with no partner in the *original*
		// source, but once this run's own backticks are individually
		// escaped, one of them can spuriously close against that
		// dangling backtick on reparse — retroactively manufacturing a
		// code span (and its no-break protection) that never existed,
		// changing what a hard-break marker or a typography substitution
		// downstream of it decided. Found by FuzzFormat (issues #6/#12)
		// on "`  \n    ```" in ModeWrap: the hard-break-separated "`"
		// has no partner pre-escape, but pass 1's fence-defeating escape
		// of the following "```" pairs one of its now-individual
		// backticks against it on pass 2's reparse, flipping whether the
		// hard break is honored and producing different output than pass
		// 1's own.
		//
		// A backtick escaped as an HTML character reference instead of a
		// backslash sidesteps this structurally rather than case by
		// case: "&#96;" is not a backtick byte at all, so it can never
		// open *or* close a code span on any reparse, ever — and it
		// still decodes to a literal "`" on render, identically to the
		// backslash form. Tildes have no such closer hazard (inline code
		// spans are backtick-only; segment.codeSpans never scans for
		// '~'), so they stay backslash-escaped.
		return escapeFenceOpenerRun(line)
	}
	if isThematicBreak(firstLinePrefix+line) || isSetextUnderline(line) || blockInterruptTriggers.MatchString(line) || isCompleteLinkRefDefLine(line) || bareLinkRefDefOpenerLineRE.MatchString(line) || (prevLineNonBlank && isTableDelimiterRowShaped(line)) {
		return escapeAfterIndent(line)
	}
	if isFirstLine && htmlBlockAnyOpenerRE.MatchString(line) {
		return escapeAfterIndent(line)
	}
	return line
}

// isCompleteLinkRefDefLine reports whether line, parsed by goldmark in
// total isolation, is a complete link reference definition — the
// authoritative form of the question linkRefDefOpenerRE used to
// approximate with a hand-mirrored grammar. The mirroring kept losing to
// goldmark's implementation details (seed 617a8c27848709db: up to 3
// columns of indent and \f accepted as separator whitespace; seed
// afe8aadcbee7bf0d: yet \f INSIDE a destination token continues it — the
// skip-before and end-of-token whitespace classes simply differ), so the
// emitted line is now judged by the same parser whose reparse the escape
// exists to control. The "]:" pre-filter keeps the parse off the hot
// path for ordinary prose. An already-escaped "\[label]:" line parses as
// a paragraph and correctly answers false, so escapes never stack.
func isCompleteLinkRefDefLine(line string) bool {
	if !strings.Contains(line, "]:") {
		return false
	}
	doc := gm.New().Parser().Parse(text.NewReader([]byte(line)))
	first := doc.FirstChild()
	return first != nil && first.Kind() == ast.KindLinkReferenceDefinition && first.NextSibling() == nil
}

// escapeAfterIndent backslash-escapes line's first non-space/tab byte —
// not byte 0: several trigger shapes tolerate leading indent (a link
// reference definition up to 3 columns, a setext underline, a table
// delimiter row), and a backslash placed BEFORE that whitespace is an
// escaped space, i.e. a literal backslash character in the rendered
// output — the escape itself would break render preservation. Escaping
// the first content byte renders as exactly the original text for every
// trigger class here (all begin with escapable ASCII punctuation).
func escapeAfterIndent(line string) string {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) {
		return line
	}
	return line[:i] + "\\" + line[i:]
}

// isThematicBreak reports whether line (ignoring up to 3 leading spaces)
// consists of 3 or more of the same character among '-', '*', '_',
// optionally separated by spaces — CommonMark's thematic-break rule. Go's
// RE2 regexp engine has no backreferences, so this can't be expressed as
// a single regex the way the other triggers can; it is checked directly.
//
// The separator class is ' ', '\t', AND a bare '\r': goldmark's own
// thematic-break scanner (parser.isThematicBreak, via util.IsSpace, whose
// spaceTable includes 0x0D) treats a lone '\r' as just another ignorable
// separator between the repeated marks, not as a disqualifying character —
// the same asymmetry already documented on filterUnsafeLineEnds and
// joinClusterLines, where a bare '\r' is ordinary literal content to
// mdreflow's own line-splitting but goldmark's scanners still fold it in
// as whitespace in specific places. Missing that asymmetry here left a
// joint-context gap in escapeBlockInterrupt's firstLinePrefix check (see
// its doc comment): a list item's own marker plus a wrapped first line
// like "---\r-" reads as only two real dashes to this function without the
// '\r' concession, so it never escaped — but goldmark's actual parser DOES
// count it as thematic-break-shaped ("- ---\r-" scans as marker "-" plus
// three dashes separated by a space and an ignorable '\r'), reparsing the
// list item as a ThematicBreak instead. That flips the item from a List
// (whose content the following empty "*" bullet cannot lazily join) to a
// bare Paragraph (which an empty bullet line CANNOT interrupt, so it
// lazily continues into it instead) — an idempotency break found by
// FuzzFormat (ModeWrap, MaxWidth 6, SmartQuotes|Ellipses) on
// "\n- ---\r- % \n*" (seed ad20670286270350): pass 1 wrapped the list
// item's "---\r- %" onto "---\r-" / "%", pass 2 reparsed "- ---\r-" as a
// thematic break and merged "%" with the following "*" that pass 1 had
// kept as a separate, empty list item.
func isThematicBreak(line string) bool {
	s := line
	for i := 0; i < 3 && strings.HasPrefix(s, " "); i++ {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	want := s[0]
	if want != '-' && want != '*' && want != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case want:
			count++
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return count >= 3
}

// isSetextUnderline reports whether line (ignoring up to 3 leading spaces,
// and any trailing spaces/tabs) consists solely of one or more '='
// characters — a CommonMark setext level-1-heading underline, which turns
// whatever paragraph immediately precedes it into a heading. Unlike a
// thematic break, a single '=' is already enough (no 3-repeat minimum),
// and unlike the other block-interrupt triggers, "=" is not itself
// escapable in a way that helps here: it is not the paragraph's own
// *first* line's construct, it retroactively changes what the *previous*
// line becomes, so escapeBlockInterrupt's normal "escape this line's
// leading byte" handles it the same way as any other trigger it finds on
// a line. Found by FuzzFormat on "0  \n    =": the source's underline sat
// at 4 spaces of indentation, disqualifying it from CommonMark's setext
// rule (max 3), so the original paragraph stayed plain text — but
// mdreflow's own line-joining removed that indentation (this paragraph is
// top-level, so continuation lines get no ContPrefix), moving the "="
// under the 3-space threshold and turning it into a real heading
// underline on reparse. A setext level-2 underline ("-" repeated) is
// already covered by isThematicBreak, since a "-" run also matches the
// thematic-break trigger and escaping its first character defeats both
// interpretations at once; "=" has no such overlap and needs this
// dedicated check.
func isSetextUnderline(line string) bool {
	s := line
	for i := 0; i < 3 && strings.HasPrefix(s, " "); i++ {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	count := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '=':
			count++
		case ' ', '\t', '\r':
			// CR is trailing whitespace to goldmark ("A\n=\r" is a real
			// heading, confirmed directly), same whitespace-class family
			// as isTableDelimiterRowShaped's CR. Accepting it anywhere in
			// the line (goldmark itself rejects an *interior* CR, "=\r=")
			// over-matches, at the usual documented cost: one
			// superfluous, identically-rendering escape.
		default:
			return false
		}
	}
	return count >= 1
}

// isTableDelimiterRowShaped reports whether line is shaped like a GFM
// table delimiter row: once trimmed of leading/trailing whitespace, it
// contains only "-", ":", "|", spaces, tabs, and CRs, with at least one
// "-". CR is in the class because it is in goldmark's (util.IsSpace-based)
// cell-padding whitespace — "|-\r|" is a real delimiter row to goldmark,
// found by FuzzFormat on "C009019\n , z0cA z0 |-\r| 10" (seed
// d994c9196409b0fd), where a width-constrained split landed "|-\r|" at
// line start unescaped and a table formed on reparse. Same
// goldmark-whitespace-alignment family as the four v0.1.4 finds
// (design.md: when a scanner disagrees with goldmark about whitespace,
// goldmark wins).
// Deliberately crude and permissive, matching GFM's own leniency (a bare
// "-|" or ":-" already qualifies, and spaces are allowed *within* the
// shape, not just at its edges — e.g. "-- |" is still a valid delimiter
// row): this only needs to decide whether escaping this line's first byte
// is warranted, and escaping a line that turns out not to have been truly
// delimiter-row-shaped after all costs nothing but one superfluous,
// identically-rendering backslash, while under-matching risks a real
// table forming on reparse — this is deliberately the fuzz-test package's
// own isTableDelimiterRowShaped oracle's shape (see fuzz_test.go), kept as
// an independent implementation here since the two serve different roles:
// that one decides whether to skip an assertion, this one decides whether
// to change output.
func isTableDelimiterRowShaped(line string) bool {
	trimmed := strings.Trim(line, " \t\r")
	return trimmed != "" && strings.ContainsRune(trimmed, '-') && strings.Trim(trimmed, "-:| \t\r") == ""
}

// attachMarker appends marker directly after s, unless doing so would
// change a trailing backslash in s from a literal character into an escape
// of marker's first byte: s ending in an odd number of backslashes means
// the last one is unescaped (see detectHardBreak's own such count), and if
// marker starts with an ASCII punctuation byte, gluing them turns that
// backslash into a valid CommonMark escape of it — most importantly
// "\<", which stops "<br>" from being recognized as raw HTML at all
// (it becomes literal, HTML-entity-escaped text instead). Found by
// FuzzFormat on "0\\ <Br>": the source had a space between the backslash
// and the tag, which hardBreakBrRE's own match consumes as (insignificant,
// it assumed) trailing whitespace before the marker; re-emitting the
// marker flush against the bare backslash fused them. A single space
// between them (real whitespace almost certainly existed there in the
// source, and this is a documented, harmless place to reintroduce it) is
// enough to break the fusion without changing anything else.
// attachMarker does not attempt to guard CommonMark emphasis "flanking"
// (whether a "*"/"_"/"~" run immediately before the marker opens or closes
// emphasis) at all: an early version tried inserting a space whenever such
// a delimiter directly preceded the marker, on the theory that a hard
// break itself counts as whitespace for flanking purposes the way literal
// marker text does not. That theory was wrong often enough to be worse
// than no guard: FuzzFormat found both a case where the space was needed
// ("!*  \n0*" — a "*" preceded by punctuation, where gluing "<br>" on
// invents emphasis the original two-space break did not have) and a case
// where inserting it broke a *correct* result ("*\\\n0*" — a "*" at the
// very start of the paragraph, where the glued form already matches the
// original's real emphasis, and the guard's added space instead turned
// "* <br>" into a bullet-list-marker-shaped line start, which
// escapeBlockInterrupt then had to defend against by escaping the "*" —
// which also defeats the emphasis the original had). Precisely
// replicating CommonMark's flanking algorithm (which depends on more than
// just the immediately adjacent byte) is out of scope; this is accepted as
// a narrow extension of design.md's existing hard-break-style-
// normalization render-preservation exception, not silently — see the M2
// report and fuzz_test.go's stripEmphasisNearHardBreak.
func attachMarker(s, marker string) string {
	if marker == "" || s == "" {
		return s + marker
	}
	if endsInUnescapedBackslash(s) && isASCIIPunct(marker[0]) {
		return s + " " + marker
	}
	return s + marker
}

// isASCIIPunct reports whether b is one of CommonMark's escapable ASCII
// punctuation characters.
func isASCIIPunct(b byte) bool {
	switch {
	case b >= '!' && b <= '/':
	case b >= ':' && b <= '@':
	case b >= '[' && b <= '`':
	case b >= '{' && b <= '~':
	default:
		return false
	}
	return true
}

// lineFrag is one source line's contribution to a hard-break cluster: its
// prose text (hard-break marker bytes, if any, already stripped) plus
// whether its leading/trailing edge sits inside a genuine inline code span
// that crosses this line boundary (see insideCodeSpanAfterLine) and so
// must not be trimmed.
type lineFrag struct {
	text                                string
	leadingProtected, trailingProtected bool
}

// joinClusterLines joins a hard-break cluster's per-line prose fragments
// into one string, matching CommonMark's own line-joining behavior as
// closely as this line-based architecture allows:
//
//   - Ordinarily, each fragment's leading/trailing ASCII space/tab is
//     insignificant padding (see trimLineSpace): it is trimmed, and
//     exactly one space is inserted between fragments — matching how a
//     soft line break renders (a browser collapses it to one space) and
//     required for idempotency (rejoining already-reflowed lines must not
//     accumulate whitespace).
//   - A fragment edge marked protected sits inside a genuine inline code
//     span that spans this line boundary (computed once for the whole
//     paragraph by insideCodeSpanAfterLine, not re-derived here): an
//     inline code span may itself span a paragraph's line break —
//     CommonMark converts that line ending to a single space *inside* the
//     span, in addition to (not instead of) whatever space the span's own
//     content already has at that edge, and touches none of the span's
//     other interior whitespace. So a join point inside a span still gets
//     exactly one inserted separator space, same as any other join — only
//     the trim differs: trimming a protected edge would delete part of
//     the span's real content, so it is skipped on that side — found by
//     FuzzFormat on "` \n0 `" (a code span opened on one line, holding a
//     real leading space, and closed on the next, holding a real trailing
//     space: the correct joined interior is two spaces, "0", one space,
//     which only holds if neither edge is trimmed while the separator is
//     still inserted).
//   - A fragment that trims to nothing (e.g. a degenerate zero-width line
//     segment — see the allBlank check in package blockmap for why that
//     can happen at all) contributes no text and no separator.
func joinClusterLines(frags []lineFrag, hasTrailingMarker bool) string {
	var b strings.Builder
	wrote := false
	lastTrimmed := false
	lastPieceEnd := ""
	for _, f := range frags {
		piece := f.text
		// The trim set includes '\r': a fragment's bytes have had their
		// real line ending stripped already, so any trailing '\r' here is
		// a bare carriage return in content — part of reflow's [ \t\r]
		// whitespace class (see segment.isBoundaryWhitespaceByte) — and
		// leaving it exposed at a fragment edge manufactures a CRLF on
		// emission: found by FuzzFormat on "0\r \n:::" (seed
		// 555aadec1740c6ec), where trimming only the trailing space left
		// "0\r" whose emitted "\r\n" the next pass read as a CRLF line
		// ending and stripped — an idempotency flip from the trim itself.
		if !f.leadingProtected {
			piece = strings.TrimLeft(piece, " \t\r")
		}
		trimmed := false
		if !f.trailingProtected {
			t := strings.TrimRight(piece, " \t\r")
			trimmed = t != piece
			piece = t
		}
		if piece == "" {
			continue
		}
		if wrote {
			b.WriteByte(' ')
		}
		b.WriteString(piece)
		wrote = true
		lastTrimmed = trimmed
		lastPieceEnd = piece
	}
	// Restoring one trimmed space when the cluster now ends in an ODD
	// backslash run: the cluster's end becomes a line end on emission, and
	// a line ending in an odd backslash run IS a backslash hard break to
	// the next parse — so the trim itself would manufacture a break the
	// source never had, which the second pass then normalizes to the
	// configured marker: found by FuzzFormat on "0\\ \n:::" (seed
	// cf5efcc4d8ee400c), where trimming "0\\ " to "0\\" turned a literal
	// backslash into a break and pass 2 rewrote it to "0<br>". The
	// restored space renders identically (a backslash before a space is
	// just a literal backslash either way) and the decision is stable: a
	// re-format re-trims and re-restores the same byte. Only applies when
	// something was actually trimmed — a trailing-protected fragment
	// (code-span interior) must not grow a space, and an untrimmed
	// ordinary fragment cannot end in an odd run at all (a source line
	// ending in one is a real hard break and travels the marker path,
	// never this join) — UNLESS this cluster itself has a trailing
	// marker (hasTrailingMarker), which is the one case an "untrimmed"
	// fragment genuinely can still end in a bare backslash: detectHardBreak
	// strips a "<br>" hard-break marker's own leading whitespace as part
	// of the match (hardBreakBrRE greedily consumes it), so the
	// marker-stripped "rest" for a line like "\ <br>" is already just "\"
	// — no trailing space for *this* function to trim at all, yet it
	// still ends in a bare backslash.
	//
	// hasTrailingMarker suppresses the restore for exactly that case,
	// because the caller (writeParagraph's flush) is about to call
	// attachMarker on this cluster's last output line regardless, and
	// attachMarker already inserts its own separating space whenever the
	// text it's attaching to ends in an unescaped backslash
	// (endsInUnescapedBackslash) — the identical hazard, independently
	// guarded, closer to where the marker is actually attached. Letting
	// *both* guards fire here double-guessed a space that then has no
	// stable meaning on reparse: found by FuzzFormat (MaxWidth 4,
	// SmartQuotes) on "\\\t\ \\\n0" (seed 8732e6eb8a47d4f3) — this
	// restore added a trailing space to a cluster ending "...\\ " that
	// then got split (needed once counted as 5 runes, not 4) into
	// "\\\\" / "\\ ", with "<br>" attached to the second piece. On
	// reparse, hardBreakBrRE's own greedy whitespace consumption ate that
	// *same* restored space as part of the marker match, so the
	// reconstructed cluster measured one rune narrower than pass 1's
	// — 4, not 5 — and no longer needed the split at all: pass 2 rejoined
	// onto one line despite MaxWidth 4, an idempotency break. Skipping the
	// restore when a marker follows removes the extra rune from pass 1's
	// own width accounting in the first place, so both passes agree on
	// the same, already-stable-via-attachMarker text.
	if !hasTrailingMarker && lastTrimmed && trailingBackslashCount(lastPieceEnd)%2 == 1 {
		b.WriteByte(' ')
	}
	return b.String()
}

// insideCodeSpanAfterLine reports, for each line in rawContents (a
// paragraph's per-line content, hard-break markers not yet stripped),
// whether the boundary immediately after that line's content — where a
// hard-break marker would sit, and where joinClusterLines would otherwise
// insert a fresh separator — falls inside a genuine inline code span.
//
// "Genuine" means actually open-to-close matched, the same way goldmark's
// own inline grammar would match one: rawContents is joined into a single
// string with "\n" (matching how goldmark's inline parser sees a
// paragraph — one continuous stream, soft breaks and all, regardless of
// how mdreflow's own dialect-marker boundaries chop it into clusters), and
// segment.CodeSpans finds the real spans in that joined text. A per-line
// running-parity guess ("odd backtick count so far") cannot make this
// distinction — an opening backtick run with no later matching close is
// not a span at all — which is exactly the gap FuzzFormat found on
// "`\\\n0" (one backtick, never closed).
func insideCodeSpanAfterLine(rawContents []string) []bool {
	n := len(rawContents)
	joined := strings.Join(rawContents, "\n")
	spans := segment.CodeSpans(joined)

	lineEnd := make([]int, n)
	pos := 0
	for i := 0; i < n; i++ {
		pos += len(rawContents[i])
		lineEnd[i] = pos
		pos++ // the "\n" joiner
	}

	out := make([]bool, n)
	for i, end := range lineEnd {
		for _, sp := range spans {
			if sp.Start <= end && end < sp.End {
				out[i] = true
				break
			}
		}
	}
	return out
}

// trimLineSpace trims leading and trailing ASCII space and tab from s —
// CommonMark's own notion of insignificant line whitespace — before a
// source line's content is joined into a prose cluster.
//
// This is deliberately narrower than strings.TrimSpace, which is
// Unicode-aware and also strips control characters like U+000B (vertical
// tab) and U+000C (form feed). Found by FuzzFormat: those characters are
// not blank per CommonMark's own (space/tab only) test, so goldmark still
// forms a paragraph around them and its HTML renderer treats one as
// meaningful (rendering, e.g., a leading form feed as a literal leading
// space in the output); stripping them here as if they were ordinary
// padding silently deleted content goldmark considers significant. Content
// this obscure is not expected in real Markdown, but trimming only what
// CommonMark itself treats as insignificant is the principled fix, not a
// fuzz-only patch.
func trimLineSpace(s string) string {
	return strings.Trim(s, " \t")
}

// stripLineEnding removes a trailing line ending from raw, reporting
// whether one was present. CommonMark recognizes three spellings — "\n",
// "\r\n", and a bare "\r" not followed by "\n" (rare in practice, but
// valid) — and goldmark's own Lines() segments can, for a document mixing
// line-ending styles, include a run of more than one of these
// concatenated as a single line's trailing bytes (confirmed directly, not
// assumed: ":::\r\r\n0" — a bare-CR ending immediately followed by a
// CRLF-ended blank line — produces one Lines() segment whose raw value is
// ":::\r\r\n", not two segments). Stripping only the single, longest
// suffix among "\r\n"/"\n"/"\r" therefore isn't enough; every trailing
// \r/\n byte is trimmed here, however many there are, so paragraph
// content is fully normalized to LF on reflow regardless of how many
// line-ending sequences the source concatenated. Missing this left a
// literal "\r" byte in reflowed output, found by FuzzFormat as an
// idempotency break on exactly that input. Pass-through (non-paragraph)
// regions keep their original line endings untouched — this only affects
// content already inside a reflowed paragraph.
func stripLineEnding(raw []byte) (content string, hadNewline bool) {
	s := string(raw)
	end := len(s)
	for end > 0 && (s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[:end], end < len(s)
}

// hardBreakBrRE matches an inline <br> (with optional self-closing slash
// and internal spacing) trailing a line, along with any whitespace right
// before it.
//
// The internal spacing before "/?>" is deliberately "[ \t]*", not Go
// regexp's "\s*": \s also matches '\f' (form feed) and '\v' (vertical
// tab), which are not valid whitespace inside an HTML tag under
// CommonMark's actual raw-HTML grammar (only ASCII space, tab, and line
// endings are). Using \s made this regex match text goldmark's own parser
// does not recognize as a tag at all — found by FuzzFormat on "<Br\f>":
// goldmark renders it as literal, HTML-escaped text ("&lt;Br &gt;"), but
// the over-broad \s made mdreflow treat the *entire* line as nothing but
// a hard-break marker with zero prose in front of it, which (via the
// "bare first line" HardBreakBr safety fallback a few lines below)
// discarded the original content outright, replacing it with a lone
// backslash — silent, severe data loss for real prose that merely
// contained a stray form-feed byte.
// Interior whitespace is accepted only in the self-closing spelling
// ("<br />"), not before a bare ">" ("<br >"): the latter is a shape
// mdreflow's own line-joining can manufacture from a multi-line inline
// tag ("<Br\n\t>" joins to "<Br >"), and recognizing it as a marker meant
// pass 1 produced text that pass 2 re-read as a hard break and rewrote —
// found by FuzzFormat on "0<Br\n\t>" (seed a9266695f535279c). A real
// "<br >" in source loses marker normalization and passes through as the
// raw HTML it already is; renders identically either way.
var hardBreakBrRE = regexp.MustCompile(`(?i)[ \t]*<br([ \t]*/)?>[ \t]*$`)

// sentenceTerminalEndRE matches text ending in sentence-terminal
// punctuation (optionally followed by closing quotes/brackets), used by
// Options.StripSentenceTerminalBreaks.
var sentenceTerminalEndRE = regexp.MustCompile(`[.!?…]["'”’)\]]*$`)

// detectHardBreak inspects a line's content (line ending already stripped)
// for one of the three hard-break syntaxes mdreflow recognizes: trailing
// backslash, trailing double-space (or more), or a trailing <br>. On a
// match it returns the marker normalized to Options.HardBreaks's style (to
// be emitted in place of the original bytes) and the remaining prose. On no
// match — or when Options.StripSentenceTerminalBreaks removes an accidental
// double-space break — it returns an empty marker and the content
// unchanged, so the line rejoins its neighbor as an ordinary soft break.
//
// isLastLine must be true when content is the paragraph's final source
// line: per CommonMark, a trailing backslash or double-space is only a
// hard break when a following line exists to break onto — on the last line
// of a block, a trailing backslash is a literal character and trailing
// spaces are insignificant, so neither is treated as a break. A literal
// <br> tag is inline HTML regardless of line position and is always
// recognized.
//
// insideSpan must be true when content's own trailing bytes sit inside an
// inline code span that continues onto the next line (see writeParagraph's
// insideSpan tracking). None of the three hard-break syntaxes are
// recognized inside a code span at all — code span content is entirely
// literal, with no nested inline parsing — so detecting one there would
// invent a break the source never had. Found by FuzzFormat on "`\\\n`": a
// code span opened on line 1 ("`" then a literal, non-escaping backslash)
// and closed on line 2; treating that trailing backslash as a hard break
// turned literal content into an actual line break on reparse.
func detectHardBreak(content string, opts Options, isLastLine, insideSpan bool) (marker, rest string) {
	if insideSpan {
		return "", content
	}
	if !isLastLine {
		// Backslash: exactly one trailing backslash is a hard break — not
		// "an odd run of trailing backslashes", which was M1's rule and is
		// wrong for 3+ (verified empirically, since this is a case where
		// intuition from the general odd/even backslash-escape-pair
		// pattern misleads: escape pairs are consumed greedily from the
		// left, e.g. "\\\\\\" is one pair, rendering one literal
		// backslash, plus one more remaining backslash, and by the
		// general pattern that remainder — trailing, unescaped — "should"
		// be hard-break-eligible the same way a single trailing backslash
		// is. It empirically is not: goldmark renders 3 (and 4, and any
		// N>=2) trailing backslashes before a line ending as literal
		// backslash characters with an ordinary soft break, never a hard
		// break; only N==1 is special-cased. Found by FuzzFormat on
		// "\\\\\\\n0" (3 backslashes): treating the run as hard-break-
		// eligible (old odd-count rule) invented a break that changes
		// the rendered content, not just its style.
		n := trailingBackslashCount(content)
		if n == 1 {
			return normalizedMarker(opts), content[:len(content)-1]
		}

		// Trailing spaces: two or more.
		i := len(content)
		for i > 0 && content[i-1] == ' ' {
			i--
		}
		if len(content)-i >= 2 {
			rest := content[:i]
			if opts.StripSentenceTerminalBreaks && sentenceTerminalEndRE.MatchString(rest) {
				return "", rest
			}
			return normalizedMarker(opts), rest
		}
	}

	// <br>.
	if m := hardBreakBrRE.FindStringIndex(content); m != nil {
		prefix := content[:m[0]]
		// The backslash-escape check must look at the byte immediately
		// before the "<" itself, not before the whole match: hardBreakBrRE
		// greedily consumes any whitespace ahead of the tag into the
		// match, and a backslash only escapes the character it is
		// directly adjacent to. "0\\ <br>" (space between the backslash
		// and the tag) must NOT be treated as escaped — found by
		// FuzzFormat as a second-pass idempotency break on exactly that
		// shape, once attachMarker started correctly inserting that
		// separating space for the unrelated fusion case below.
		ltPos := m[0] + strings.IndexByte(content[m[0]:], '<')
		if endsInUnescapedBackslash(content[:ltPos]) {
			// Fuzz-found inverse of the marker-fusion case attachMarker
			// guards against: an odd number of backslashes *directly*
			// before the "<" is a valid CommonMark escape of that "<",
			// which stops "<br>" from being raw HTML at all — it renders
			// as literal, HTML-entity-escaped text ("00000\<Br>" renders
			// "00000&lt;Br&gt;", not a break). Treating it as a hard
			// break here would then normalize it to an actual "<br>" (or
			// strip/respell it), turning inert literal text into a real
			// line break — a content change, not a style change. Found
			// by FuzzFormat on "00000\<Br>".
			return "", content
		}
		return normalizedMarker(opts), prefix
	}

	return "", content
}

// endsInUnescapedBackslash reports whether s ends in an odd number of
// trailing backslashes — i.e. the last one is itself unescaped and would
// escape whatever comes immediately after s.
func endsInUnescapedBackslash(s string) bool {
	return trailingBackslashCount(s)%2 == 1
}

// trailingBackslashCount returns the number of consecutive backslash
// characters s ends in.
func trailingBackslashCount(s string) int {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	return n
}

// normalizedMarker returns the hard-break marker bytes for opts.HardBreaks,
// regardless of which of the three syntaxes the original line used.
func normalizedMarker(opts Options) string {
	switch opts.HardBreaks {
	case HardBreakSpaces:
		return "  "
	case HardBreakBackslash:
		return "\\"
	default:
		return "<br>"
	}
}
