package mdreflow

import (
	"bytes"
	"unicode/utf8"

	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/blockmap"
	"github.com/jbeda/mdreflow/internal/gm"
)

// SkipReason is a stable, machine-legible code naming the guard that
// froze a paragraph. Codes are part of the public surface: agents and
// scripts may branch on them. New codes may be added; existing ones do
// not change meaning.
type SkipReason string

const (
	// SkipLinkRefDefShape: the paragraph contains a line shaped like a
	// link reference definition ("[label]: ...").
	SkipLinkRefDefShape SkipReason = "link-ref-def-shape"
	// SkipLinkRefDefNeighbor: the paragraph sits directly against link-
	// reference-definition machinery above it, within a definition's
	// possible reach.
	SkipLinkRefDefNeighbor SkipReason = "link-ref-def-neighbor"
	// SkipPossibleLinkRefDef: reflow could complete an unbalanced "[" or
	// an unclosed destination into a real definition.
	SkipPossibleLinkRefDef SkipReason = "possible-link-ref-def"
	// SkipRawHTMLDeclOpener: a raw "<?" or "<!" opener outside a code
	// span.
	SkipRawHTMLDeclOpener SkipReason = "raw-html-decl-opener"
	// SkipUnterminatedTag: the paragraph opens with an HTML/JSX tag whose
	// closing ">" is on a later line.
	SkipUnterminatedTag SkipReason = "unterminated-tag"
	// SkipTableAdjacency: the paragraph sits directly under a table with
	// no blank line between them.
	SkipTableAdjacency SkipReason = "table-adjacency"
	// SkipDeepNesting: the paragraph is nested more than two container
	// (list/blockquote) levels deep.
	SkipDeepNesting SkipReason = "deep-nesting"
	// SkipControlBytes: the paragraph contains a C0 control byte other
	// than tab, CR, or LF.
	SkipControlBytes SkipReason = "control-bytes"
	// SkipDegenerateBlank: the paragraph (or one of its lines) trims to
	// nothing — a control-character parser artifact.
	SkipDegenerateBlank SkipReason = "degenerate-blank"
	// SkipHiddenLineGap: the parser carved another construct's bytes out
	// of this paragraph's line range.
	SkipHiddenLineGap SkipReason = "hidden-line-gap"
	// SkipDoubleOwnedLine: a line belongs to both this paragraph and a
	// duplicate link-reference definition.
	SkipDoubleOwnedLine SkipReason = "duplicate-def-line"
	// SkipDialectBlock: the block is a recognized non-prose dialect
	// construct (front-matter fence, math block, MDX, shortcode).
	SkipDialectBlock SkipReason = "dialect-block"
)

// FrozenParagraph reports one paragraph Format passes through
// byte-for-byte instead of reflowing: where it is, which guard froze it,
// and what an author can do about it. Line numbers are 1-based and
// inclusive, counted in the source given to Explain.
type FrozenParagraph struct {
	StartLine, EndLine int
	Reason             SkipReason
	// Detail is a short human-readable statement of what fired, phrased
	// to follow "skipped: ".
	Detail string
	// Remediation says how to make the paragraph reflowable, when there
	// is something to do; frozen paragraphs are always preserved
	// byte-for-byte, so hand-formatting once is always an option.
	Remediation string
}

// skipWording maps each internal skip reason to its public code and
// user-facing wording. Remediations follow docs/why-this-is-hard.md's
// "What authors can do about a frozen paragraph": move Markdown-syntax-
// looking literals into fenced code blocks, blank-separate neighbors
// (noting the tight-list render cost), or hand-format once — and never
// suggest backslash-escaping, which the zone deliberately ignores.
var skipWording = map[blockmap.SkipReason]FrozenParagraph{
	blockmap.SkipLinkRefDefShape: {
		Reason: SkipLinkRefDefShape,
		Detail: `paragraph contains a "[label]:" shape (link-reference-definition zone)`,
		Remediation: `Move the literal into a fenced code block, or format the paragraph by hand — ` +
			`mdreflow preserves it byte-for-byte. Backslash-escaping the bracket does not help: ` +
			`the zone judges escaped and unescaped spellings alike.`,
	},
	blockmap.SkipLinkRefDefNeighbor: {
		Reason: SkipLinkRefDefNeighbor,
		Detail: `paragraph sits directly below a "[label]:" definition shape, within its possible reach`,
		Remediation: `Separate the paragraph from the definition lines with a blank line ` +
			`(inside a list this makes the list loose, which changes rendering), ` +
			`or format the paragraph by hand — mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipPossibleLinkRefDef: {
		Reason: SkipPossibleLinkRefDef,
		Detail: `reflow could complete the paragraph's unbalanced "[" or unclosed link destination into a link reference definition`,
		Remediation: `Move the bracketed literal into a fenced code block, or format the paragraph by hand — ` +
			`mdreflow preserves it byte-for-byte. Backslash-escaping the bracket does not help: ` +
			`escaped spellings are judged the same way.`,
	},
	blockmap.SkipRawHTMLDeclOpener: {
		Reason: SkipRawHTMLDeclOpener,
		Detail: `paragraph contains a raw "<?" or "<!" opener outside a code span`,
		Remediation: `Move the snippet into a fenced code block, or format the paragraph by hand — ` +
			`mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipUnterminatedTag: {
		Reason: SkipUnterminatedTag,
		Detail: `paragraph opens with an HTML/JSX tag whose closing ">" is on a later line`,
		Remediation: `Close the tag on the line it opens on, move it into a fenced code block, ` +
			`or format the paragraph by hand — mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipTableAdjacency: {
		Reason: SkipTableAdjacency,
		Detail: `paragraph sits directly under a table with no blank line between them`,
		Remediation: `Add a blank line between the table and the paragraph, ` +
			`or format the paragraph by hand — mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipDeepNesting: {
		Reason: SkipDeepNesting,
		Detail: `paragraph is nested more than two list/blockquote levels deep`,
		Remediation: `Reduce the nesting depth, or format the paragraph by hand — ` +
			`mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipControlBytes: {
		Reason:      SkipControlBytes,
		Detail:      `paragraph contains a control character (other than tab or line endings)`,
		Remediation: `Remove the control character; these are never produced by ordinary text editing.`,
	},
	blockmap.SkipDegenerateBlank: {
		Reason:      SkipDegenerateBlank,
		Detail:      `paragraph has a line with no visible content (control-character artifact)`,
		Remediation: `Remove the invisible characters; these are never produced by ordinary text editing.`,
	},
	blockmap.SkipHiddenLineGap: {
		Reason: SkipHiddenLineGap,
		Detail: `link-reference-definition machinery is interleaved with the paragraph's own lines`,
		Remediation: `Separate the definitions from the prose with a blank line, ` +
			`or format the paragraph by hand — mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipDoubleOwnedLine: {
		Reason: SkipDoubleOwnedLine,
		Detail: `a line belongs to both this paragraph and a duplicated "[label]:" definition`,
		Remediation: `Remove or rename the duplicate definition label, ` +
			`or format the paragraph by hand — mdreflow preserves it byte-for-byte.`,
	},
	blockmap.SkipDialectBlock: {
		Reason:      SkipDialectBlock,
		Detail:      `block is a non-prose dialect construct (front matter, math, MDX, or shortcode)`,
		Remediation: `Nothing to do: this construct is intentionally never reflowed.`,
	},
}

// Explain reports every paragraph Format would pass through byte-for-byte
// instead of reflowing, in source order, with the guard that froze it and
// a remediation hint. It is purely diagnostic: it never writes anything,
// and Format's output is unaffected by whether Explain runs.
//
// Explain judges src as given (the same verdicts Format's first pass
// uses). Options participate the same way they do in Format — the dialect
// selects block recognition — and invalid options or non-UTF-8 input fail
// with the same errors Format returns.
func Explain(src []byte, opts Options) ([]FrozenParagraph, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	if !utf8.Valid(src) {
		return nil, ErrInvalidUTF8
	}
	doc := gm.New().Parser().Parse(text.NewReader(src))
	skips := blockmap.SkipsForDialect(doc, src, opts.Dialect == DialectMkDocs)
	out := make([]FrozenParagraph, 0, len(skips))
	for _, s := range skips {
		w, ok := skipWording[s.Reason]
		if !ok {
			continue
		}
		w.StartLine = 1 + bytes.Count(src[:s.Start], []byte("\n"))
		end := s.End
		if end > s.Start && end <= len(src) && src[end-1] == '\n' {
			end--
		}
		w.EndLine = 1 + bytes.Count(src[:end], []byte("\n"))
		out = append(out, w)
	}
	return out, nil
}
