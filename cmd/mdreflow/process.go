package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// printExplain reports every paragraph Format left unformatted in
// content, one record per paragraph on stderr (keeping stdout clean for
// formatted output and diffs): a "path:start-end: skipped:" line naming
// what fired plus its stable reason code in brackets, then an indented
// remediation line. Diagnostics only — exit codes and output bytes are
// unaffected. Explain errors are impossible here by construction (Format
// already accepted the same content and options), so they are reported
// and otherwise ignored rather than failing a run that already
// succeeded.
func printExplain(path string, content []byte, opts mdreflow.Options, stderr io.Writer) {
	frozen, err := mdreflow.Explain(content, opts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: explain failed: %v\n", path, err)
		return
	}
	for _, fp := range frozen {
		loc := fmt.Sprintf("%d", fp.StartLine)
		if fp.EndLine != fp.StartLine {
			loc = fmt.Sprintf("%d-%d", fp.StartLine, fp.EndLine)
		}
		fmt.Fprintf(stderr, "%s:%s: skipped: %s [%s]\n  %s\n", path, loc, fp.Detail, fp.Reason, fp.Remediation)
	}
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

// nonRegularFileReason returns a one-line refusal reason if path is not
// a regular file — a symlink, FIFO, device node, socket, or directory —
// or "" if it is safe to read and write. It uses Lstat, not Stat, so a
// symlink is refused as itself rather than followed and classified by
// its target: reading through a symlink can pull content from outside
// the walked tree (security review S3), and a FIFO or device node can
// hang or exhaust memory on read (S4). A directory walk never hands
// processFile a symlink (walkDir skips them) or a directory, so this
// mainly guards paths named explicitly on the command line; Lstat errors
// (e.g. the path vanished) are left for the subsequent os.ReadFile to
// report.
func nonRegularFileReason(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return ""
	}
	mode := info.Mode()
	if mode.IsRegular() {
		return ""
	}
	switch {
	case mode&os.ModeSymlink != 0:
		return "refused: not a regular file (symlink)"
	case mode&os.ModeDir != 0:
		return "refused: not a regular file (directory)"
	case mode&os.ModeNamedPipe != 0:
		return "refused: not a regular file (named pipe)"
	case mode&os.ModeSocket != 0:
		return "refused: not a regular file (socket)"
	case mode&os.ModeDevice != 0:
		return "refused: not a regular file (device)"
	default:
		return "refused: not a regular file"
	}
}

// processFile reads, formats, and (depending on flags) checks/diffs/
// writes/prints path. If explicit is true, path was named directly on
// the command line (as opposed to discovered by a directory walk), so
// an exclude match is refused loudly (docs/design.md's "Excludes apply
// even to explicitly named files") rather than silently skipped.
func processFile(path string, opts mdreflow.Options, f *flags, ex *excluder, explicit bool, stdout, stderr io.Writer) (processResult, error) {
	var res processResult

	if explicit {
		excluded, source, err := ex.check(path, false, filepath.Dir(path))
		if err != nil {
			return res, err
		}
		if excluded && !f.force {
			fmt.Fprintf(stderr, "%s: skipped (excluded by %s)\n", path, source)
			res.refused = true
			return res, nil
		}
	}

	if !f.force {
		if reason := nonRegularFileReason(path); reason != "" {
			fmt.Fprintf(stderr, "%s: %s\n", path, reason)
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

	if f.explain {
		printExplain(path, content, opts, stderr)
	}

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
		if err := writeFileAtomic(path, out); err != nil {
			return res, err
		}
	}
	return res, nil
}

// writeFileAtomic replaces path's content with data without ever leaving
// a truncated or partially-written file in its place (security review
// S6): it writes to a sibling temp file in the same directory, chmods it
// to path's existing mode, fsyncs, closes, then renames it over path. A
// crash, SIGKILL, or ENOSPC mid-write now leaves either the untouched
// original or the complete new content, never a mix of both. The rename
// also replaces path outright rather than following it, which is the
// write-side half of the symlink protection in nonRegularFileReason
// (S3): even if a symlink slipped through — e.g. --force on an
// explicitly-named symlink skips the refusal, so the read still follows
// it — the target's inode is left alone and the symlink itself is
// replaced with an ordinary file holding the result.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else if !errors.Is(err, os.ErrNotExist) {
		// A real stat failure (permissions, I/O error, ...) is worth
		// failing loudly over — silently falling back to 0644 would
		// quietly widen or narrow the file's permissions.
		return fmt.Errorf("%s: stat before write: %w", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mdreflow-*")
	if err != nil {
		return fmt.Errorf("%s: create temp file: %w", path, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup: once Rename succeeds tmpPath no longer
	// exists, so this is a harmless no-op error on the success path.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%s: write temp file: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%s: chmod temp file: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%s: sync temp file: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%s: close temp file: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("%s: rename into place: %w", path, err)
	}
	return nil
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

	if f.explain {
		printExplain("<stdin>", content, opts, stderr)
	}

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
