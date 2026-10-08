package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
)

// A panel request whose handler panics (#512) answers 500 with the text a
// failed save answers with, not an empty page, and the panic reaches Sentry
// as one event tagged panel-request with the request path.

// panickingStore is the fake store with a guild-wide moderators save that
// panics, the way triage forced the panic.
type panickingStore struct {
	*store.Fake
}

func (panickingStore) SaveGuildModeratorRoles(context.Context, string, store.GuildModeratorRoles, store.ChangeLogEntry) error {
	panic("store: moderators save blew up")
}

func TestRequestWhoseHandlerPanicsAnswersTheServerErrorAndReportsOnce(t *testing.T) {
	fake := store.NewFake()
	w := newTestWorldOver(t, panickingStore{fake}, newFakeForum(t))
	signIn(t, w.forum, w.b)
	rec := recordSentry(t)

	res := w.b.postForm("/moderators", moderatorsForm(t, fake, "role-hq"))

	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusInternalServerError)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), serverErrorText) {
		t.Errorf("body = %q, want it to carry %q, the text a failed save answers with", body, serverErrorText)
	}

	events := rec.sent()
	if len(events) != 1 {
		t.Fatalf("sent %d Sentry events, want 1", len(events))
	}
	var event struct {
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(events[0], &event); err != nil {
		t.Fatalf("event %s: %v", events[0], err)
	}
	if got := event.Tags["context"]; got != "panel-request" {
		t.Errorf("tag context = %q, want panel-request", got)
	}
	if !carriesPath(t, events[0], "/moderators") {
		t.Errorf("the event does not carry the request path /moderators: %s", events[0])
	}
}

// A request that does not panic answers as it did before: the recovery adds
// nothing to its answer.
func TestRequestThatDoesNotPanicAnswersWithoutTheServerError(t *testing.T) {
	fake := store.NewFake()
	w := newTestWorldOver(t, fake, newFakeForum(t))
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/moderators", moderatorsForm(t, fake, "role-hq"))

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), serverErrorText) {
		t.Errorf("body = %q, want no %q: nothing panicked", body, serverErrorText)
	}
}

// carriesPath reports whether some string in the event, wherever the SDK
// puts it, is the path or a URL with that path. The criterion is that the
// event carries the path, not which of the event's fields holds it, so the
// test reads every field and not only the extra context.
func carriesPath(t *testing.T, event []byte, path string) bool {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(event, &decoded); err != nil {
		t.Fatalf("event %s: %v", event, err)
	}
	var walk func(v any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case string:
			if v == path {
				return true
			}
			u, err := url.Parse(v)
			return err == nil && u.Host != "" && u.Path == path
		case map[string]any:
			for _, e := range v {
				if walk(e) {
					return true
				}
			}
		case []any:
			return slices.ContainsFunc(v, walk)
		}
		return false
	}
	return walk(decoded)
}
