package blockmap

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
)

// esmChain freezes the paragraphs of an MDX import/export statement.
//
// MDX reads a line that starts, at column 1 outside any container, with
// "import " or "export " as the start of an ESM block of JavaScript. The
// block runs to a blank line, or past it while the code there is incomplete,
// as in a function body with blank lines in it. goldmark sees paragraphs:
// the existing mdx-import-export rule froze one only when every line starts
// with import or export, so a statement over several lines (a function, a
// styled-components template, an object) was reflowed into broken
// JavaScript.
//
// The paragraph that starts a block is frozen, and so is each following
// paragraph whose first line continues the statement: indented, or opening
// with a closing bracket, as code after a blank line inside a statement
// almost always does. The first paragraph that does neither ends the block.
// Telling incomplete code from complete would take a JavaScript parser; this
// approximation can only freeze a paragraph too many (an indented paragraph
// right after a complete statement) or end the block early, where the code
// that follows reflows as it did before. It is idempotent: it reads only the
// first line of each paragraph it freezes, and of the paragraph that ends
// it, whose first line reflow never moves.
type esmChain struct {
	active bool
}

// inside reports whether paragraph p, the next top-level one in document
// order, starts or continues an import/export statement, and so must be
// frozen.
func (ec *esmChain) inside(p ast.Node, source []byte) bool {
	lines := p.Lines()
	if lines.Len() == 0 {
		return false
	}
	ls := lineStart(source, lines.At(0).Start)
	line := source[ls:]
	if ec.active && len(line) > 0 && strings.IndexByte(" \t})]", line[0]) >= 0 {
		return true
	}
	ec.active = bytes.HasPrefix(line, []byte("import ")) || bytes.HasPrefix(line, []byte("export "))
	return ec.active
}
