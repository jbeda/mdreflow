// Package blockmap derives the set of reflow-eligible paragraphs from a
// goldmark AST.
//
// For M1 the map is deliberately narrow: only top-level paragraphs (direct
// children of the document) are eligible. This means:
//
//   - Anything goldmark parses as a non-Paragraph node at the top level
//     (fenced/indented code blocks, headings, HTML blocks, lists,
//     blockquotes, tables, thematic breaks, ...) is never touched by the
//     block map, so the splice architecture in package reflow emits it
//     byte-for-byte automatically.
//   - YAML front matter is removed from the AST entirely by goldmark-meta
//     before the block map ever sees it, so it passes through the same way.
//   - Paragraphs nested inside a list item or blockquote are also left
//     untouched (M2 adds continuation-indented reflow for those); this falls
//     out of only walking direct document children, no extra code needed.
package blockmap

import (
	"github.com/yuin/goldmark/ast"
)

// Paragraph describes one top-level, reflow-eligible paragraph.
type Paragraph struct {
	// Node is the goldmark AST node for the paragraph.
	Node *ast.Paragraph
	// Start and End are the byte offsets, into the source, spanned by the
	// paragraph's raw source lines (End is exclusive).
	Start, End int
}

// Paragraphs walks the direct children of doc and returns every top-level
// Paragraph node, in source order, with its byte range.
//
// doc must be the *ast.Document returned by parsing source with the
// mdreflow-configured goldmark instance (see package gm); source must be
// the exact bytes that were parsed, since the returned ranges index into it.
func Paragraphs(doc ast.Node, source []byte) []Paragraph {
	var out []Paragraph
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		p, ok := n.(*ast.Paragraph)
		if !ok {
			continue
		}
		lines := p.Lines()
		if lines.Len() == 0 {
			continue // empty paragraph; nothing to reflow or emit specially
		}
		start := lines.At(0).Start
		end := lines.At(lines.Len() - 1).Stop
		out = append(out, Paragraph{Node: p, Start: start, End: end})
	}
	return out
}
