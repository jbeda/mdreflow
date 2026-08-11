// Package opts holds the option types shared between the public mdreflow
// package and the internal pipeline packages. It exists to be a leaf: an
// internal package cannot import the root (which imports it), so a shared
// enum like Mode needs one definition here, aliased everywhere, rather
// than separate iota declarations in the root and internal/reflow "kept in
// lockstep" by comment alone and bridged with unchecked casts — a constant
// inserted mid-list would compile cleanly and silently remap every mode
// (go-quality review S4).
//
// The user-facing doc comments live on the root package's re-exports
// (mdreflow.Mode, mdreflow.ModeSentence, ...), which is what pkg.go.dev
// renders; comments here are deliberately minimal.
package opts

// Mode selects the reflow strategy. See mdreflow.Mode.
type Mode int

const (
	ModeSentence Mode = iota
	ModePara
	ModeWrap
)

// Dialect selects the renderer profile; see mdreflow.Dialect.
type Dialect int

const (
	DialectGFM Dialect = iota
	DialectMkDocs
)

// Span is a half-open byte range [Start, End). The public
// mdreflow.Span is a distinct, concrete struct with the same shape —
// not an alias — so pkg.go.dev shows its fields (go-quality review S3);
// format.go adapts between the two at the Segmenter boundary.
type Span struct {
	Start, End int
}

// String returns the CLI-facing name ("sentence", "gfm", ...), so errors,
// logs, and flag values speak one vocabulary. Out-of-range values print as
// their integer. Hand-written rather than generated: two enums with five
// values total is below the threshold where stringer/enumer earn their
// build step — revisit if Dialect grows past ~5 profiles or a third enum
// appears.
func (m Mode) String() string {
	switch m {
	case ModeSentence:
		return "sentence"
	case ModePara:
		return "para"
	case ModeWrap:
		return "wrap"
	}
	return itoa(int(m))
}

func (d Dialect) String() string {
	switch d {
	case DialectGFM:
		return "gfm"
	case DialectMkDocs:
		return "mkdocs"
	}
	return itoa(int(d))
}

// itoa avoids importing strconv (and fmt, whose %d on a Stringer-bearing
// type would recurse) for the out-of-range case.
func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
