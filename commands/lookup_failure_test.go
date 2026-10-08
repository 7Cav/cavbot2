package commands

import (
	"strings"
	"testing"
)

// lastReply returns the text a run last showed its member: the content of
// its last edit, or of its last response when it never edited.
func lastReply(t *testing.T, calls []recordedCall) string {
	t.Helper()
	reply, found := "", false
	for _, c := range calls {
		switch {
		case c.Method == "Respond" && c.Response != nil && c.Response.Data != nil:
			reply, found = c.Response.Data.Content, true
		case c.Method == "Edit" && c.Edit != nil && c.Edit.Content != nil:
			reply, found = *c.Edit.Content, true
		}
	}
	if !found {
		t.Fatalf("the run showed its member nothing; calls %+v", calls)
	}
	return reply
}

// assertFixedReply fails unless reply opens with ❌, carries the phrase
// that tells its case apart, and leaves out every error rec captured. A nil
// rec is a run that captured nothing.
func assertFixedReply(t *testing.T, reply, phrase string, rec *captureRecorder) {
	t.Helper()
	if !strings.HasPrefix(reply, "❌") {
		t.Errorf("reply %q doesn't open with ❌", reply)
	}
	if !strings.Contains(reply, phrase) {
		t.Errorf("reply %q doesn't carry %q", reply, phrase)
	}
	if rec == nil {
		return
	}
	for _, err := range rec.errs {
		if strings.Contains(reply, err.Error()) {
			t.Errorf("reply %q carries the error's text %q", reply, err.Error())
		}
	}
}

// assertLookupFailed fails unless a run whose 7Cav API lookup failed told
// its member so with lookupFailedReply and none of the error, and sent the
// error to Sentry once under the command's registered name.
func assertLookupFailed(t *testing.T, calls []recordedCall, rec *captureRecorder, command string) {
	t.Helper()
	if rec.count != 1 {
		t.Fatalf("captures = %d, want 1", rec.count)
	}
	assertCaptureNames(t, rec.kvs[0], map[string]string{"command": command})
	assertFixedReply(t, lastReply(t, calls), lookupFailedReply, rec)
}
