package mdreflow_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"

	"github.com/jbeda/mdreflow"
)

// fixtures returns the base name (without extension) of every golden pair
// in testdata/: <name>.md plus <name>.golden.md.
func fixtures(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("testdata/*.golden.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no golden fixtures found under testdata/")
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		base := filepath.Base(m)
		names[i] = base[:len(base)-len(".golden.md")]
	}
	return names
}

// TestGoldenFixtures runs every testdata/*.md fixture through Format and
// diffs it against the matching *.golden.md, then checks the two
// guarantees that hold automatically for every fixture: idempotency and
// render preservation.
func TestGoldenFixtures(t *testing.T) {
	for _, name := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", name+".golden.md"))
			if err != nil {
				t.Fatal(err)
			}

			got, err := mdreflow.Format(src, mdreflow.Options{})
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if !bytes.Equal(got, golden) {
				t.Errorf("Format(%s.md) does not match golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, golden)
			}

			t.Run("idempotent", func(t *testing.T) {
				twice, err := mdreflow.Format(got, mdreflow.Options{})
				if err != nil {
					t.Fatalf("Format(Format(x)): %v", err)
				}
				if !bytes.Equal(twice, got) {
					t.Errorf("Format is not idempotent.\n--- Format(x) ---\n%s\n--- Format(Format(x)) ---\n%s", got, twice)
				}
			})

			t.Run("render-preserving", func(t *testing.T) {
				before := normalizeWhitespace(renderHTML(t, src))
				after := normalizeWhitespace(renderHTML(t, got))
				if before != after {
					t.Errorf("rendered HTML changed.\n--- before ---\n%s\n--- after ---\n%s", before, after)
				}
			})
		})
	}
}

// renderHTML renders src with the same goldmark configuration the reflow
// pipeline parses with (internal/gm), so the comparison in
// TestGoldenFixtures/render-preserving is apples to apples.
func renderHTML(t *testing.T, src []byte) string {
	t.Helper()
	md := gm.New()
	doc := md.Parser().Parse(text.NewReader(src))
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
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
// without differing in rendered meaning. HardBreakStyle normalization
// canonicalizes to "<br>" regardless of which spelling the source used
// (matching design.md's documented hard-break-style render-preservation
// exception), so this rule canonicalizes both sides of a comparison the
// same way — found by FuzzFormat on input "\x00<Br>\n00".
var anyBrTag = regexp.MustCompile(`(?i)<br\s*/?>`)

// normalizeWhitespace collapses whitespace runs to a single space before
// comparing rendered HTML. Reflow moves *where* a paragraph's soft line
// breaks fall without changing that they render as inter-word whitespace
// (a browser collapses "\n" the same as " "), so a literal byte comparison
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
// stated semantics, and required for HardBreakStyle normalization to have
// one canonical output regardless of how many spaces the source used), so
// it does not reproduce that single leftover space. Dropping it here
// (after normalizeWhitespace, so it also can't reappear from an unrelated
// spelled-out multi-space run elsewhere) treats it as the goldmark
// rendering artifact it is, not a real content difference — a browser
// collapses that one space against the block boundary identically either
// way.
//
// Both normalizations are applied identically to both sides of every
// comparison, so neither can mask a real content change — only the two
// cosmetic differences reflow (and hard-break normalization) are
// explicitly allowed to make.
func normalizeWhitespace(html string) string {
	s := whitespaceRun.ReplaceAllString(html, " ")
	s = anyBrTag.ReplaceAllString(s, "<br>")
	s = spaceBeforeBr.ReplaceAllString(s, "<br>")
	return strings.TrimSpace(s)
}
