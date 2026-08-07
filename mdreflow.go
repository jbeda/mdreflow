// Package mdreflow reflows Markdown prose: it changes where lines break
// inside paragraph text and leaves everything else in a document
// untouched. Its home mode is sentence-per-line (one sentence per source
// line, aka semantic line breaks); paragraph-per-line and hard-wrap modes
// share the same pipeline (see docs/design.md).
//
// mdreflow is a reflow tool, not a formatter: it never rewrites block
// structure (heading style, list markers, tables, escaping). The Markdown
// parser is used read-only to locate paragraph prose; output is produced
// by splicing reflowed prose into the verbatim source bytes.
//
// M2 status: sentence mode only. ModePara, ModeWrap, MaxWidth, and
// Typography are declared here per the library's full API but return an
// error from Format until a later milestone implements them — see each
// field's doc comment.
package mdreflow

import "github.com/jbeda/mdreflow/internal/segment"

// Mode selects the top-level reflow strategy.
type Mode int

const (
	// ModeSentence joins each paragraph's lines and splits at sentence
	// boundaries, one sentence per line. The only mode implemented in M1.
	ModeSentence Mode = iota
	// ModePara joins each paragraph to a single line. Not implemented
	// until M3; Format returns an error if selected.
	ModePara
	// ModeWrap hard-wraps at Options.MaxWidth, breaking at word
	// boundaries only. Not implemented until M3; Format returns an error
	// if selected.
	ModeWrap
)

// HardBreakStyle selects how hard line breaks are normalized when
// reflowed prose moves them to a new position in the source. Every
// preserved hard break (however it was originally spelled) is rewritten to
// this style.
type HardBreakStyle int

const (
	// HardBreakBr renders a hard break as a literal <br>. Default: an
	// accidental double-space hard break survives formatting but becomes
	// loudly visible in a diff.
	HardBreakBr HardBreakStyle = iota
	// HardBreakSpaces renders a hard break as a trailing double space.
	HardBreakSpaces
	// HardBreakBackslash renders a hard break as a trailing backslash.
	HardBreakBackslash
)

// Typography selects opt-in, span-level prose substitutions (never
// applied inside code spans or skip ranges). Not implemented until M5;
// Format returns an error if non-zero.
type Typography uint

const (
	// SmartQuotes substitutes straight quotes for curly quotes.
	SmartQuotes Typography = 1 << iota
	// Ellipses substitutes "..." for "…".
	Ellipses
)

// Span is a half-open byte range [Start, End) into the text passed to a
// Segmenter's Breaks method.
type Span = segment.Span

// Segmenter finds sentence boundaries in prose text. It is independently
// testable and swappable: provide Options.Segmenter to plug in something
// smarter than the built-in regex/abbreviation-list segmenter.
type Segmenter interface {
	// Breaks returns the whitespace gaps separating sentences in text: for
	// each boundary, the [start,end) byte range of the inter-sentence
	// whitespace. The caller (not the Segmenter) decides what replaces
	// each gap.
	Breaks(text string) []Span
}

// Options configures Format, Check, and FormatReader. The zero value is
// the default and is always valid: sentence mode, unbounded width, no
// typography substitutions, <br> hard-break style, the built-in segmenter
// with its default abbreviation list.
type Options struct {
	// Mode selects the reflow strategy. Zero value is ModeSentence.
	Mode Mode

	// MaxWidth bounds line length: 0 (default) is unbounded. Not
	// implemented until M3; Format returns an error if non-zero.
	MaxWidth int

	// Typography enables opt-in prose substitutions. Zero value is off.
	// Not implemented until M5; Format returns an error if non-zero.
	Typography Typography

	// HardBreaks selects the normalized hard-break style every preserved
	// hard break is rewritten to. Zero value is HardBreakBr.
	HardBreaks HardBreakStyle

	// StripSentenceTerminalBreaks treats a trailing double-space
	// immediately after sentence-terminal punctuation as an accidental
	// hard break and removes it — a documented, flag-reversible exception
	// to render preservation. Only that syntax, only that position: a
	// trailing backslash or <br>, or a double-space anywhere else, is
	// always respected.
	StripSentenceTerminalBreaks bool

	// Abbreviations adds to (never replaces) the built-in segmenter's
	// abbreviation exception list. Ignored if Segmenter is set.
	Abbreviations []string

	// Segmenter overrides the built-in sentence segmenter. Nil (default)
	// uses the built-in regex/abbreviation-list segmenter seeded with
	// DefaultAbbreviations plus Abbreviations.
	Segmenter Segmenter
}

// DefaultAbbreviations returns a copy of the built-in sentence segmenter's
// abbreviation exception list (see docs/design.md's "Sentence
// segmentation" section). Options.Abbreviations adds to this list; it is
// never replaced wholesale short of providing a custom Segmenter.
func DefaultAbbreviations() []string {
	return segment.DefaultAbbreviations()
}
