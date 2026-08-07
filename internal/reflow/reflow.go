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
	"strings"

	"github.com/yuin/goldmark/ast"

	"github.com/jbeda/mdreflow/internal/blockmap"
	"github.com/jbeda/mdreflow/internal/segment"
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

// Options configures the reflow pipeline. It mirrors the subset of
// mdreflow.Options the pipeline needs.
type Options struct {
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
}

// writeParagraph joins p's prose lines into hard-break clusters — stopping
// at both hard breaks and dialect-marker boundary lines (blockmap.Paragraph
// .Boundary) — sentence-splits each cluster, and writes the result to buf,
// re-indenting every output line after the first with p.ContPrefix.
func writeParagraph(buf *bytes.Buffer, p blockmap.Paragraph, source []byte, seg Segmenter, opts Options) {
	lines := p.Node.Lines()
	n := lines.Len()

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

	flush := func(marker string) {
		if len(curLines) == 0 && marker == "" {
			return
		}
		text := joinClusterLines(curLines)
		curLines = nil
		sentences := splitSentences(text, seg)
		for i, s := range sentences {
			if i == len(sentences)-1 {
				s = attachMarker(s, marker)
			}
			outLines = append(outLines, outLine{text: s})
		}
	}

	for i := 0; i < n; i++ {
		lineSeg := lines.At(i)
		content := rawContents[i]

		if p.Boundary[i] {
			flush("")
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
			outLines = append(outLines, outLine{text: text, verbatim: verbatim})
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
			flush(marker)
		}
	}

	for i, ol := range outLines {
		if i > 0 {
			buf.WriteByte('\n')
			if !ol.verbatim {
				buf.WriteString(p.ContPrefix)
			}
		}
		if !ol.verbatim {
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
			// paragraph in the first place.
			ol.text = escapeBlockInterrupt(ol.text, i == 0)
		}
		buf.WriteString(ol.text)
	}
	if lastLineHasNewline {
		buf.WriteByte('\n')
	}
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
	breaks := filterBreaks(seg.Breaks(text), segment.NoBreakSpans(text))

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
// separately (linkRefDefOpenerRE), not folded in here: unlike every other
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

// linkRefDefOpenerRE matches a link-reference-definition-shaped
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
//   - "[0]:" — nothing at all after the colon — incomplete, safe (no
//     token for `[^\s<][^\s]*` to match, so this regex correctly does not
//     match it either).
//
// It excludes a "[^..." bracket (i.e. "[^label]:") on purpose: that shape
// is a *footnote* definition, a completely different, already-legitimate
// construct whose "[^label]: " prefix goldmark keeps as literal text at
// the start of the footnote body's own Paragraph node (unlike a real link
// reference definition, which is its own dedicated AST node — see
// package blockmap's doc comment — and so can never reach this check as
// an unmodified first line to begin with). Escaping a real footnote
// body's own marker would sever the link between it and its "[^label]"
// reference elsewhere in the document — caught by the golden fixture
// testdata/no-break-spans.md before it could ship.
//
// The label body also excludes a raw "[" (not just "]"): a real
// CommonMark link label cannot contain an unescaped "[" either, so
// "[[]: http://0.a" is not actually a link reference definition — and
// escaping it anyway, besides being unnecessary, changed how the rest of
// the line's autolink recognition behaved, found by FuzzFormat on exactly
// that input.
var linkRefDefOpenerRE = regexp.MustCompile(`^\[(\^\]|[^\^\[\]][^\[\]]*\]):[ \t]*[^\s<][^\s]*[ \t]*$`)

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
func escapeBlockInterrupt(line string, isFirstLine bool) string {
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
		// them from participating in any run-length match at all, fixing
		// both hazards at once, and still renders identically (each is a
		// literal escaped punctuation character either way).
		i := 0
		for i < len(line) && (line[i] == '`' || line[i] == '~') {
			i++
		}
		var b strings.Builder
		for _, c := range line[:i] {
			b.WriteByte('\\')
			b.WriteRune(c)
		}
		b.WriteString(line[i:])
		return b.String()
	}
	if isThematicBreak(line) || isSetextUnderline(line) || blockInterruptTriggers.MatchString(line) || linkRefDefOpenerRE.MatchString(line) {
		return "\\" + line
	}
	if isFirstLine && htmlBlockAnyOpenerRE.MatchString(line) {
		return "\\" + line
	}
	return line
}

// isThematicBreak reports whether line (ignoring up to 3 leading spaces)
// consists of 3 or more of the same character among '-', '*', '_',
// optionally separated by spaces — CommonMark's thematic-break rule. Go's
// RE2 regexp engine has no backreferences, so this can't be expressed as
// a single regex the way the other triggers can; it is checked directly.
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
		case ' ', '\t':
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
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 1
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
func joinClusterLines(frags []lineFrag) string {
	var b strings.Builder
	wrote := false
	for _, f := range frags {
		piece := f.text
		if !f.leadingProtected {
			piece = strings.TrimLeft(piece, " \t")
		}
		if !f.trailingProtected {
			piece = strings.TrimRight(piece, " \t")
		}
		if piece == "" {
			continue
		}
		if wrote {
			b.WriteByte(' ')
		}
		b.WriteString(piece)
		wrote = true
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
var hardBreakBrRE = regexp.MustCompile(`(?i)[ \t]*<br[ \t]*/?>[ \t]*$`)

// sentenceTerminalEndRE matches text ending in sentence-terminal
// punctuation (optionally followed by closing quotes/brackets), used by
// Options.StripSentenceTerminalBreaks.
var sentenceTerminalEndRE = regexp.MustCompile(`[.!?]["'”’)\]]*$`)

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
		n := 0
		for n < len(content) && content[len(content)-1-n] == '\\' {
			n++
		}
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
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	return n%2 == 1
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
