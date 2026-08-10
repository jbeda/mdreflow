package segment

import "testing"

// TestNoBreakSpansCover checks that NoBreakSpans reports at least one span
// covering the marker construct in each case, so a sentence break can never
// land inside it. It does not assert exact byte ranges (several rules can
// overlap the same text); it asserts coverage.
//
// "reference link" and "shortcut reference" are deliberately absent here
// as of issue #30: the ask-goldmark parse registers no reference
// definitions (docs/design.md, "Reference links need no resolution"), so
// "[text][ref]" and "[ref]" parse as literal bracket prose, not a link —
// a break inside the label is harmless (label matching collapses internal
// whitespace) and is no longer protected. See TestNoBreakSpansUnresolvedReferenceNotProtected.
func TestNoBreakSpansCover(t *testing.T) {
	cases := []struct {
		name string
		text string
		// start, end mark the substring that must be fully covered by the
		// union of returned spans.
		marker string
	}{
		{"inline code", "See `a. b()` here.", "`a. b()`"},
		{"link", "Read [the docs](https://example.com/a.b) now.", "[the docs](https://example.com/a.b)"},
		{"image", "See ![a diagram](img/a.b.png) below.", "![a diagram](img/a.b.png)"},
		{"nested bracket link text", "See [a [nested] link](https://example.com) now.", "[a [nested] link](https://example.com)"},
		{"autolink", "Visit <https://example.com/a.b> now.", "<https://example.com/a.b>"},
		{"footnote reference", "This needs a citation.[^1] More text.", "[^1]"},
		{"inline math", "The formula $E = mc^2$ is famous.", "$E = mc^2$"},
		{"hugo shortcode angle", "Use {{< ref \"a.b.md\" >}} here.", "{{< ref \"a.b.md\" >}}"},
		{"hugo shortcode percent", "Use {{% ref \"a.b.md\" %}} here.", "{{% ref \"a.b.md\" %}}"},
		{"mdx curly expr", "The value is {a.b.c} exactly.", "{a.b.c}"},
		{"inline html tag", "Wrap it in <Badge>text</Badge> please.", "<Badge>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := indexOf(tc.text, tc.marker)
			if start < 0 {
				t.Fatalf("test bug: marker %q not found in %q", tc.marker, tc.text)
			}
			end := start + len(tc.marker)

			spans := NoBreakSpans(tc.text)
			if !fullyCovered(spans, start, end) {
				t.Errorf("NoBreakSpans(%q) = %v, does not fully cover marker %q at [%d,%d)", tc.text, spans, tc.marker, start, end)
			}
		})
	}
}

func indexOf(text, sub string) int {
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fullyCovered reports whether the union of spans covers every byte in
// [start, end).
func fullyCovered(spans []Span, start, end int) bool {
	for pos := start; pos < end; {
		advanced := false
		for _, s := range spans {
			if s.Start <= pos && pos < s.End {
				pos = s.End
				advanced = true
				break
			}
		}
		if !advanced {
			return false
		}
	}
	return true
}
