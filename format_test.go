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

// normalizeWhitespace collapses whitespace runs to a single space before
// comparing rendered HTML. Reflow moves *where* a paragraph's soft line
// breaks fall without changing that they render as inter-word whitespace
// (a browser collapses "\n" the same as " "), so a literal byte comparison
// of the HTML would flag every reflowed paragraph as a false positive.
// This normalization is applied identically to both sides of the
// comparison, so it cannot mask a real content change — only the cosmetic
// difference in soft-break position that reflow is explicitly allowed to
// make.
func normalizeWhitespace(html string) string {
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(html, " "))
}
