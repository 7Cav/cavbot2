package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// Each line in testdata/src/replies that carries a want comment is a message
// to Discord, or a store into an error type that reaches one, that the
// analyzer reports. It reports no other line.
func TestErrorReply(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzer, "replies")
}
