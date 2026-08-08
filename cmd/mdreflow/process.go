package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/jbeda/mdreflow"
)

// processResult reports what happened to one target so the caller can
// fold it into the run's aggregate exit code (see the precedence rule
// documented in printUsage: 2 > 3 > 1 > 0).
type processResult struct {
	refused     bool
	reformatted bool
}

// unifiedDiff renders a unified diff of before -> after, labeled path,
// for --diff output.
func unifiedDiff(path string, before, after []byte) (string, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
		FromFile: path,
		ToFile:   path,
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
}

// processFile reads, formats, and (depending on flags) checks/diffs/
// writes/prints path. If explicit is true, path was named directly on
// the command line (as opposed to discovered by a directory walk), so
// an exclude match is refused loudly (docs/design.md's "Excludes apply
// even to explicitly named files") rather than silently skipped.
func processFile(path string, opts mdreflow.Options, f *flags, ex *excluder, explicit bool, stdout, stderr io.Writer) (processResult, error) {
	var res processResult

	if explicit {
		excluded, source, err := ex.check(path, false)
		if err != nil {
			return res, err
		}
		if excluded && !f.force {
			fmt.Fprintf(stderr, "%s: skipped (excluded by %s)\n", path, source)
			res.refused = true
			return res, nil
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}

	if !f.force {
		if reason := refusalReason(path, content, hasMarkdownExt(path)); reason != "" {
			fmt.Fprintf(stderr, "%s: %s\n", path, reason)
			res.refused = true
			return res, nil
		}
	}

	out, err := mdreflow.Format(content, opts)
	if err != nil {
		// The library rejects invalid UTF-8 anywhere in the file; the
		// sniff above only sees the first 8KB, and --force skips it
		// entirely. Same refusal treatment (exit 3), not a hard error.
		if errors.Is(err, mdreflow.ErrInvalidUTF8) {
			fmt.Fprintf(stderr, "%s: refused: invalid UTF-8\n", path)
			res.refused = true
			return res, nil
		}
		return res, fmt.Errorf("%s: %w", path, err)
	}
	changed := !bytes.Equal(content, out)

	if f.check || f.diff {
		if changed {
			res.reformatted = true
			if f.diff {
				d, err := unifiedDiff(path, content, out)
				if err != nil {
					return res, err
				}
				fmt.Fprint(stdout, d)
			} else {
				fmt.Fprintf(stdout, "would reformat %s\n", path)
			}
		}
		return res, nil
	}

	if f.stdout {
		_, err := stdout.Write(out)
		return res, err
	}

	if changed {
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode()
		}
		if err := os.WriteFile(path, out, mode); err != nil {
			return res, err
		}
	}
	return res, nil
}

// processStdin is processFile's counterpart for the stdin/stdout pipe
// mode: no exclude checks (there is no path to exclude), and the result
// (formatted content, a report, or a diff) always goes to stdout.
func processStdin(opts mdreflow.Options, f *flags, stdin io.Reader, stdout, stderr io.Writer) (processResult, error) {
	var res processResult

	content, err := io.ReadAll(stdin)
	if err != nil {
		return res, err
	}

	if !f.force {
		if reason := refusalReason("<stdin>", content, false); reason != "" {
			fmt.Fprintf(stderr, "<stdin>: %s\n", reason)
			res.refused = true
			return res, nil
		}
	}

	out, err := mdreflow.Format(content, opts)
	if err != nil {
		if errors.Is(err, mdreflow.ErrInvalidUTF8) {
			fmt.Fprintf(stderr, "<stdin>: refused: invalid UTF-8\n")
			res.refused = true
			return res, nil
		}
		return res, fmt.Errorf("<stdin>: %w", err)
	}
	changed := !bytes.Equal(content, out)

	if f.check || f.diff {
		if changed {
			res.reformatted = true
			if f.diff {
				d, err := unifiedDiff("-", content, out)
				if err != nil {
					return res, err
				}
				fmt.Fprint(stdout, d)
			} else {
				fmt.Fprintln(stdout, "would reformat -")
			}
		}
		return res, nil
	}

	_, err = stdout.Write(out)
	return res, err
}
