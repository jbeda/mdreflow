package mdreflow

// SetConvergenceBackstop enables or disables Format's run-to-fixpoint
// loop for tests. The harness turns it off so idempotency oracles bite
// on the single-pass core: a planner that only converges via the backstop
// is a bug to surface, not behavior to accept. Not safe to toggle from
// parallel tests.
func SetConvergenceBackstop(enabled bool) {
	convergenceBackstop = enabled
}
