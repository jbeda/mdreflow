// Package gm provides the single goldmark configuration used throughout
// mdreflow: by the reflow pipeline to parse source Markdown, and by tests to
// render HTML for the render-preservation property check. Sharing one
// constructor keeps both uses in lockstep — a parser change here changes
// both consistently, which is the point.
package gm

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// New returns a freshly configured goldmark instance: CommonMark plus the
// GFM extension bundle (tables, strikethrough, linkify, task lists) for
// dialect recognition, and goldmark-meta for YAML front matter (removed
// from the AST so it passes through byte-for-byte). See docs/design.md and
// docs/m0-spike-findings.md for why this specific set.
//
// html.WithUnsafe() is set because this same instance renders HTML for the
// render-preservation property check (see format_test.go): without it,
// goldmark replaces raw HTML — including a literal "<br>" hard-break
// marker, which Options.HardBreaks's default style writes into reflowed
// output — with an opaque "<!-- raw HTML omitted -->" comment, which would
// make HardBreakBr normalization spuriously fail render preservation even
// though it renders identically to a true hard-break node once real HTML
// is allowed through. mdreflow's own reflow pipeline never renders
// anything, so this only affects test/CLI rendering, not parsing.
func New() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			meta.Meta,
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
		),
	)
}
