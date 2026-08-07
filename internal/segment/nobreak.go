package segment

import "regexp"

// NoBreakSpans returns byte ranges within text that a sentence break must
// never land inside: inline code spans, links and images (including
// one-level-nested-bracket link/image text), autolinks, footnote
// references, inline math, inline Hugo shortcodes, inline MDX/JSX {expr}
// spans, and inline HTML/JSX tags. It is a deliberately simple, text-based
// approximation of CommonMark's (and these dialects') inline grammar — a
// span longer than any configured width limit simply overflows; break
// points just never land inside one.
//
// The reflow pipeline uses this to filter a Segmenter's candidate breaks,
// independent of which Segmenter produced them.
func NoBreakSpans(text string) []Span {
	var out []Span
	out = append(out, codeSpans(text)...)
	out = append(out, bracketedSpans(text)...)
	out = append(out, otherSpans(text)...)
	return out
}

// CodeSpans is codeSpans, exported for package reflow: it needs to know a
// paragraph's real (matched-open-and-close) code span ranges, across its
// raw source lines joined by "\n", to tell whether a given line boundary
// falls inside one — the naive "odd backtick count so far" parity a purely
// per-line scan can afford is not sufficient, since an opening run with no
// later matching close is not a span at all (see joinClusterLines and
// detectHardBreak in package reflow for what this fixes).
func CodeSpans(text string) []Span {
	return codeSpans(text)
}

// codeSpans finds CommonMark-style backtick code spans: a run of one or
// more backticks, then the shortest run of backticks of the same length
// later in the text. An opening run with no matching close is not a code
// span and is skipped, matching CommonMark.
func codeSpans(text string) []Span {
	var out []Span
	i := 0
	for i < len(text) {
		if text[i] != '`' {
			i++
			continue
		}
		if precededByOddBackslashes(text, i) {
			// A backslash-escaped backtick renders as a literal backtick
			// and cannot open a code span — confirmed against goldmark,
			// not assumed (`\`a\`` renders as literal "`a`", no <code>).
			// Only the opening backtick of a run is checked: once inside
			// a genuine span, closing-search does not re-check this
			// (backslash escapes do not work inside code spans at all,
			// per CommonMark), so this exclusion applies once, here, at
			// the point a new run is considered as a candidate opener —
			// not inside the closing-search loop below. This matters for
			// package reflow's own escapeBlockInterrupt, which
			// backslash-escapes every backtick of a fenced-code-opener
			// run to defeat it: without this exclusion, the now-escaped
			// backticks could still spuriously pair up with unrelated
			// backticks elsewhere in the text as this function's own
			// false-positive code span — found by FuzzFormat via a
			// resulting idempotency break on "```! 0`0".
			i++
			continue
		}
		start := i
		j := i
		for j < len(text) && text[j] == '`' {
			j++
		}
		runLen := j - start

		k := j
		closed := -1
		for k < len(text) {
			if text[k] != '`' {
				k++
				continue
			}
			m := k
			for m < len(text) && text[m] == '`' {
				m++
			}
			if m-k == runLen {
				closed = m
				break
			}
			k = m
		}
		if closed >= 0 {
			out = append(out, Span{Start: start, End: closed})
			i = closed
		} else {
			i = j // unmatched opening run; not a code span
		}
	}
	return out
}

// bracketedSpans finds links and images — "[text](dest)", "[text][ref]",
// and shortcut reference "[text]" — by manually matching brackets and
// parens rather than a fixed-depth regex, so one level of nested "[...]"
// inside the link/image text (e.g. a footnote marker embedded in link
// text) is still recognized as part of the same span. An optional leading
// "!" (image) is included in the span. Unmatched "[" is left unprotected,
// matching CommonMark (it is not a link).
func bracketedSpans(text string) []Span {
	var out []Span
	i := 0
	for i < len(text) {
		start := i
		if text[i] == '!' && i+1 < len(text) && text[i+1] == '[' {
			i++
		}
		if i >= len(text) || text[i] != '[' {
			i = start + 1
			continue
		}

		depth := 1
		j := i + 1
		for j < len(text) && depth > 0 {
			switch text[j] {
			case '\\':
				j++ // skip escaped char, whatever it is
			case '[':
				depth++
			case ']':
				depth--
			}
			j++
		}
		if depth != 0 {
			i = start + 1
			continue // unmatched "["; not a link/image
		}
		closeBracket := j // index just after the matching "]"
		end := closeBracket

		switch {
		case closeBracket < len(text) && text[closeBracket] == '(':
			pd := 1
			k := closeBracket + 1
			for k < len(text) && pd > 0 {
				switch text[k] {
				case '(':
					pd++
				case ')':
					pd--
				}
				k++
			}
			if pd == 0 {
				end = k
			}
		case closeBracket < len(text) && text[closeBracket] == '[':
			k := closeBracket + 1
			for k < len(text) && text[k] != ']' {
				k++
			}
			if k < len(text) {
				end = k + 1
			}
		}

		out = append(out, Span{Start: start, End: end})
		i = end
	}
	return out
}

// Autolinks, footnote references, inline math, inline Hugo shortcodes,
// inline MDX/JSX {expr} spans, and inline HTML/JSX tags. Each is a
// text-based approximation, not a full grammar; see NoBreakSpans's doc
// comment.
var (
	autolinkRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9+.-]*:[^\s<>]*>|<[^\s<>@]+@[^\s<>]+>`)

	// footnoteRefRE matches a footnote reference like "[^1]". bracketedSpans
	// already protects these as a degenerate shortcut-reference-style
	// bracket span; this rule is kept as an explicit, independently
	// correct fallback.
	footnoteRefRE = regexp.MustCompile(`\[\^[^\]\n]+\]`)

	// inlineMathRE matches single-dollar inline math, "$...$". It does not
	// attempt to distinguish from a "$$" display-math delimiter; display
	// math is block-level and handled by the skip-list, not this scanner.
	inlineMathRE = regexp.MustCompile(`\$[^$\n]+\$`)

	// hugoShortcodeRE matches an inline Hugo shortcode, "{{< ... >}}" or
	// "{{% ... %}}".
	hugoShortcodeRE = regexp.MustCompile(`\{\{[<%].*?[%>]\}\}`)

	// curlyExprRE matches an inline MDX/JSX "{expr}" span. It also matches
	// (harmlessly, redundantly) the interior of a Hugo shortcode span,
	// since both use curly braces; overlapping no-break spans are fine, a
	// break is blocked if it lands in any of them.
	curlyExprRE = regexp.MustCompile(`\{[^{}\n]+\}`)

	// inlineHTMLRE matches an inline HTML/JSX open, close, or self-closing
	// tag, e.g. "<Tabs>", "</Tabs>", "<br/>", "<TabItem value=\"go\">".
	inlineHTMLRE = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:_-]*(?:\s[^<>\n]*)?/?>`)
)

// precededByOddBackslashes reports whether text[pos] is immediately
// preceded by an odd number of backslash characters — i.e. whether it is
// itself backslash-escaped.
func precededByOddBackslashes(text string, pos int) bool {
	n := 0
	for n < pos && text[pos-1-n] == '\\' {
		n++
	}
	return n%2 == 1
}

func otherSpans(text string) []Span {
	var out []Span
	for _, re := range [...]*regexp.Regexp{
		autolinkRE,
		footnoteRefRE,
		inlineMathRE,
		hugoShortcodeRE,
		curlyExprRE,
		inlineHTMLRE,
	} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			out = append(out, Span{Start: m[0], End: m[1]})
		}
	}
	return out
}
