package mdreflow_test

import (
	"strings"
	"testing"

	"github.com/jbeda/mdreflow"
)

// TestExplain tables Explain's contract: each frozen paragraph is
// reported with 1-based inclusive line numbers and the documented stable
// reason code, in source order; a document with nothing frozen reports
// nothing.
func TestExplain(t *testing.T) {
	type want struct {
		startLine, endLine int
		reason             mdreflow.SkipReason
	}
	cases := []struct {
		name string
		src  string
		want []want
	}{
		{
			name: "clean document reports nothing",
			src:  "Just prose here.\nAnother sentence of it.\n",
			want: nil,
		},
		{
			name: "def-shaped paragraph and its neighbor",
			src:  "Fine paragraph.\n\n[label]: /url\nfrozen neighbor prose\n",
			want: []want{{4, 4, mdreflow.SkipLinkRefDefNeighbor}},
		},
		{
			name: "paragraph containing a def shape",
			src:  "Prose with `x[0]: boom` quoted\nacross two lines.\n",
			want: []want{{1, 2, mdreflow.SkipLinkRefDefShape}},
		},
		{
			name: "unbalanced bracket that could form a definition",
			src:  "an unclosed [bracket here\nand a [b]: completer\n",
			want: []want{{1, 2, mdreflow.SkipLinkRefDefShape}},
		},
		{
			// A pipe-delimited tail line would be swallowed into the table
			// as a row; the delimiter-row-plus-lazy-paragraph shape from
			// build's precededByTable comment is what actually leaves a
			// paragraph sibling directly under a table node.
			name: "paragraph directly under a table",
			src:  "0\n\t|-\n--\n0\n",
			want: []want{{3, 4, mdreflow.SkipTableAdjacency}},
		},
		{
			name: "deep nesting",
			src:  "> * > deep paragraph\n",
			want: []want{{1, 1, mdreflow.SkipDeepNesting}},
		},
		{
			name: "unterminated tag opener",
			src:  "<Component attr\nmore>\n",
			want: []want{{1, 2, mdreflow.SkipUnterminatedTag}},
		},
		{
			name: "control byte",
			src:  "text with a \x01 control byte\nsecond line\n",
			want: []want{{1, 2, mdreflow.SkipControlBytes}},
		},
		{
			name: "raw declaration opener",
			src:  "prose with <!DOCTYPE-ish opener\nsecond line\n",
			want: []want{{1, 2, mdreflow.SkipRawHTMLDeclOpener}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mdreflow.Explain([]byte(tc.src), mdreflow.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Explain returned %d records, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.StartLine != w.startLine || g.EndLine != w.endLine || g.Reason != w.reason {
					t.Errorf("record %d = %d-%d [%s], want %d-%d [%s]",
						i, g.StartLine, g.EndLine, g.Reason, w.startLine, w.endLine, w.reason)
				}
				if g.Detail == "" || g.Remediation == "" {
					t.Errorf("record %d missing wording: %+v", i, g)
				}
				if strings.Contains(g.Remediation, "escap") &&
					!strings.Contains(g.Remediation, "does not help") {
					t.Errorf("record %d remediation suggests escaping: %q", i, g.Remediation)
				}
			}
		})
	}

	t.Run("invalid UTF-8 refused like Format", func(t *testing.T) {
		if _, err := mdreflow.Explain([]byte{0xff, 0xfe}, mdreflow.Options{}); err == nil {
			t.Fatal("Explain(invalid UTF-8) = nil error, want ErrInvalidUTF8")
		}
	})
}
