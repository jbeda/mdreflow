package blockmap

import (
	"bytes"

	"github.com/yuin/goldmark/ast"
)

// fenceChain freezes the paragraphs that MDX reads as the inside of a code
// fence opened in an HTML block.
//
// A line holding only a tag, such as "<Playground>", opens a CommonMark HTML
// block that runs to the next blank line, so a fence directly under it is
// HTML, not a fence. After the first blank line inside the fence, goldmark
// reads the code as paragraphs, and reflowing them rewrites the code. MDX
// has no HTML blocks: it reads the tag as JSX and the fence as code.
//
// So when an HTML block leaves a fence open, the walk follows the fence
// through the raw lines that come after it, and freezes each paragraph that
// starts inside it (SkipFenceInHTMLBlock). The first paragraph or HTML block
// that starts outside the fence ends the chain. Only fence lines are read:
// a line opening with a run of three or more backticks or tildes opens a
// fence, and a run of the same character at least as long with nothing
// after it closes one. Indentation is ignored, since MDX disables indented
// code.
//
// The closing fence line opens a fence to goldmark, which runs to the next
// bare fence line. If that line opens another fence to MDX, its code is
// paragraphs to goldmark again, so the chain carries on through it.
//
// The chain is idempotent: it reads only the HTML block, the paragraphs it
// freezes and the blocks between them, none of which reflow. In plain
// Markdown, where the HTML block is real, the frozen paragraphs render the
// same either way, so the only cost is that they are not reflowed.
type fenceChain struct {
	active bool
	pos    int  // source offset the scan has reached, at a line start
	open   bool // a fence is open at pos
	char   byte // the open fence's character
	run    int  // the open fence's run length
}

// htmlBlock follows HTML block b, the next HTML block or paragraph in
// document order, and starts the chain if b leaves a fence open.
func (fc *fenceChain) htmlBlock(b *ast.HTMLBlock, source []byte) {
	lines := b.Lines()
	if lines.Len() == 0 {
		return
	}
	end := lines.At(lines.Len() - 1).Stop
	if b.HasClosure() {
		end = b.ClosureLine.Stop
	}
	if !fc.active {
		*fc = fenceChain{active: true, pos: lineStart(source, lines.At(0).Start)}
	}
	fc.advance(source, end)
	if !fc.open {
		*fc = fenceChain{}
	}
}

// inside reports whether paragraph p, the next one in document order,
// starts inside a fence an HTML block opened, and so must be frozen.
func (fc *fenceChain) inside(p ast.Node, source []byte) bool {
	if !fc.active {
		return false
	}
	lines := p.Lines()
	if lines.Len() == 0 {
		return false
	}
	fc.advance(source, lines.At(0).Start)
	if !fc.open {
		*fc = fenceChain{}
		return false
	}
	fc.advance(source, lines.At(lines.Len()-1).Stop)
	return true
}

// advance reads each line that starts in source[fc.pos:to], opening and
// closing fences, and leaves fc.pos at the start of the next line.
func (fc *fenceChain) advance(source []byte, to int) {
	for fc.pos < to && fc.pos < len(source) {
		end := bytes.IndexByte(source[fc.pos:], '\n')
		if end < 0 {
			end = len(source)
		} else {
			end += fc.pos
		}
		fc.line(bytes.TrimLeft(source[fc.pos:end], " \t"))
		fc.pos = end + 1
	}
}

// line applies one source line, its indentation removed, to the fence state.
func (fc *fenceChain) line(l []byte) {
	if len(l) == 0 || (l[0] != '`' && l[0] != '~') {
		return
	}
	run := 0
	for run < len(l) && l[run] == l[0] {
		run++
	}
	if run < 3 {
		return
	}
	rest := l[run:]
	if fc.open {
		if l[0] == fc.char && run >= fc.run && len(bytes.TrimSpace(rest)) == 0 {
			fc.open = false
		}
		return
	}
	// A backtick fence's info string cannot hold a backtick; such a line
	// is inline code.
	if l[0] == '`' && bytes.IndexByte(rest, '`') >= 0 {
		return
	}
	fc.open, fc.char, fc.run = true, l[0], run
}
