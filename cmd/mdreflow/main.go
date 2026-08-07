// Command mdreflow reflows Markdown prose read from stdin and writes the
// result to stdout.
//
// M1 is deliberately minimal — a pipe/editor-filter mode only. Path
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
	mode := fs.String("mode", "sentence", "reflow mode (only \"sentence\" is implemented in M1)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: mdreflow < in.md > out.md")
		fmt.Fprintln(stderr, "Reflows Markdown prose read from stdin and writes it to stdout.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := mdreflow.Options{}
	switch *mode {
	case "sentence":
		opts.Mode = mdreflow.ModeSentence
	default:
		fmt.Fprintf(stderr, "mdreflow: unsupported --mode %q (only \"sentence\" is implemented in M1)\n", *mode)
		return 2
	}
	if err := mdreflow.FormatReader(stdout, stdin, opts); err != nil {
		fmt.Fprintf(stderr, "mdreflow: %v\n", err)
		return 1
	}
	return 0
}
