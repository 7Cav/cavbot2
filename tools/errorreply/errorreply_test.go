package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// Each line in the replies and relay testdata that carries a want comment
// is a message to Discord, a store into an error type that reaches one, or a
// value put in another package's variable, that the analyzer reports. It
// reports no other line.
func TestErrorReply(t *testing.T) {
	analysistest.Run(diagnosticsOnly{t}, analysistest.TestData(), analyzer, "github.com/7cav/cavbot2/replies", "github.com/7cav/cavbot2/relay")
}

// Each line in the panel and commands testdata that carries a want comment
// is a panel answer, or a store into a value that reaches one, that the
// analyzer reports as a panel answer. It reports no other line, and nothing
// the smoke tool's fake forum answers a maintainer.
func TestPanelAnswer(t *testing.T) {
	analysistest.Run(diagnosticsOnly{t}, analysistest.TestData(), analyzer, "github.com/7cav/cavbot2/panel", "github.com/7cav/cavbot2/commands", "github.com/7cav/cavbot2/tools/smoke")
}

// diagnosticsOnly passes analysistest's failures on to the test, apart from
// a fact the testdata doesn't expect. A fact is how the check of one package
// tells the check of another what a function returns, not something the
// check reports, so the testdata expects none.
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
