// Package gm provides the single goldmark configuration used throughout
// mdreflow: by the reflow pipeline to parse source Markdown, and by tests to
// render HTML for the render-preservation property check. Sharing one
// constructor keeps both uses in lockstep — a parser change here changes
// both consistently, which is the point.
package gm

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// New returns a freshly configured goldmark instance: CommonMark plus the
// GFM extension bundle (tables, strikethrough, linkify, task lists) for
// dialect recognition. See docs/design.md and docs/m0-spike-findings.md
// for why this specific set.
//
// No front-matter extension is registered: mdreflow never consumes front
// matter's parsed metadata, only needs the block kept out of reflow and
// emitted byte-for-byte, which package blockmap does with its own
// byte-range pre-scan (see blockmap's frontMatterEnd) rather than a
// goldmark parser hook — see docs/design.md's Dependencies section for why
// this replaced github.com/yuin/goldmark-meta.
//
// html.WithUnsafe() is set because this same instance renders HTML for the
// render-preservation property check (see format_test.go): without it,
// goldmark replaces raw HTML — including a source-authored "<br>" hard-
// break marker, which reflow keeps and canonicalizes — with an opaque
// "<!-- raw HTML omitted -->" comment, which would spuriously fail render
// preservation even though it renders identically to a true hard-break
// node once real HTML is allowed through. mdreflow's own reflow pipeline
// never renders anything, so this only affects test/CLI rendering, not
// parsing.
func New() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
		),
	)
}

// inlineExtensions is the inline-relevant subset of New's extension.GFM
// bundle, shared with NewInline below so the two configurations are
// constructed from one site: extension.GFM also carries Table and
// TaskList, which are block-level constructs a paragraph-only parser
// never sees, so NewInline lists only the extensions that change inline
// parsing (Linkify's bare-URL autolinking, Strikethrough's "~~text~~").
var inlineExtensions = []goldmark.Extender{
	extension.Linkify,
	extension.Strikethrough,
}

// NewInline returns a goldmark instance for computing breakable-region
// spans over a reflow cluster's joined text (see docs/design.md, "No-break
// spans: ask goldmark, not a hand grammar"). Its block layer is only
// goldmark's paragraph parser, with no paragraph transformers, so the
// cluster text — which exists nowhere in the document itself — always
// parses as exactly one Paragraph: it can never diverge into a List,
// Heading, blockquote, fence, or link reference definition the way a
// full block parse of the same bytes standalone could. The inline layer
// is goldmark's default inline parser set plus inlineExtensions, the
// dialect-relevant subset of New's GFM bundle.
func NewInline() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithParser(parser.NewParser(
			parser.WithBlockParsers(
				util.Prioritized(parser.NewParagraphParser(), 100),
			),
			parser.WithInlineParsers(parser.DefaultInlineParsers()...),
		)),
		goldmark.WithExtensions(inlineExtensions...),
	)
}
