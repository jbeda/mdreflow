// Package gm provides the single goldmark configuration used throughout
// mdreflow: by the reflow pipeline to parse source Markdown, and by tests to
// render HTML for the render-preservation property check. Sharing one
// constructor keeps both uses in lockstep — a parser change here changes
// both consistently, which is the point.
package gm

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
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

// IsCompleteLinkRefDefLine reports whether line, parsed on its own, is
// exactly one complete link reference definition and nothing else.
//
// It is deliberately empirical rather than a hand-mirrored grammar: what
// counts as a definition turns on goldmark's own implementation details
// (up to 3 columns of indent and \f accepted as separator whitespace, yet
// \f inside a destination token continues it — the skip-before and
// end-of-token whitespace classes simply differ), so the question is put
// to the same parser whose reparse the callers exist to control.
//
// It is also label-shape-agnostic, which is the point at the blockmap call
// site: no footnote extension is registered in New, so "[^1]: /url" is an
// ordinary definition to goldmark and exactly as able to swallow the line
// below it as "[docs]: /url" is, while "[^1]: ordinary body prose" is not
// a definition at all and stays reflowable.
//
// The "]:" pre-filter keeps the parse off the hot path for ordinary prose.
// An already-escaped "\[label]:" line parses as a paragraph and correctly
// answers false, so escapes never stack.
func IsCompleteLinkRefDefLine(line string) bool {
	if !strings.Contains(line, "]:") {
		return false
	}
	doc := New().Parser().Parse(text.NewReader([]byte(line)))
	first := doc.FirstChild()
	return first != nil && first.Kind() == ast.KindLinkReferenceDefinition && first.NextSibling() == nil
}

// LinkRefDefs returns the set of link reference definitions src forms, each
// rendered as a "label\x00destination\x00title" key.
//
// goldmark records definitions in the parser context rather than the AST
// (only its duplicate-label handling leaves one behind as a node), and
// parser.Reference carries no source position — only label, destination and
// title. So this cannot answer "is there a definition at offset N"; it
// answers "which definitions exist", which is what comparing a paragraph
// before and after reflow needs.
func LinkRefDefs(src []byte) map[string]struct{} {
	ctx := parser.NewContext()
	New().Parser().Parse(text.NewReader(src), parser.WithContext(ctx))
	out := make(map[string]struct{})
	for _, r := range ctx.References() {
		out[string(r.Label())+"\x00"+string(r.Destination())+"\x00"+string(r.Title())] = struct{}{}
	}
	return out
}

// FormsNewLinkRefDef reports whether after holds a link reference definition
// that before did not — the structural signature of reflow manufacturing a
// definition out of prose, or feeding an existing one a title.
//
// This is checked on reflow's *candidate output* rather than on its input,
// which is what distinguishes it from IsCompleteLinkRefDefLine. The hazard
// does not exist in the source: a paragraph's own bytes are inert until a
// break lands inside them and strands a "label]:" fragment at a line start,
// or splits a following paragraph so its first line reads as a title. There
// is nothing dangerous to inspect beforehand, so the parser has to be asked
// about the text reflow proposes to emit.
//
// The comparison window must contain every line the definition would occupy.
// A paragraph's own bytes are enough for a definition manufactured *inside*
// it by a break, which is what this call site checks. They are not enough
// when the definition sits on the line above and the paragraph's first line
// becomes its title: nothing forms within the paragraph alone, so that case
// is caught earlier, by the def-chain-start fact in package blockmap.
//
// What makes even the narrow window sound is that definition formation is
// decided by the lines involved and nothing else. A general render
// comparison at this scope would not be: a reference *link* resolves against
// definitions elsewhere in the document, so a paragraph containing "[foo]"
// renders differently alone than it does in place.
//
// Both directions of difference are not equivalent, and only this one is
// checked: a definition appearing is corruption (prose leaves the page),
// while one disappearing cannot happen, since reflow never joins a
// definition line into a paragraph.
func FormsNewLinkRefDef(before, after []byte) bool {
	if bytes.Equal(before, after) || !bytes.Contains(after, []byte("]:")) {
		return false
	}
	was := LinkRefDefs(before)
	for k := range LinkRefDefs(after) {
		if _, existed := was[k]; !existed {
			return true
		}
	}
	return false
}
