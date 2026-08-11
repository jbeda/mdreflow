package mdreflow_test

import (
	"testing"

	"github.com/jbeda/mdreflow"
	"github.com/jbeda/mdreflow/internal/render"
)

// TestHardBreakSpellingPolicy checks mdreflow's hard-break spelling policy
// (docs/design.md, "Hard line breaks"): each source spelling is preserved
// as-is, except that two trailing spaces are promoted to a backslash — the
// one respelling mdreflow performs, since two spaces are invisible and
// routinely stripped by editors in transit. A literal "<br>" is never
// introduced for a source that did not already use it. Every case must
// also render identically before and after, and be idempotent.
func TestHardBreakSpellingPolicy(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"spaces promoted to backslash", "First line ends here.  \nSecond line follows.\n", "First line ends here.\\\nSecond line follows.\n"},
		{"backslash preserved", "First line ends here.\\\nSecond line follows.\n", "First line ends here.\\\nSecond line follows.\n"},
		{"br preserved", "First line ends here.<br>\nSecond line follows.\n", "First line ends here.<br>\nSecond line follows.\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mdreflow.Format([]byte(tc.src), mdreflow.Options{})
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("Format(%q) = %q, want %q", tc.src, got, tc.want)
			}

			before := render.Normalize(renderHTML(t, []byte(tc.src)))
			after := render.Normalize(renderHTML(t, got))
			if before != after {
				t.Errorf("hard-break spelling changed rendered HTML.\n--- before ---\n%s\n--- after ---\n%s", before, after)
			}

			twice, err := mdreflow.Format(got, mdreflow.Options{})
			if err != nil {
				t.Fatalf("Format(Format(x)): %v", err)
			}
			if string(twice) != string(got) {
				t.Errorf("Format not idempotent: got %q, then %q", got, twice)
			}
		})
	}
}

// TestHardBreakPromotionFallback checks the promotion's own fallback: a
// two-space hard break normally promotes to a backslash, but not where a
// backslash cannot land — directly after an even, non-zero run of trailing
// backslashes, since appending one more would leave three or more, which
// goldmark reads as literal text, not a hard break. There mdreflow falls
// back to two trailing spaces instead, never to "<br>" (docs/design.md,
// "Hard line breaks").
func TestHardBreakPromotionFallback(t *testing.T) {
	src := "a\\\\  \nb\n"
	want := "a\\\\  \nb\n"

	got, err := mdreflow.Format([]byte(src), mdreflow.Options{})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if string(got) != want {
		t.Errorf("Format(%q) = %q, want %q", src, got, want)
	}

	before := render.Normalize(renderHTML(t, []byte(src)))
	after := render.Normalize(renderHTML(t, got))
	if before != after {
		t.Errorf("promotion fallback changed rendered HTML.\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	twice, err := mdreflow.Format(got, mdreflow.Options{})
	if err != nil {
		t.Fatalf("Format(Format(x)): %v", err)
	}
	if string(twice) != string(got) {
		t.Errorf("Format not idempotent: got %q, then %q", got, twice)
	}
}
