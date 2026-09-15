package panel

import (
	"testing"
	"time"
)

// SetClockForTest pins the package clock for one test and restores it on
// cleanup. Compiled into the test binary only.
func SetClockForTest(t *testing.T, now func() time.Time) {
	t.Helper()
	prev := panelNow
	panelNow = now
	t.Cleanup(func() { panelNow = prev })
}
