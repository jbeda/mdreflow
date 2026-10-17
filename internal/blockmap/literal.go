package blockmap

import (
	"bytes"
	"regexp"

	"github.com/yuin/goldmark/ast"
)

// templateOpenerRE matches an MDX expression that opens with a template
// literal: `{` then a backtick, as in code={`…`} or <CodeBlock>{`…`}.
var templateOpenerRE = regexp.MustCompile("\\{\\s*`")

// literalChain freezes the paragraphs of an MDX template literal that spans
// lines.
//
// A code sample passed to a component as a template literal, as an attribute
// (code={`…`}), as children (<CodeBlock>{`…`}</CodeBlock>) or inside an
// object (files={{"a.js": `…`}}), spans lines, and its whitespace is the
// code. Reflowing the paragraph that holds it joins those lines; and where
// the literal holds blank lines, goldmark ends a paragraph at each one, so
// the rest of the literal becomes paragraphs that read as prose.
//
// So a paragraph is frozen (SkipTemplateLiteral) when it opens a literal that
// runs past the end of its line ('{', optional whitespace, a backtick; see
// opensTemplate), or when it leaves the count of unescaped backticks odd with
// a '{' before the unmatched one. The second case also starts a chain: the
// walk keeps counting backticks in the raw source, and while the count is
// odd, each paragraph that starts there is inside the literal and frozen too.
// The first paragraph that starts with the count even ends the chain.
//
// Only backticks are counted, not JavaScript or JSX syntax. A line that opens
// with an unescaped run of three backticks cannot be inside a real template
// literal, where it would have to be escaped, so it ends the chain: a count
// thrown off by a backtick in a quoted string then stops at the next code
// fence instead of running on out of phase. A blank line elsewhere in an
// expression (between attributes, inside an object literal) keeps the old
// handling. The chain is idempotent: every paragraph it reads is frozen, and
// the blocks between them never reflow.
type literalChain struct {
	active bool
	pos    int  // source offset the count has reached
	odd    bool // the count of unescaped backticks up to pos is odd
}

// advance counts unescaped backticks in source[lc.pos:to]. It reports false,
// leaving the count meaningless, if it meets a line that opens with an
// unescaped code fence.
func (lc *literalChain) advance(source []byte, to int) bool {
	for ; lc.pos < to; lc.pos++ {
		if lc.pos > 0 && source[lc.pos-1] == '\n' && opensWithFence(source[lc.pos:]) {
			return false
		}
		if source[lc.pos] == '`' && !backslashEscaped(source, lc.pos) {
			lc.odd = !lc.odd
		}
	}
	return true
}

// inside reports whether paragraph p, the next one in document order, opens
// or lies inside a template literal that holds a blank line, and so must be
// frozen. collect also passes HTML blocks, which never reflow but can open a
// literal, and ignores the answer.
func (lc *literalChain) inside(p ast.Node, source []byte) bool {
	lines := p.Lines()
	if lines.Len() == 0 {
		return false
	}
	start, stop := lines.At(0).Start, lines.At(lines.Len()-1).Stop
	if lc.active && lc.advance(source, start) && lc.odd {
		return true
	}
	*lc = literalChain{}
	para := source[start:stop]
	opens := opensTemplate(para)
	odd, unmatched := oddBackticks(para)
	if odd && bytes.IndexByte(para[:unmatched], '{') >= 0 {
		*lc = literalChain{active: true, pos: stop, odd: true}
		return true
	}
	return opens
}

// opensTemplate reports whether para holds an MDX expression that opens a
// template literal spanning a line break: '{', optional whitespace, then a
// backtick, with an even number of backticks before the '{' (so it is not
// inside a code span), and no closing backtick before the end of its line.
// A literal on one line is safe to leave to reflow, which never breaks inside
// what goldmark reads as a code span; requiring the line break keeps prose
// such as "N × {`Before`, `After`}" reflowing.
func opensTemplate(para []byte) bool {
	for _, m := range templateOpenerRE.FindAllIndex(para, -1) {
		if odd, _ := oddBackticks(para[:m[0]]); odd {
			continue
		}
		rest := para[m[1]:]
		eol := bytes.IndexByte(rest, '\n')
		if eol < 0 {
			eol = len(rest)
		}
		if odd, _ := oddBackticks(rest[:eol]); !odd {
			return true // no closing backtick on the opener's line
		}
	}
	return false
}

// oddBackticks reports whether b holds an odd number of unescaped backticks,
// and if so the offset of the last one.
func oddBackticks(b []byte) (bool, int) {
	odd, last := false, -1
	for i, c := range b {
		if c == '`' && !backslashEscaped(b, i) {
			odd = !odd
			last = i
		}
	}
	return odd, last
}

// opensWithFence reports whether line, after leading spaces and tabs, opens
// with a run of three or more backticks.
func opensWithFence(line []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("```"))
}

// backslashEscaped reports whether the byte at source[i] is preceded by an
// odd run of backslashes.
func backslashEscaped(source []byte, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && source[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}
