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
)

// New returns a freshly configured goldmark instance: CommonMark plus the
// GFM extension bundle (tables, strikethrough, linkify, task lists) for
// dialect recognition, and goldmark-meta for YAML front matter (removed
// from the AST so it passes through byte-for-byte). See docs/design.md and
// docs/m0-spike-findings.md for why this specific set.
func New() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			meta.Meta,
		),
	)
}
