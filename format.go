package mdreflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"
	"github.com/jbeda/mdreflow/internal/reflow"
	"github.com/jbeda/mdreflow/internal/segment"
)

// ErrInvalidUTF8 is returned (possibly wrapped) by Format, Check, and
// FormatReader when the input is not valid UTF-8. Markdown is text; bytes
// with no character interpretation have nothing meaningful to reflow.
// Callers branch with errors.Is.
var ErrInvalidUTF8 = errors.New("mdreflow: input is not valid UTF-8")

// maxFormatPasses bounds the convergence loop in Format. The first pass
// does the work; the second proves stability. Passes beyond that only run
// on inputs where the reflow planner mispredicted its own output's
// reparse — a bug class (see docs/design.md's Convergence section) that
// the fuzz harness hunts against the single-pass core directly.
const maxFormatPasses = 4

// convergenceBackstop gates Format's run-to-fixpoint loop. Always true in
// production; the test harness turns it off (via export_test.go) so
// idempotency oracles exercise the single-pass core — a planner that
// needs the backstop is a bug to find, not behavior to mask.
var convergenceBackstop = true

// Format reflows src according to opts and returns the result. Everything
// outside reflowed paragraph prose — code blocks, front matter, headings,
// lists, blockquotes, tables, and so on — is returned byte-for-byte (see
// docs/design.md's "Guarantees" section).
//
// Format returns an error, without partial output, if src is not valid
// UTF-8 (ErrInvalidUTF8) or opts is invalid: see the doc comments on Mode
// and Options.MaxWidth.
//
// Format's output is a fixpoint: formatting it again returns it
// unchanged. In the vanishingly rare case where reflow will not converge
// (docs/design.md, Convergence), Format returns src as-is rather than
// unstable output.
func Format(src []byte, opts Options) ([]byte, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	if !utf8.Valid(src) {
		return nil, ErrInvalidUTF8
	}

	seg := opts.Segmenter
	if seg == nil {
		seg = segment.New(opts.Abbreviations)
	}
	rOpts := reflow.Options{
		Mode:                        reflow.Mode(opts.Mode),
		MaxWidth:                    opts.MaxWidth,
		HardBreaks:                  reflow.HardBreakStyle(opts.HardBreaks),
		StripSentenceTerminalBreaks: opts.StripSentenceTerminalBreaks,
	}

	out := formatOnce(src, seg, rOpts)
	if !convergenceBackstop {
		return out, nil
	}
	cur := out
	for i := 1; i < maxFormatPasses; i++ {
		next := formatOnce(cur, seg, rOpts)
		if bytes.Equal(next, cur) {
			return cur, nil
		}
		cur = next
	}
	return src, nil
}

// formatOnce runs one parse+reflow pass — the single-pass core Format
// iterates to fixpoint.
func formatOnce(src []byte, seg reflow.Segmenter, rOpts reflow.Options) []byte {
	doc := gm.New().Parser().Parse(text.NewReader(src))
	return reflow.Format(src, doc, seg, rOpts)
}

// Check reports whether Format would change src, without writing
// anything.
func Check(src []byte, opts Options) (bool, error) {
	out, err := Format(src, opts)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(src, out), nil
}

// FormatReader reads all of src, formats it per opts, and writes the
// result to dst. Markdown is not streamable in general — e.g. a reference
// link definition on the last line can affect how the first paragraph
// renders — so FormatReader buffers the full input before writing
// anything.
func FormatReader(dst io.Writer, src io.Reader, opts Options) error {
	b, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	out, err := Format(b, opts)
	if err != nil {
		return err
	}
	_, err = dst.Write(out)
	return err
}

// validateOptions rejects any invalid option value or combination
// loudly and without partial output — consistent with mdreflow's "loud,
// machine-legible behavior" design principle for unattended/agent use.
func validateOptions(opts Options) error {
	switch opts.Mode {
	case ModeSentence, ModePara, ModeWrap:
	default:
		return fmt.Errorf("mdreflow: unknown Mode value %d", opts.Mode)
	}
	if opts.Mode == ModePara && opts.MaxWidth != 0 {
		// ModePara joins a paragraph to a single line unconditionally
		// (see its doc comment); MaxWidth has nothing to apply to.
		// Rejecting loudly, rather than silently ignoring it, matches
		// design.md's "loud, machine-legible behavior" principle.
		return errors.New("mdreflow: MaxWidth must be 0 with ModePara (para mode always joins to a single line)")
	}
	return nil
}
