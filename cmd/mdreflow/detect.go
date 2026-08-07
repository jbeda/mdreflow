package main

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// markdownExts are the extensions a directory walk picks up, and the
// extensions that pass an explicit-file extension check without further
// question (docs/design.md's CLI and Non-Markdown input detection
// sections).
var markdownExts = map[string]bool{
	".md":       true,
	".mdx":      true,
	".markdown": true,
}

// nonMarkdownExts is a deny-list of common non-Markdown extensions
// refused when explicitly named (docs/design.md: "known-other extensions
// ... are refused"). It is deliberately not exhaustive: an unrecognized
// extension (or none) is allowed through to the binary/UTF-8 sniff
// rather than refused, since design.md defers content-based sniffing for
// unknown extensions until mislabeled files are observed in practice.
var nonMarkdownExts = map[string]bool{
	".yaml": true, ".yml": true, ".json": true, ".toml": true, ".xml": true,
	".go": true, ".py": true, ".js": true, ".mjs": true, ".cjs": true,
	".ts": true, ".jsx": true, ".tsx": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	".csv": true, ".tsv": true, ".ini": true, ".cfg": true, ".conf": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".rb": true, ".java": true, ".kt": true, ".c": true, ".h": true,
	".cpp": true, ".hpp": true, ".cc": true, ".rs": true, ".swift": true,
	".lock": true, ".sum": true, ".mod": true, ".sql": true, ".proto": true,
	".pdf": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".ico": true, ".zip": true, ".tar": true, ".gz": true,
	".bz2": true, ".7z": true, ".exe": true, ".bin": true, ".woff": true,
	".woff2": true, ".ttf": true, ".otf": true, ".mp3": true, ".mp4": true,
}

const sniffBytes = 8192

// refusalReason returns a one-line, machine-legible explanation for why
// path should be refused as non-Markdown input, or "" if it should
// proceed. It never reads more than sniffBytes of content.
//
// hasMarkdownExt reports whether path's extension is a recognized
// Markdown extension: callers that already know this (e.g. because they
// found the file by walking a directory, which only enqueues Markdown
// extensions) can pass true and skip the extension check, but the binary
// sniff still applies.
func refusalReason(path string, content []byte, hasMarkdownExt bool) string {
	if !hasMarkdownExt {
		ext := strings.ToLower(filepath.Ext(path))
		if nonMarkdownExts[ext] {
			return "refused: not a Markdown file (extension " + ext + ")"
		}
	}

	sniff := content
	if len(sniff) > sniffBytes {
		sniff = sniff[:sniffBytes]
	}
	for _, b := range sniff {
		if b == 0 {
			return "refused: binary content (NUL byte in first 8KB)"
		}
	}
	if !utf8.Valid(sniff) {
		return "refused: invalid UTF-8 in first 8KB"
	}

	return ""
}

func hasMarkdownExt(path string) bool {
	return markdownExts[strings.ToLower(filepath.Ext(path))]
}
