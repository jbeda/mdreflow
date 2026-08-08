package mdreflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"
	"github.com/jbeda/mdreflow/internal/reflow"
	"github.com/jbeda/mdreflow/internal/segment"
	"github.com/jbeda/mdreflow/internal/typography"
)

// Format reflows src according to opts and returns the result. Everything
// outside reflowed paragraph prose — code blocks, front matter, headings,
// lists, blockquotes, tables, and so on — is returned byte-for-byte (see
// docs/design.md's "Guarantees" section).
//
// Format returns an error, without partial output, if opts is invalid:
// see the doc comments on Mode and Options.MaxWidth. The one documented
// exception to byte-for-byte pass-through of prose content is
// Options.Typography, which is off by default.
func Format(src []byte, opts Options) ([]byte, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}

	seg := opts.Segmenter
	if seg == nil {
		seg = segment.New(opts.Abbreviations)
	}

	doc := gm.New().Parser().Parse(text.NewReader(src))
	rOpts := reflow.Options{
		Mode:                        reflow.Mode(opts.Mode),
		MaxWidth:                    opts.MaxWidth,
		Typography:                  typography.Typography(opts.Typography),
		HardBreaks:                  reflow.HardBreakStyle(opts.HardBreaks),
		StripSentenceTerminalBreaks: opts.StripSentenceTerminalBreaks,
	}
	return reflow.Format(src, doc, seg, rOpts), nil
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
