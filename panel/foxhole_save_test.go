package panel

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/7cav/cavbot2/store"
)

// firstCallStall is a hookedStore hook that, once armed, stalls the first
// store call made, whichever method it is, as stallingStore stalls SaveHub.
// The call goes through once its context is done, and the store fake
// answers a done context with the context's error. A call made with no
// deadline fails the test after stallLimit and goes through as made. It
// stalls one call, so the page loads before it and the Foxhole runtime's
// reads after it answer as the fake does.
type firstCallStall struct {
	t     *testing.T
	clock *testClock
	armed atomic.Bool
}

func (s *firstCallStall) hook(ctx context.Context) {
	if s.armed.CompareAndSwap(true, false) && !stallUntilDone(ctx, s.clock) {
		s.t.Errorf("the stalled store call reached no deadline in %v", stallLimit)
	}
}

// A Foxhole save whose store call stalls past its deadline fails, as a hub
// save does: the Foxhole manager gets the server error answer, and the
// failure reaches Sentry once as the deadline running out. Each case
// stalls the first store call its save makes, and a stalled add starts no
// Foxhole action (#649).
func TestFoxholeSaveStoreCallPastItsDeadlineFailsTheSave(t *testing.T) {
	cases := []struct {
		name string
		// open loads what the manager posts the save from, and returns the
		// post, made once the store stalls.
		open func(t *testing.T, w *testWorld) (save func() *http.Response)
		// action is the change log action of the Foxhole action the save
		// starts, empty for a save that starts none.
		action string
	}{
		{
			name: "note save",
			open: func(t *testing.T, w *testWorld) func() *http.Response {
				doc := openNote(t, w.b, memberDoe.ID)
				return func() *http.Response { return submitNote(t, w.b, doc, "Logistics lead this war") }
			},
		},
		{
			name: "approve",
			open: func(t *testing.T, w *testWorld) func() *http.Response {
				doc := parseHTML(t, w.b.get(foxholePath))
				return func() *http.Response { return submitSelection(t, w.b, doc, opApprove, memberKestrel.ID) }
			},
		},
		{
			name: "clear approvals",
			open: func(t *testing.T, w *testWorld) func() *http.Response {
				approveThroughThePage(t, w, memberKestrel.ID)
				doc := parseHTML(t, w.b.get(foxholePath))
				return func() *http.Response { return submitSelection(t, w.b, doc, opClear, memberKestrel.ID) }
			},
		},
		{
			name: "add",
			open: func(t *testing.T, w *testWorld) func() *http.Response {
				page := parseHTML(t, w.b.get(foxholePath))
				doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", memberVance.Username))
				return func() *http.Response { return confirmAdd(t, w.b, doc, nil) }
			},
			action: "add",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stall := &firstCallStall{t: t, clock: installClock(t)}
			w := newFoxholeWorldWith(t, func(st store.Store) store.Store { return hookedStore{st: st, hook: stall.hook} })
			save := tc.open(t, w)
			rec := recordSentry(t)
			stall.armed.Store(true)

			res := save()

			if !isServerError(res.StatusCode) {
				t.Errorf("status = %d, want 5xx", res.StatusCode)
			}
			errs := rec.recorded()
			if len(errs) != 1 {
				t.Fatalf("Sentry got %d events, want 1", len(errs))
			}
			if !errors.Is(errs[0], context.DeadlineExceeded) {
				t.Errorf("the event's error %v does not wrap context.DeadlineExceeded", errs[0])
			}
			if tc.action != "" {
				if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), tc.action); len(entries) != 0 {
					t.Errorf("the change log holds %d %s entries, want none: the stalled save started a Foxhole action", len(entries), tc.action)
				}
			}
		})
	}
}
