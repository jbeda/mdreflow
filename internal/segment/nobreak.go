package segment

import "regexp"

// NoBreakSpans returns byte ranges within text that a sentence break must
// never land inside: inline code spans, inline/reference links and images,
// and autolinks. It is a deliberately simple, text-based approximation of
// CommonMark's inline grammar — enough to keep sentence boundaries out of
// an inline code span like "a. b" and out of link destinations/titles,
// which is the M1 bar from docs/design.md ("at minimum inline code spans
// must be protected").
// Full no-break-span coverage (inline HTML, footnote references, images
// with nested brackets, ...) is deferred to M2.
//
// The reflow pipeline uses this to filter a Segmenter's candidate breaks,
// independent of which Segmenter produced them.
func NoBreakSpans(text string) []Span {
	var out []Span
	out = append(out, codeSpans(text)...)
	out = append(out, linkSpans(text)...)
	return out
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

// Inline/reference links and images, and scheme/email autolinks. Link and
// image text is assumed not to contain nested brackets, and destinations
// not to contain parens — simple, not the full CommonMark grammar, but
// sufficient to keep a sentence break out of a link's visible span.
var (
	inlineLinkRE = regexp.MustCompile(`!?\[[^\[\]]*\]\([^()]*\)`)
	refLinkRE    = regexp.MustCompile(`!?\[[^\[\]]*\]\[[^\[\]]*\]`)
	autolinkRE   = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9+.-]*:[^\s<>]*>|<[^\s<>@]+@[^\s<>]+>`)
)

func linkSpans(text string) []Span {
	var out []Span
	for _, re := range [...]*regexp.Regexp{inlineLinkRE, refLinkRE, autolinkRE} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			out = append(out, Span{Start: m[0], End: m[1]})
		}
	}
	return out
}
