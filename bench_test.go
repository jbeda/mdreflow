package mdreflow_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jbeda/mdreflow"
)

// benchParagraph builds a single-paragraph document of roughly n words,
// salted with the inline constructs whose no-break spans make width
// measurement non-trivial (code spans, links, multi-space runs). The
// generator is deterministic so allocations and timings are comparable
// across runs.
func benchParagraph(n int) []byte {
	rng := rand.New(rand.NewSource(1))
	words := strings.Fields("the quick reflow pipeline measures every candidate against its width budget and keeps prose readable without changing how a document renders")
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch r := rng.Float64(); {
		case r < 0.02:
			b.WriteString("`inline  code`")
		case r < 0.04:
			b.WriteString("[a  link](https://example.com/path)")
		default:
			b.WriteString(words[rng.Intn(len(words))])
		}
	}
	b.WriteByte('\n')
	return []byte(b.String())
}

// BenchmarkFormat exercises each mode at growing paragraph sizes. The
// width-constrained modes (wrap, sentence+MaxWidth) are the ones with a
// history of superlinear behavior; sentence and para are the controls.
func BenchmarkFormat(b *testing.B) {
	modes := []struct {
		name string
		opts mdreflow.Options
	}{
		{"sentence", mdreflow.Options{}},
		{"para", mdreflow.Options{Mode: mdreflow.ModePara}},
		{"wrap80", mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 80}},
		{"sentence-mw80", mdreflow.Options{MaxWidth: 80}},
	}
	for _, m := range modes {
		for _, words := range []int{100, 400, 1600} {
			src := benchParagraph(words)
			b.Run(fmt.Sprintf("%s/words=%d", m.name, words), func(b *testing.B) {
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := mdreflow.Format(src, m.opts); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkFormatRawPipeline is BenchmarkFormat's control with the render
// backstop disabled: the delta between the two is the backstop's cost
// (two extra parse+render+normalize passes per changed document), pinned
// here so a regression in it is visible.
func BenchmarkFormatRawPipeline(b *testing.B) {
	mdreflow.SetRenderBackstop(false)
	defer mdreflow.SetRenderBackstop(true)
	for _, words := range []int{400, 1600} {
		src := benchParagraph(words)
		b.Run(fmt.Sprintf("wrap80/words=%d", words), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			opts := mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 80}
			for i := 0; i < b.N; i++ {
				if _, err := mdreflow.Format(src, opts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
