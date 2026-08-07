// Package reflow implements the M1 reflow pipeline: given a parsed goldmark
// document and its source bytes, join each top-level paragraph's lines,
// find sentence boundaries, and splice the result back into the verbatim
// source. Everything outside a reflowed paragraph is copied byte-for-byte.
//
// Hard line breaks (trailing double-space, trailing backslash, `<br>`) are
// immovable: the pipeline never joins across one. Hard-break *style*
// normalization (design.md's HardBreakStyle) is not implemented yet — the
// original marker bytes are preserved exactly, which is the M1 bar
// ("not corrupting" hard breaks, not normalizing them).
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

// Format reflows every top-level paragraph in doc and returns the full
// document bytes, splicing reflowed prose into the otherwise-untouched
// source.
func Format(source []byte, doc ast.Node, seg Segmenter) []byte {
	paras := blockmap.Paragraphs(doc, source)

	var out bytes.Buffer
	cursor := 0
	for _, p := range paras {
		out.Write(source[cursor:p.Start])
		writeParagraph(&out, p.Node, source, seg)
		cursor = p.End
	}
	out.Write(source[cursor:])
	return out.Bytes()
}

// cluster is a maximal run of a paragraph's source lines joined by soft
// (movable) breaks, ending either at a hard break or at the paragraph's
// last line.
type cluster struct {
	text   string // joined, whitespace-normalized prose for this cluster
	marker string // hard-break marker bytes to re-emit verbatim, or ""
}

// writeParagraph joins node's lines into hard-break clusters, sentence-
// splits each cluster, and writes the sentence-per-line result to buf.
func writeParagraph(buf *bytes.Buffer, node *ast.Paragraph, source []byte, seg Segmenter) {
	lines := node.Lines()
	n := lines.Len()

	var clusters []cluster
	var curLines []string
	lastLineHasNewline := false
	for i := 0; i < n; i++ {
		lineSeg := lines.At(i)
		content, hasNewline := stripLineEnding(lineSeg.Value(source))
		if i == n-1 {
			lastLineHasNewline = hasNewline
		}
		marker, rest := detectHardBreak(content)
		curLines = append(curLines, strings.TrimSpace(rest))
		if marker != "" || i == n-1 {
			clusters = append(clusters, cluster{
				text:   strings.Join(curLines, " "),
				marker: marker,
			})
			curLines = nil
		}
	}

	for ci, cl := range clusters {
		sentences := splitSentences(cl.text, seg)
		for si, s := range sentences {
			buf.WriteString(s)
			if si != len(sentences)-1 {
				buf.WriteByte('\n')
				continue
			}
			buf.WriteString(cl.marker)
			if ci != len(clusters)-1 || lastLineHasNewline {
				buf.WriteByte('\n')
			}
		}
	}
}

// splitSentences takes one cluster's already-joined prose, asks seg for
// candidate breaks, drops any that land inside a no-break
// span (inline code, links), and cuts text at what remains. Each returned
// string is one output line: whitespace-trimmed, never containing the
// break whitespace itself.
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

// stripLineEnding removes a trailing "\n" or "\r\n" from raw, reporting
// whether one was present. CRLF paragraph content is thus normalized to LF
// on reflow; pass-through (non-paragraph) regions keep their original line
// endings untouched.
func stripLineEnding(raw []byte) (content string, hadNewline bool) {
	s := string(raw)
	switch {
	case strings.HasSuffix(s, "\r\n"):
		return s[:len(s)-2], true
	case strings.HasSuffix(s, "\n"):
		return s[:len(s)-1], true
	default:
		return s, false
	}
}

// hardBreakBrRE matches an inline <br> (with optional self-closing slash
// and internal spacing) trailing a line, along with any whitespace right
// before it.
var hardBreakBrRE = regexp.MustCompile(`(?i)[ \t]*<br\s*/?>[ \t]*$`)

// detectHardBreak inspects a line's content (line ending already
// stripped) for one of the three hard-break syntaxes mdreflow recognizes:
// trailing backslash, trailing double-space (or more), or a trailing
// <br>. On a match it returns the exact trailing marker bytes (to be
// re-emitted verbatim) and the remaining prose. On no match it returns an
// empty marker and the content unchanged.
func detectHardBreak(content string) (marker, rest string) {
	// Backslash: an odd run of trailing backslashes means the last one is
	// unescaped and forms a hard break; only that final backslash is the
	// marker, the rest (an escaped pair, if any) is prose.
	n := 0
	for n < len(content) && content[len(content)-1-n] == '\\' {
		n++
	}
	if n%2 == 1 {
		return content[len(content)-1:], content[:len(content)-1]
	}

	// Trailing spaces: two or more.
	i := len(content)
	for i > 0 && content[i-1] == ' ' {
		i--
	}
	if len(content)-i >= 2 {
		return content[i:], content[:i]
	}

	// <br>.
	if m := hardBreakBrRE.FindStringIndex(content); m != nil {
		return content[m[0]:], content[:m[0]]
	}

	return "", content
}
