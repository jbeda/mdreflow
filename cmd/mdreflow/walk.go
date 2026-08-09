package main

import (
	"io/fs"
	"path/filepath"
)

// walkDir recursively collects Markdown files (.md, .mdx, .markdown)
// under root, skipping anything excluded (gitignore, config exclude:
// patterns, or the built-in always-excludes). Excluded directories are
// pruned rather than descended into. This is plain "picked up by the
// walk" exclusion — not the same as an explicitly-named path being
// refused (see docs/design.md's "Excludes" section and refuseExplicit in
// process.go): a walk silently skips what it finds excluded, since
// nothing was explicitly asked for by name.
func walkDir(root string, ex *excluder) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			// The walk root itself is never exclude-checked (the user
			// asked for it by name); also sidesteps a panic in the
			// vendored gitignore repository matcher when path equals
			// its own base directory exactly.
			return nil
		}

		// A symlink (to a file or a directory) discovered by the walk is
		// skipped silently, like any other walk exclusion (security
		// review S3/S4): reading or writing through it can escape the
		// walked tree entirely, and a path named explicitly on the
		// command line still gets processFile's loud regular-file
		// refusal instead. filepath.WalkDir already doesn't descend into
		// symlinked directories, so this only changes symlinked files
		// from "silently formatted through" to "silently skipped".
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		isDir := d.IsDir()

		excluded, _, cerr := ex.check(path, isDir, root)
		if cerr != nil {
			return cerr
		}
		if excluded {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		if isDir {
			return nil
		}
		if hasMarkdownExt(path) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}
