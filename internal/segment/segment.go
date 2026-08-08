// Package segment implements mdreflow's built-in sentence segmenter and the
// no-break inline span detector the reflow pipeline uses to protect inline
// code and links from being split.
//
// The segmenter is a regex/punctuation splitter with an abbreviation
// exception list, the approach every shipping sentence-per-line tool uses
// (flowmark, mdslw, rumdl) — see docs/design.md's "Sentence segmentation"
// section for the rationale.
package segment

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span is a half-open byte range [Start, End) into the text a Segmenter or
// NoBreakSpans was called with.
type Span struct {
	Start, End int
}

// terminalRun matches a run of sentence-terminal punctuation (., !, ?)
// followed by any closing quote or bracket characters. Matching stops at
// the punctuation itself; deciding whether what follows is whitespace that
// plausibly starts a new sentence happens separately in Breaks. Because the
// preceding character is never inspected here, terminal punctuation is
// recognized the same way whether it directly follows prose or trails
// inline markup like a code span followed by a period, or bold text
// followed by a period — the flowmark #68 case.
//
// U+2026 HORIZONTAL ELLIPSIS ("…") is in the terminal set alongside the
// ASCII trio. A literal "..." already matched (three separate members of
// "[.!?]+" in one run) and was already treated as sentence-terminal, so a
// paragraph reading "Well then... Next sentence." has always split there.
// Options.Typography's Ellipses flag rewrites that run to a single "…"
// rune, and without this addition the *second* Format pass over its own
// output would no longer recognize the boundary the first pass had just
// created — an idempotency break. Recognizing both spellings identically
// and unconditionally (not gated on the typography flag) fixes it where
// it belongs: the segmenter has no business caring which of two
// renderings of the same punctuation the source happens to use. This is a
// strict superset of the previous behavior — a literal "…" in input text,
// which was simply never recognized as sentence-terminal before, now is,
// which is a correctness improvement rather than a behavior anything
// could reasonably have depended on the absence of.
var terminalRun = regexp.MustCompile("[.!?…]+[\"'”’)\\]]*")

// Segmenter is mdreflow's built-in Segmenter implementation.
type Segmenter struct {
	abbrev map[string]struct{}
}

// New returns a Segmenter whose abbreviation exception list is the built-in
// default list plus extra (case-insensitive; extra is added, not
// substituted).
func New(extra []string) *Segmenter {
	s := &Segmenter{abbrev: make(map[string]struct{}, len(defaultAbbreviations)+len(extra))}
	for _, a := range defaultAbbreviations {
		s.abbrev[strings.ToLower(a)] = struct{}{}
	}
	for _, a := range extra {
		s.abbrev[strings.ToLower(a)] = struct{}{}
	}
	return s
}

// Breaks returns the inter-sentence whitespace gaps in text, as [start,end)
// byte spans, in order.
//
// A candidate break is a run of terminal punctuation followed by
// whitespace, then a character that plausibly starts a new sentence
// (uppercase letter, digit, opening quote, or opening bracket/paren).
// Candidates are suppressed when the token ending at the punctuation is a
// known abbreviation, or is a single capital letter (an initial, e.g. "J."
// in "J. Beda"). Because a candidate requires whitespace immediately after
// the punctuation, decimal points ("3.14") and abbreviations packed against
// the next word ("e.g.foo") never match in the first place.
func (s *Segmenter) Breaks(text string) []Span {
	var out []Span
	for _, m := range terminalRun.FindAllStringIndex(text, -1) {
		m0, e0 := m[0], m[1]

		// Whitespace run immediately after the punctuation.
		e1 := e0
		for e1 < len(text) && (text[e1] == ' ' || text[e1] == '\t') {
			e1++
		}
		if e1 == e0 || e1 >= len(text) {
			continue // no following whitespace, or nothing after it
		}

		// Guard: the next character must plausibly start a new sentence.
		// A leading backslash escape is skipped first: "\[0]:" reads,
		// once rendered, as starting with "[" — the backslash itself
		// disappears — so the *escaped* character is what should decide
		// this, not the backslash byte. Without this, a sentence-start
		// candidate mdreflow's own escapeBlockInterrupt inserts (to
		// defend against a different hazard — see reflow.escapeBlockInterrupt)
		// stops being recognized as one on a second pass, which is an
		// idempotency break: found by FuzzFormat on "[0]!  [0]:".
		next := text[e1:]
		if len(next) >= 2 && next[0] == '\\' && isASCIIPunctByte(next[1]) {
			next = next[1:]
		}
		r, _ := utf8.DecodeRuneInString(next)
		if !startsSentence(r) {
			continue
		}

		// Token immediately preceding the punctuation run, e.g. "Mr." or
		// "J." — used for the abbreviation and single-initial exceptions.
		wordStart := m0
		for wordStart > 0 && text[wordStart-1] != ' ' && text[wordStart-1] != '\t' {
			wordStart--
		}
		token := text[wordStart:e0]

		if _, known := s.abbrev[strings.ToLower(token)]; known {
			continue
		}
		if isSingleInitial(token) {
			continue
		}

		out = append(out, Span{Start: e0, End: e1})
	}
	return out
}

// isASCIIPunctByte reports whether b is one of CommonMark's escapable
// ASCII punctuation characters (the set backslash-escaping recognizes).
func isASCIIPunctByte(b byte) bool {
	switch {
	case b >= '!' && b <= '/':
	case b >= ':' && b <= '@':
	case b >= '[' && b <= '`':
	case b >= '{' && b <= '~':
	default:
		return false
	}
	return true
}

// startsSentence reports whether r plausibly begins a new sentence:
// uppercase, a digit, an opening quote, or an opening bracket/paren.
func startsSentence(r rune) bool {
	if unicode.IsUpper(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '"', '\'', '“', '‘', '(', '[':
		return true
	}
	return false
}

// isSingleInitial reports whether token is exactly one uppercase letter
// followed by a single period, e.g. "J." — the single-capital-initial
// exception from docs/design.md, independent of the abbreviation list.
func isSingleInitial(token string) bool {
	if len(token) < 2 || token[len(token)-1] != '.' {
		return false
	}
	letters := token[:len(token)-1]
	r, size := utf8.DecodeRuneInString(letters)
	return size == len(letters) && unicode.IsUpper(r)
}
