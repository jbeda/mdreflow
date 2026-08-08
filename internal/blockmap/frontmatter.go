package blockmap

import "bytes"

// frontMatterDelim is the exact line content (a physical line, trimmed only
// of its own line terminator — never of interior whitespace) that both
// opens and closes a YAML front-matter block.
//
// mdreflow deliberately requires exactly "---": not goldmark-meta's looser
// trigger (any line consisting solely of '-' characters — a bare "-", "--",
// or any longer run all qualified), which caused a family of fuzz-found
// parser-artifact edge cases now removed along with goldmark-meta itself
// (see docs/design.md's Dependencies section). "---" is also the
// convention every tool that actually matters here uses — Hugo, Jekyll,
// Docusaurus, mdslw — so tightening to it costs nothing in practice.
const frontMatterDelim = "---"

// frontMatterEnd returns the byte offset one past the end of source's
// leading YAML front-matter block — through the closing delimiter line's
// own trailing newline, or through end of input if that line has none —
// or -1 if source has no front-matter block.
//
// A front-matter block requires:
//
//   - source's very first physical line, trimmed of a trailing '\r', to be
//     exactly "---".
//   - a later physical line, also trimmed of a trailing '\r' and nothing
//     else, that is also exactly "---" (the closing delimiter). If no such
//     line exists before EOF, source is treated as NOT having front
//     matter at all — it is left for goldmark to parse as ordinary
//     Markdown (see docs/m0-spike-findings.md's "plain goldmark+GFM" row:
//     a ThematicBreak, then whatever the "YAML" lines parse as). This is
//     the deliberately conservative choice goldmark-meta did not make (it
//     swallowed the whole rest of the document as attempted front matter
//     on an unterminated opener, then injected a parse-error artifact into
//     the AST when the swallowed text failed to parse as YAML): malformed
//     or merely incomplete input is safer treated as never-front-matter
//     than as front-matter-that-failed.
//   - A closing "..." (valid per the YAML spec's own document-end marker,
//     but not a convention any of Hugo/Jekyll/Docusaurus/mdslw use for
//     Markdown front matter) is deliberately NOT recognized as a closer.
//
// The block's interior bytes are never parsed as YAML or anything else —
// opaque to mdreflow, which only needs the byte range to keep out of
// reflow. build's frontMatterEnd-derived skip check is a pure byte-range
// test, so it applies regardless of what goldmark's own stock block
// parsing happens to make of those interior lines (Paragraph, List,
// ThematicBreak, or — when the last front-matter line before the closing
// delimiter is itself paragraph-shaped text — a Setext heading the
// delimiter line completes); none of those node shapes can straddle the
// delimiter line itself, so "does this paragraph's own range start before
// frontMatterEnd" is always exactly right, never a partial overlap.
func frontMatterEnd(source []byte) int {
	if !isFrontMatterDelimLine(source, 0) {
		return -1
	}
	for pos := lineEndInclusive(source, 0); pos < len(source); {
		end := lineEndInclusive(source, pos)
		if isFrontMatterDelimLine(source, pos) {
			return end
		}
		pos = end
	}
	return -1
}

// lineEndInclusive returns the offset just past the '\n' terminating the
// physical line starting at start, or len(source) if that line is the
// last, unterminated line in source.
func lineEndInclusive(source []byte, start int) int {
	if i := bytes.IndexByte(source[start:], '\n'); i >= 0 {
		return start + i + 1
	}
	return len(source)
}

// isFrontMatterDelimLine reports whether the physical line starting at
// start is exactly frontMatterDelim once its line terminator ("\n", or
// "\r\n") is removed.
func isFrontMatterDelimLine(source []byte, start int) bool {
	end := lineEndInclusive(source, start)
	content := bytes.TrimSuffix(source[start:end], []byte("\n"))
	content = bytes.TrimSuffix(content, []byte("\r"))
	return string(content) == frontMatterDelim
}
