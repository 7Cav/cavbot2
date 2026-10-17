package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// Each line in the errordata and voice testdata that carries a want comment
// is one the check reports. Such a line is where data leaves an error for
// somewhere other than the log, Sentry or a comparison, or where code points
// the log at a place the check doesn't read. The check reports no other
// line. Each want is the empty pattern, which matches any message, so a
// fixture holds the check to which lines it reports and leaves the wording
// free.
func TestErrorData(t *testing.T) {
	analysistest.Run(diagnosticsOnly{t}, analysistest.TestData(), analyzer,
		"github.com/7cav/cavbot2/errordata", "github.com/7cav/cavbot2/voice")
}

// diagnosticsOnly passes analysistest's failures on to the test, apart from
// a fact the testdata doesn't expect. A fact is how the check of one package
// tells the check of another what a function does with its parameters, not
// something the check reports, so the testdata expects none.
type diagnosticsOnly struct {
	t *testing.T
}

func (d diagnosticsOnly) Errorf(format string, args ...any) {
	d.t.Helper()
	if format == "%v: unexpected %s: %v" && len(args) == 3 && args[1] == "fact" {
		return
	}
	d.t.Errorf(format, args...)
}
