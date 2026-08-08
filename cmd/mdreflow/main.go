// Command mdreflow reflows Markdown prose: sentence-per-line by default,
// paragraph-per-line or hard-wrap on request. See --help (printUsage in
// this package) for the full CLI surface; docs/design.md is canonical.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jbeda/mdreflow/internal/exclude"
)

// Exit codes — a contract other tools and agents branch on (docs/design.md's
// CLI section). When more than one applies in a single run, the most
// severe wins: 2 (usage/config error) > 3 (refused input) > 1
// (would-reformat) > 0 (clean). Usage and config errors abort the run
// immediately, before processing further targets; a refusal on one file
// does not stop later files in the same run from being processed, but
// forces the run's exit code to at least 3 once all targets have been
// handled.
const (
	exitOK            = 0
	exitWouldReformat = 1
	exitUsage         = 2
	exitRefused       = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mdreflow", flag.ContinueOnError)
	fs.SetOutput(stderr)

	ff := &flags{set: map[string]bool{}}
	fs.StringVar(&ff.mode, "mode", "sentence", "reflow mode: sentence, para, or wrap")
	fs.IntVar(&ff.maxWidth, "max-width", 0, "max line width in runes (0 = unbounded in sentence mode, 80 in wrap mode; invalid in para mode)")
	fs.BoolVar(&ff.check, "check", false, "report files that would be reformatted, write nothing, exit 1 if any would change")
	fs.BoolVar(&ff.diff, "diff", false, "like --check, but print a unified diff to stdout instead of a one-line report")
	fs.BoolVar(&ff.stdout, "stdout", false, "print the formatted result to stdout instead of writing in place (requires exactly one input file; ignored if --check or --diff is also given)")
	fs.BoolVar(&ff.force, "force", false, "format anyway despite an exclude match or a non-Markdown/binary refusal")
	fs.StringVar(&ff.configPath, "config", "", "use this config file instead of discovering .mdreflow.yaml upward from each target")
	fs.BoolVar(&ff.noGitignore, "no-gitignore", false, "do not consult .gitignore files when walking directories or checking excludes")
	fs.StringVar(&ff.hardBreaks, "hard-breaks", "br", "hard line break style: br, spaces, or backslash")
	fs.BoolVar(&ff.stripSentenceTerminalBreaks, "strip-sentence-terminal-breaks", false, "treat a trailing double-space immediately after sentence-terminal punctuation as an accidental hard break and remove it")
	fs.BoolVar(&ff.smartQuotes, "smart-quotes", false, "substitute curly quotes for straight quotes in prose (never in code spans, links, or skipped blocks)")
	fs.BoolVar(&ff.ellipses, "ellipses", false, `substitute "…" for "..." in prose (never in code spans, links, or skipped blocks)`)
	fs.BoolVar(&ff.version, "version", false, "print version information to stdout and exit 0")

	// Parse errors already name the offending flag; a pointer to --help
	// beats re-dumping the full usage text on stderr. Explicit --help gets
	// the full text on stdout and exits 0 — it is not an error, and agents
	// branch on that.
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: mdreflow [flags] [path ...]  (run 'mdreflow --help' for full documentation)")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stdout, fs)
			return exitOK
		}
		return exitUsage
	}
	fs.Visit(func(fl *flag.Flag) { ff.set[fl.Name] = true })

	// --version, like --help, is informational: one line on stdout,
	// exit 0, nothing read and nothing written. Handled before any path
	// or stdin handling so it works with no input available at all.
	if ff.version {
		printVersion(stdout)
		return exitOK
	}

	paths := fs.Args()
	if len(paths) == 0 || (len(paths) == 1 && paths[0] == "-") {
		return runStdin(ff, stdin, stdout, stderr)
	}
	return runPaths(ff, paths, stdout, stderr)
}

func runStdin(ff *flags, stdin io.Reader, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "mdreflow: %v\n", err)
		return exitUsage
	}

	cc := newConfigCache()
	cfg, cfgDir, err := cc.resolve(cwd, ff.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mdreflow: %v\n", err)
		return exitUsage
	}
	ro, err := mergeOptions(ff, cfg, cfgDir)
	if err != nil {
		fmt.Fprintf(stderr, "mdreflow: %v\n", err)
		return exitUsage
	}

	res, err := processStdin(ro.opts, ff, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mdreflow: %v\n", err)
		return exitUsage
	}
	switch {
	case res.refused:
		return exitRefused
	case res.reformatted:
		return exitWouldReformat
	default:
		return exitOK
	}
}

// target is one file to process, and whether it was named explicitly on
// the command line (vs. discovered by walking a directory argument) —
// see processFile's doc comment for why that distinction matters for
// excludes.
type target struct {
	path     string
	explicit bool
}

func runPaths(ff *flags, paths []string, stdout, stderr io.Writer) int {
	startDir := paths[0]
	if info, err := os.Stat(startDir); err != nil || !info.IsDir() {
		startDir = filepath.Dir(startDir)
	}
	repoRoot := ""
	if !ff.noGitignore {
		if root, err := exclude.FindRepoRoot(startDir); err == nil {
			repoRoot = root
		}
	}

	cc := newConfigCache()
	ex := newExcluder(ff.noGitignore, repoRoot, ff.configPath, cc)

	var targets []target
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(stderr, "mdreflow: %s: %v\n", p, err)
			return exitUsage
		}
		if info.IsDir() {
			files, err := walkDir(p, ex)
			if err != nil {
				fmt.Fprintf(stderr, "mdreflow: %v\n", err)
				return exitUsage
			}
			for _, f := range files {
				targets = append(targets, target{path: f, explicit: false})
			}
		} else {
			targets = append(targets, target{path: p, explicit: true})
		}
	}

	if ff.stdout && len(targets) != 1 {
		fmt.Fprintf(stderr, "mdreflow: --stdout requires exactly one input file, got %d\n", len(targets))
		return exitUsage
	}

	sawRefusal := false
	sawReformat := false
	for _, t := range targets {
		dir := filepath.Dir(t.path)
		cfg, cfgDir, err := cc.resolve(dir, ff.configPath)
		if err != nil {
			fmt.Fprintf(stderr, "mdreflow: %v\n", err)
			return exitUsage
		}
		ro, err := mergeOptions(ff, cfg, cfgDir)
		if err != nil {
			fmt.Fprintf(stderr, "mdreflow: %s: %v\n", t.path, err)
			return exitUsage
		}

		res, err := processFile(t.path, ro.opts, ff, ex, t.explicit, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "mdreflow: %v\n", err)
			return exitUsage
		}
		if res.refused {
			sawRefusal = true
		}
		if res.reformatted {
			sawReformat = true
		}
	}

	switch {
	case sawRefusal:
		return exitRefused
	case sawReformat:
		return exitWouldReformat
	default:
		return exitOK
	}
}
