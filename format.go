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
	"github.com/jbeda/mdreflow/internal/render"
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

// renderBackstop gates Format's render-preservation fallback. Always
// true in production; the test harness turns it off (via export_test.go)
// where it needs to observe the raw pipeline output, and a dedicated
// corpus test asserts the backstop never trips on legitimate documents.
// Like the convergence backstop it is for users and internally a bug
// detector: any input that needs it is a planner bug to root-cause
// (docs/design.md, "Render preservation is also structural").
var renderBackstop = true

// widthFloor gates the MinMaxWidth validation. Always true in production;
// the test harness turns it off (via export_test.go) so the fuzzer and
// the narrow-width fixtures keep driving the unrestricted core — a
// tiny-width finding is still a bug signal to root-cause (some generalize
// to legal widths with long tokens), it just cannot be a user-facing bug
// on its own (docs/design.md, "The width floor").
var widthFloor = true

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

	var seg reflow.Segmenter
	if opts.Segmenter != nil {
		seg = segmenterAdapter{opts.Segmenter}
	} else {
		seg = segment.New(opts.Abbreviations)
	}
	// Mode and HardBreakStyle are aliases of the shared internal
	// definitions (package opts), so no conversion happens here — the
	// former hand-mirrored enums and their unchecked casts are gone.
	rOpts := reflow.Options{
		Mode:                        opts.Mode,
		MaxWidth:                    opts.MaxWidth,
		HardBreaks:                  opts.HardBreaks,
		StripSentenceTerminalBreaks: opts.StripSentenceTerminalBreaks,
		MkDocs:                      opts.Dialect == DialectMkDocs,
	}

	out := formatOnce(src, seg, rOpts)
	if !convergenceBackstop {
		return out, nil
	}
	cur := out
	for i := 1; i < maxFormatPasses; i++ {
		next := formatOnce(cur, seg, rOpts)
		if bytes.Equal(next, cur) {
			return renderChecked(src, cur, opts), nil
		}
		cur = next
	}
	return src, nil
}

// renderChecked is the render backstop (docs/design.md, "Render
// preservation is also structural"): it returns out only if out renders
// to the same normalized HTML as src, and src unchanged otherwise — "we
// could not safely flow this" is a no-op, never corruption. The
// comparison runs through internal/render, the production home of the
// fuzz harness's render oracle, so "same" means "modulo the two
// documented cosmetic normalizations" (soft-break whitespace, <br>
// spelling) and nothing else.
//
// StripSentenceTerminalBreaks is the one option whose entire purpose is
// a render change (removing an accidental hard break removes a <br>), so
// the check is skipped when it is set — it is design.md's documented,
// flag-reversible exception, and the flag is opt-in.
//
// A render error is treated as "cannot verify": src is returned
// unchanged. In practice goldmark's renderer only fails on writer
// errors, which a bytes.Buffer never produces.
func renderChecked(src, out []byte, opts Options) []byte {
	if !renderBackstop || opts.StripSentenceTerminalBreaks || bytes.Equal(src, out) {
		return out
	}
	before, err := render.Normalized(src)
	if err != nil {
		return src
	}
	after, err := render.Normalized(out)
	if err != nil {
		return src
	}
	if before != after {
		return src
	}
	return out
}

// segmenterAdapter bridges a caller-supplied Segmenter (whose Breaks
// returns the public, concrete []Span) to the internal pipeline's
// interface. One small copy per paragraph cluster, in exchange for a
// public Span type whose fields render in documentation.
type segmenterAdapter struct{ s Segmenter }

func (a segmenterAdapter) Breaks(text string) []segment.Span {
	spans := a.s.Breaks(text)
	out := make([]segment.Span, len(spans))
	for i, sp := range spans {
		out[i] = segment.Span{Start: sp.Start, End: sp.End}
	}
	return out
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
	if widthFloor && opts.MaxWidth != 0 && opts.MaxWidth < MinMaxWidth {
		return fmt.Errorf("mdreflow: MaxWidth must be 0 (unbounded) or at least %d, got %d", MinMaxWidth, opts.MaxWidth)
	}
	switch opts.Dialect {
	case DialectGFM, DialectMkDocs:
	default:
		return fmt.Errorf("mdreflow: unknown Dialect value %d", opts.Dialect)
	}
	return nil
}
