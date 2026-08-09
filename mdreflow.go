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
// The library API in docs/design.md is complete: all three modes,
// MaxWidth, hard-break normalization, the abbreviation list, and a
// pluggable Segmenter are all implemented.
package mdreflow

import (
	"github.com/jbeda/mdreflow/internal/opts"
	"github.com/jbeda/mdreflow/internal/segment"
)

// Mode selects the top-level reflow strategy.
//
// (Mode, HardBreakStyle, and their constants are aliases of one shared
// definition in an internal leaf package the pipeline uses directly, so
// the public and internal values can never drift; this alias is the
// documented, supported name.)
type Mode = opts.Mode

const (
	// ModeSentence joins each paragraph's lines and splits at sentence
	// boundaries, one sentence per line. The default mode.
	ModeSentence Mode = opts.ModeSentence
	// ModePara joins each paragraph (each hard-break cluster — see
	// HardBreakStyle) to a single line, with no further splitting.
	// Options.MaxWidth must be 0 in this mode: para mode's whole point is
	// one unconditional line, so a non-zero MaxWidth has nothing to apply
	// to and Format returns an error rather than silently ignoring it —
	// see Options.MaxWidth's doc comment.
	ModePara Mode = opts.ModePara
	// ModeWrap hard-wraps at Options.MaxWidth, breaking at word
	// boundaries only: a no-break span or a single word wider than the
	// limit overflows rather than being split. Options.MaxWidth of 0
	// defaults to 80 in this mode only — see Options.MaxWidth's doc
	// comment.
	ModeWrap Mode = opts.ModeWrap
)

// HardBreakStyle selects how hard line breaks are normalized when
// reflowed prose moves them to a new position in the source. Every
// preserved hard break (however it was originally spelled) is rewritten to
// this style.
type HardBreakStyle = opts.HardBreakStyle

const (
	// HardBreakBr renders a hard break as a literal <br>. Default: an
	// accidental double-space hard break survives formatting but becomes
	// loudly visible in a diff.
	HardBreakBr HardBreakStyle = opts.HardBreakBr
	// HardBreakSpaces renders a hard break as a trailing double space.
	HardBreakSpaces HardBreakStyle = opts.HardBreakSpaces
	// HardBreakBackslash renders a hard break as a trailing backslash.
	HardBreakBackslash HardBreakStyle = opts.HardBreakBackslash
)

// Span is a half-open byte range [Start, End) into the text passed to a
// Segmenter's Breaks method. It is a concrete struct (not an alias to an
// internal type) so its fields are visible in rendered documentation;
// Format adapts it at the Segmenter boundary.
type Span struct {
	Start, End int
}

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

// MinMaxWidth is the smallest non-zero Options.MaxWidth Format accepts —
// see Options.MaxWidth's doc comment for why tiny widths are refused.
const MinMaxWidth = 20

// Dialect names the renderer profile a document tree targets. It is a
// single-select enum like Mode, and a bundle rather than a feature flag:
// a dialect selects which flavor-specific block recognitions are on
// (docs/design.md, "Dialects: renderer profiles, and the skip-list").
type Dialect = opts.Dialect

const (
	// DialectGFM is the default: the permissive GitHub-flavored superset
	// (GFM extensions plus footnotes). There is deliberately no
	// "commonmark" value — that name is kept for a possible future
	// strict profile with the GFM extensions off, and aliasing it to the
	// default would burn the name on the one thing it doesn't accurately
	// describe.
	DialectGFM Dialect = opts.DialectGFM
	// DialectMkDocs additionally reflows MkDocs / Python-Markdown
	// admonition bodies ("!!! note" plus a 4-space-indented body). A
	// CommonMark parser can only see such a body as an indented code
	// block, so reflowing it changes what a CommonMark renderer emits —
	// which is why it is opt-in and can never be the default.
	DialectMkDocs Dialect = opts.DialectMkDocs
)

// Options configures Format, NeedsFormat, and FormatReader. The zero value is
// the default and is always valid: sentence mode, unbounded width, <br>
// hard-break style, the GFM dialect, the built-in segmenter with its
// default abbreviation list.
type Options struct {
	// Mode selects the reflow strategy. Zero value is ModeSentence.
	Mode Mode

	// Dialect opts into flavor-specific block recognition. Zero value
	// is DialectGFM (today's behavior); see the Dialect constants.
	Dialect Dialect

	// MaxWidth bounds line length, measured in runes — not bytes, and not
	// Unicode grapheme clusters or East-Asian display width. That is a
	// deliberate, pragmatic simplification (the same starting point every
	// shipping line-wrap tool uses); full grapheme-cluster/width-aware
	// measurement is a documented future refinement, not implemented
	// here. In every mode, a no-break span (inline code, a link, ...) or
	// a single word wider than the limit overflows rather than being
	// split.
	//
	// A non-zero MaxWidth below MinMaxWidth (20) is an options error.
	// Nearly every pathological width behavior the fuzz campaign found
	// needed single-digit widths, where the geometry forces breaks
	// inside constructs; no real document wants width 12. Refusing the
	// range outright deletes that adversarial surface from the product
	// (docs/design.md, "The width floor").
	//
	// MaxWidth's meaning is mode-dependent:
	//
	//   - ModeSentence: 0 (default) leaves long sentences alone. A
	//     non-zero value adds a secondary break to any sentence longer
	//     than MaxWidth, at the last clause boundary (a comma or
	//     semicolon followed by a space) before the limit, falling back
	//     to the last word boundary before the limit, falling back to an
	//     overflowing single word or no-break span if neither exists.
	//   - ModeWrap: classic hard wrap at word boundaries. 0 means 80 in
	//     this mode specifically, matching the CLI's --max-width default
	//     (docs/design.md's CLI table) — the one place in this package a
	//     zero MaxWidth does not mean "unbounded".
	//   - ModePara: MaxWidth must be 0. A paragraph always joins to a
	//     single line unconditionally in this mode, so Format returns an
	//     error for a non-zero MaxWidth rather than silently ignoring it.
	MaxWidth int

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
