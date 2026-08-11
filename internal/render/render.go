// Package render renders Markdown through the same goldmark
// configuration the reflow pipeline parses with (package gm) and
// normalizes the HTML for render-preservation comparison. It is the
// production home of what began as the fuzz harness's render oracle
// (docs/design.md, "Render preservation is also structural"): Format
// compares Normalized(input) against Normalized(output) after the
// fixpoint loop and falls back to the original document on any
// difference, so the normalization rules here are load-bearing — too
// loose and a real content change slips through, too strict and
// ordinary documents silently stop reflowing. Every rule is applied
// identically to both sides of a comparison, so a rule can only mask
// the specific cosmetic difference it names, never a content change.
package render

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"
)

// HTML renders src to HTML with the pipeline's goldmark configuration.
func HTML(src []byte) (string, error) {
	md := gm.New()
	doc := md.Parser().Parse(text.NewReader(src))
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Normalized is HTML followed by Normalize — the form two documents are
// compared in.
func Normalized(src []byte) (string, error) {
	h, err := HTML(src)
	if err != nil {
		return "", err
	}
	return Normalize(h), nil
}

// whitespaceRun matches a run of whitespace, including the literal "\n"
// goldmark's HTML renderer emits for a paragraph's soft line breaks.
var whitespaceRun = regexp.MustCompile(`\s+`)

// spaceBeforeBr matches a single space directly before a "<br>" tag, left
// over after whitespaceRun has already collapsed any longer run to one
// space.
var spaceBeforeBr = regexp.MustCompile(` <br>`)

// anyBrTag matches any spelling of a <br> tag: case-insensitive, optional
// internal spacing, optional self-closing slash — e.g. "<Br>", "<BR />",
// "<br/>". HTML tag names are case-insensitive per the HTML spec, so a
// browser renders all of these identically; goldmark's raw-HTML pass-
// through does not normalize them, so they can differ byte-for-byte
// without differing in rendered meaning. Reflow keeps a source-authored
// <br> but canonicalizes its spelling to "<br>" (docs/design.md, "Hard
// line breaks"), so this rule canonicalizes both sides of a comparison
// the same way — found by FuzzFormat on input "\x00<Br>\n00".
var anyBrTag = regexp.MustCompile(`(?i)<br\s*/?>`)

// Normalize collapses whitespace runs to a single space before comparing
// rendered HTML. Reflow moves *where* a paragraph's soft line breaks
// fall without changing that they render as inter-word whitespace (a
// browser collapses "\n" the same as " "), so a literal byte comparison
// of the HTML would flag every reflowed paragraph as a false positive.
//
// It also drops a single leftover space immediately before "<br>". This is
// a second, narrower normalization for a goldmark rendering quirk found by
// FuzzFormat: CommonMark attaches no meaning to more than two trailing
// spaces before a hard break, but goldmark's HTML renderer keeps
// spaces-beyond-two as literal preceding text instead of also collapsing
// them into the break, e.g. "x    \ny" (4 trailing spaces) renders as
// "x <br>\ny", not "x<br>\ny". mdreflow's hard-break detection treats any
// run of 2+ trailing spaces as one break (matching the CommonMark spec's
// stated semantics) and, when it promotes such a run to a backslash, has
// one canonical output regardless of how many spaces the source used, so
// it does not reproduce that single leftover space. Dropping it here
// (after the whitespace collapse, so it also can't reappear from an
// unrelated spelled-out multi-space run elsewhere) treats it as the
// goldmark rendering artifact it is, not a real content difference — a
// browser collapses that one space against the block boundary identically
// either way.
func Normalize(html string) string {
	s := whitespaceRun.ReplaceAllString(html, " ")
	s = anyBrTag.ReplaceAllString(s, "<br>")
	s = spaceBeforeBr.ReplaceAllString(s, "<br>")
	return strings.TrimSpace(s)
}
