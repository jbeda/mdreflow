// Package opts holds the option types shared between the public mdreflow
// package and the internal pipeline packages. It exists to be a leaf: an
// internal package cannot import the root (which imports it), so before
// this package the root and internal/reflow each declared their own Mode
// and HardBreakStyle with iota values "kept in lockstep" by comment alone
// and bridged with unchecked casts — a constant inserted mid-list would
// compile cleanly and silently remap every mode (go-quality review S4).
// One definition here, aliased everywhere, deletes the hazard.
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

// HardBreakStyle selects the normalized hard-break spelling. See
// mdreflow.HardBreakStyle.
type HardBreakStyle int

const (
	HardBreakBr HardBreakStyle = iota
	HardBreakSpaces
	HardBreakBackslash
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

// String returns the CLI-facing name ("sentence", "gfm", "br", ...), so
// errors, logs, and flag values speak one vocabulary. Out-of-range values
// print as their integer. Hand-written rather than generated: three enums
// with eight values total is below the threshold where stringer/enumer
// earn their build step — revisit if Dialect grows past ~5 profiles or a
// fourth enum appears.
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

func (h HardBreakStyle) String() string {
	switch h {
	case HardBreakBr:
		return "br"
	case HardBreakSpaces:
		return "spaces"
	case HardBreakBackslash:
		return "backslash"
	}
	return itoa(int(h))
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
