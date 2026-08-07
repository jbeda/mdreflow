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
)

// Format reflows src according to opts and returns the result. Everything
// outside reflowed paragraph prose — code blocks, front matter, headings,
// lists, blockquotes, tables, and so on — is returned byte-for-byte (see
// docs/design.md's "Guarantees" section).
//
// Format returns an error, without partial output, if opts selects
// behavior M1 does not implement yet: see the doc comments on Mode,
// Options.MaxWidth, Options.Typography, and
// Options.StripSentenceTerminalBreaks.
func Format(src []byte, opts Options) ([]byte, error) {
	if err := checkImplemented(opts); err != nil {
		return nil, err
	}

	seg := opts.Segmenter
	if seg == nil {
		seg = segment.New(opts.Abbreviations)
	}

	doc := gm.New().Parser().Parse(text.NewReader(src))
	return reflow.Format(src, doc, seg), nil
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

// checkImplemented rejects any option combination M1's pipeline doesn't
// implement yet, loudly and without partial output — consistent with
// mdreflow's "loud, machine-legible behavior" design principle for
// unattended/agent use.
func checkImplemented(opts Options) error {
	switch opts.Mode {
	case ModeSentence:
	case ModePara:
		return errors.New("mdreflow: mode para not implemented (M3)")
	case ModeWrap:
		return errors.New("mdreflow: mode wrap not implemented (M3)")
	default:
		return fmt.Errorf("mdreflow: unknown Mode value %d", opts.Mode)
	}
	if opts.MaxWidth != 0 {
		return errors.New("mdreflow: MaxWidth not implemented (M3)")
	}
	if opts.Typography != 0 {
		return errors.New("mdreflow: Typography not implemented (M5)")
	}
	if opts.StripSentenceTerminalBreaks {
		return errors.New("mdreflow: StripSentenceTerminalBreaks not implemented (M2)")
	}
	return nil
}
