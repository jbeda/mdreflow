// Command mdreflow reflows Markdown prose read from stdin and writes the
// result to stdout.
//
// M3 is still stdin/stdout only — a pipe/editor-filter mode. Path
// arguments, in-place writes, config discovery, excludes, --check/--diff,
// and the rest of the CLI surface described in docs/design.md land in M4.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jbeda/mdreflow"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mdreflow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "sentence", "reflow mode: sentence, para, or wrap")
	maxWidth := fs.Int("max-width", 0, "max line width in runes (0 = unbounded in sentence mode, 80 in wrap mode; invalid in para mode)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: mdreflow [flags] < in.md > out.md")
		fmt.Fprintln(stderr, "Reflows Markdown prose read from stdin and writes it to stdout.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := mdreflow.Options{MaxWidth: *maxWidth}
	switch *mode {
	case "sentence":
		opts.Mode = mdreflow.ModeSentence
	case "para":
		opts.Mode = mdreflow.ModePara
	case "wrap":
		opts.Mode = mdreflow.ModeWrap
	default:
		fmt.Fprintf(stderr, "mdreflow: unsupported --mode %q (want one of: sentence, para, wrap)\n", *mode)
		return 2
	}
	if opts.Mode == mdreflow.ModePara && opts.MaxWidth != 0 {
		// Caught here (rather than left to Format's own error, surfaced
		// only after reading all of stdin) so an invalid flag combination
		// is diagnosed as a usage error (exit 2), not a format error.
		fmt.Fprintln(stderr, "mdreflow: --max-width is not valid with --mode=para (para mode always joins to a single line)")
		return 2
	}
	if err := mdreflow.FormatReader(stdout, stdin, opts); err != nil {
		fmt.Fprintf(stderr, "mdreflow: %v\n", err)
		return 1
	}
	return 0
}
