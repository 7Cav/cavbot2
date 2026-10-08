package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// Each line in testdata/src/replies that carries a want comment is a
// HandleError call the analyzer reports, and it reports no other line.
func TestErrorReply(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer, "replies")
}
