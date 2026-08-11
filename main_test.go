package mdreflow_test

import (
	"os"
	"testing"

	"github.com/jbeda/mdreflow"
)

// TestMain disables the MinMaxWidth floor for the whole harness package:
// the narrow-width fixture families (w1-, w5-, mw10-, ...), the fuzz
// corpus, and the regression pins all deliberately drive the unrestricted
// core at widths the product refuses (docs/design.md, "The width floor").
// Tests that assert the floor itself re-enable it locally.
func TestMain(m *testing.M) {
	mdreflow.SetWidthFloor(false)
	code := m.Run()
	reportRenderSkips()
	os.Exit(code)
}
