package mdreflow

// SetConvergenceBackstop enables or disables Format's run-to-fixpoint
// loop for tests. The harness turns it off so idempotency oracles bite
// on the single-pass core: a planner that only converges via the backstop
// is a bug to surface, not behavior to accept. Not safe to toggle from
// parallel tests.
func SetConvergenceBackstop(enabled bool) {
	convergenceBackstop = enabled
}

// SetRenderBackstop enables or disables Format's render-preservation
// fallback for tests. The harness turns it off where it needs the raw
// pipeline output; TestRenderBackstopNeverTripsOnCorpus asserts the
// enabled path is a no-op on every legitimate fixture. Not safe to
// toggle from parallel tests.
func SetRenderBackstop(enabled bool) {
	renderBackstop = enabled
}

// SetWidthFloor enables or disables the MinMaxWidth validation for tests.
// The harness turns it off so the fuzzer and the narrow-width fixtures
// drive the unrestricted core (docs/design.md, "The width floor"). Not
// safe to toggle from parallel tests.
func SetWidthFloor(enabled bool) {
	widthFloor = enabled
}
