package commands

import (
	"context"
	"testing"
)

// Deleting a recording early (#391), as the runtime sees it. The panel
// deletes through the library; these cases record through /record with the
// fake voice connection and delete through the same library.

// A recording deleted right after its stop, while its mix is still being
// built, is no failure: the mix's end sends nothing to Sentry.
func TestRecordingDeletedWhileItsMixIsBuiltSendsNothingToSentry(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordOneSpeaker(t)
	if err := sc.lib.Delete(context.Background(), sc.onlyRecording(t)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	captures := countCaptures(t)

	sc.clock.advance(0)

	if *captures != 0 {
		t.Errorf("the mix's end sent %d events to Sentry, want none", *captures)
	}
}
