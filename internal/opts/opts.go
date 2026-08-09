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

// Span is a half-open byte range [Start, End). The public
// mdreflow.Span is a distinct, concrete struct with the same shape —
// not an alias — so pkg.go.dev shows its fields (go-quality review S3);
// format.go adapts between the two at the Segmenter boundary.
type Span struct {
	Start, End int
}
