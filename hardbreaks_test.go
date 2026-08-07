package mdreflow_test

import (
	"testing"

	"github.com/jbeda/mdreflow"
)

// TestHardBreakStyleMatrix checks every combination of input hard-break
// syntax (trailing double-space, trailing backslash, literal <br>) against
// every Options.HardBreaks style: the output always uses the configured
// style, regardless of which syntax the source used, and rendered HTML is
// unchanged by the substitution (see internal/gm's html.WithUnsafe comment
// for why a literal "<br>" in the source renders the same as a true
// hard-break node).
func TestHardBreakStyleMatrix(t *testing.T) {
	inputs := []struct {
		name string
		src  string
	}{
		{"spaces", "First line ends here.  \nSecond line follows.\n"},
		{"backslash", "First line ends here.\\\nSecond line follows.\n"},
		{"br", "First line ends here.<br>\nSecond line follows.\n"},
	}
	styles := []struct {
		name   string
		style  mdreflow.HardBreakStyle
		marker string
	}{
		{"br", mdreflow.HardBreakBr, "<br>"},
		{"spaces", mdreflow.HardBreakSpaces, "  "},
		{"backslash", mdreflow.HardBreakBackslash, "\\"},
	}

	for _, in := range inputs {
		for _, st := range styles {
			t.Run(in.name+"->"+st.name, func(t *testing.T) {
				got, err := mdreflow.Format([]byte(in.src), mdreflow.Options{HardBreaks: st.style})
				if err != nil {
					t.Fatalf("Format: %v", err)
				}
				want := "First line ends here." + st.marker + "\nSecond line follows.\n"
				if string(got) != want {
					t.Errorf("Format(%s hard break, style %s) = %q, want %q", in.name, st.name, got, want)
				}

				before := normalizeWhitespace(renderHTML(t, []byte(in.src)))
				after := normalizeWhitespace(renderHTML(t, got))
				if before != after {
					t.Errorf("hard-break style normalization changed rendered HTML.\n--- before ---\n%s\n--- after ---\n%s", before, after)
				}

				// Idempotency: reformatting the already-normalized output
				// with the same style must be a no-op.
				twice, err := mdreflow.Format(got, mdreflow.Options{HardBreaks: st.style})
				if err != nil {
					t.Fatalf("Format(Format(x)): %v", err)
				}
				if string(twice) != string(got) {
					t.Errorf("Format not idempotent for style %s: got %q, then %q", st.name, got, twice)
				}
			})
		}
	}
}
