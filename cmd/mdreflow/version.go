package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// Build metadata, overridden at release time by goreleaser's ldflags
// (-X main.version=... -X main.commit=... -X main.date=...; see
// .goreleaser.yaml). A plain "go build ./cmd/mdreflow" leaves these at
// their defaults, which is the honest answer for an unreleased binary.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// goldmarkModulePath is the dependency whose version --version reports
// alongside mdreflow's own: goldmark is the parser, so its version is
// the single most useful thing for explaining a difference in behavior
// between two mdreflow builds at the same mdreflow version.
const goldmarkModulePath = "github.com/yuin/goldmark"

// printVersion writes one machine-legible line to w. One line, and
// grep/cut-friendly, on purpose: docs/design.md's CLI section calls for
// loud, machine-legible behavior for unattended and agent use, and a
// multi-line banner is worse for both.
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "mdreflow %s (commit %s, built %s) %s goldmark %s\n",
		version, commit, date, runtime.Version(), goldmarkVersion())
}

// goldmarkVersion reads goldmark's module version out of the binary's own
// build info rather than hardcoding it, so it cannot go stale relative to
// go.mod. It returns "unknown" when build info is unavailable (which
// happens for binaries built in unusual ways) or when goldmark is
// somehow not among the recorded dependencies.
func goldmarkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path != goldmarkModulePath {
			continue
		}
		if dep.Replace != nil {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return "unknown"
}
