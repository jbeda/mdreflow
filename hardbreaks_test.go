package mdreflow_test

import (
	"github.com/jbeda/mdreflow/internal/render"

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

				before := render.Normalize(renderHTML(t, []byte(in.src)))
				after := render.Normalize(renderHTML(t, got))
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

// TestHardBreakSpacesCheckboxFallsBackToBackslash regression-tests #40: a
// hard break whose cluster prose reduces to exactly a checkbox shape
// ("[ ]"/"[x]"/"[X]") on a task-list item's first paragraph line, under
// HardBreakSpaces, must not emit the configured "  " marker — the
// task-list extension's own regexp (`^\[([\sxX])\]\s*`) swallows the
// checkbox, the two marker spaces, and the line ending together, losing
// the hard break entirely on reparse. mdreflow falls back to the
// backslash spelling at this one position instead.
func TestHardBreakSpacesCheckboxFallsBackToBackslash(t *testing.T) {
	src := "* [X] <br>\n0\n"
	got, err := mdreflow.Format([]byte(src), mdreflow.Options{HardBreaks: mdreflow.HardBreakSpaces})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	want := "* [X]\\\n  0\n"
	if string(got) != want {
		t.Errorf("Format(checkbox hard break, spaces style) = %q, want %q", got, want)
	}

	before := render.Normalize(renderHTML(t, []byte(src)))
	after := render.Normalize(renderHTML(t, got))
	if before != after {
		t.Errorf("checkbox hard-break fallback changed rendered HTML.\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	twice, err := mdreflow.Format(got, mdreflow.Options{HardBreaks: mdreflow.HardBreakSpaces})
	if err != nil {
		t.Fatalf("Format(Format(x)): %v", err)
	}
	if string(twice) != string(got) {
		t.Errorf("Format not idempotent for checkbox hard break: got %q, then %q", got, twice)
	}
}

// TestHardBreakBackslashRunFallsBackToBr regression-tests #47: a backslash
// hard-break marker glued onto prose that itself ends in one or more
// backslashes produces a trailing run goldmark's inline parser does not
// read back as a hard break (only a trailing run of exactly one backslash
// is special-cased — see attachMarker's doc comment). mdreflow falls back
// to the "<br>" spelling at this one position instead, regardless of the
// configured style.
func TestHardBreakBackslashRunFallsBackToBr(t *testing.T) {
	src := "y\\\\\n\\\n0\n"
	got, err := mdreflow.Format([]byte(src), mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 54, HardBreaks: mdreflow.HardBreakBackslash})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	want := "y\\\\<br>\n0\n"
	if string(got) != want {
		t.Errorf("Format(backslash-run hard break, backslash style) = %q, want %q", got, want)
	}

	before := render.Normalize(renderHTML(t, []byte(src)))
	after := render.Normalize(renderHTML(t, got))
	if before != after {
		t.Errorf("backslash-run hard-break fallback changed rendered HTML.\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	twice, err := mdreflow.Format(got, mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 54, HardBreaks: mdreflow.HardBreakBackslash})
	if err != nil {
		t.Fatalf("Format(Format(x)): %v", err)
	}
	if string(twice) != string(got) {
		t.Errorf("Format not idempotent for backslash-run hard break: got %q, then %q", got, twice)
	}
}
