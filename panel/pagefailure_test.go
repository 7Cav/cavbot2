package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/getsentry/sentry-go"
	"golang.org/x/net/html"
)

// sentryRecorder keeps the error behind every event the panel sends to
// Sentry. It keeps the error the panel captured, not sentry-go's rendering of
// it, so a test asks errors.Is of what was reported.
type sentryRecorder struct {
	mu   sync.Mutex
	errs []error
}

// errors returns the captured errors, one per event, in order.
func (r *sentryRecorder) errors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// discardTransport sends nothing anywhere.
type discardTransport struct{}

func (discardTransport) Configure(sentry.ClientOptions)        {}
func (discardTransport) SendEvent(*sentry.Event)               {}
func (discardTransport) Flush(time.Duration) bool              { return true }
func (discardTransport) FlushWithContext(context.Context) bool { return true }
func (discardTransport) Close()                                {}

// captureSentry installs a Sentry client for the test and returns what it
// records. Without it the panel's captures only log, as with no SENTRY_DSN.
func captureSentry(t *testing.T) *sentryRecorder {
	t.Helper()
	rec := &sentryRecorder{}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://test@example.com/1",
		Transport: discardTransport{},
		BeforeSend: func(e *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			var captured error
			if hint != nil {
				captured = hint.OriginalException
			}
			rec.errs = append(rec.errs, captured)
			return e
		},
	})
	if err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return rec
}

// ctxStore is the store fake behind a database that honours the context the
// way Postgres does: a read whose context is done fails with an error that
// wraps the context's error. One read can be armed to block until its
// context is done, the way a read waiting on a lock does, and one can be set
// to fail outright.
type ctxStore struct {
	*store.Fake
	mu sync.Mutex
	// blocked names the read armed to block, once; started closes when it
	// begins waiting, and releaseErr is what it fails with once released,
	// the context's own error when nil.
	blocked    string
	started    chan struct{}
	releaseErr error
	// failing names the read that fails with errStoreDown.
	failing string
}

// errStoreDown is a store read failing for a reason that is not the
// context.
var errStoreDown = errors.New("store: connection reset by peer")

// neverReleased bounds a blocked read whose context never ends, so a panel
// without a time budget fails the test instead of hanging it.
const neverReleased = 5 * time.Second

// blockRead arms the named read to block until its context is done, then
// fail with releaseErr, or with the context's error when releaseErr is nil. The channel
// closes once the read is waiting.
func (s *ctxStore) blockRead(read string, releaseErr error) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocked, s.releaseErr, s.started = read, releaseErr, make(chan struct{})
	return s.started
}

// failRead makes every call of the named read fail with errStoreDown.
func (s *ctxStore) failRead(read string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = read
}

// gate is what every page read passes through before the fake answers.
func (s *ctxStore) gate(ctx context.Context, read string) error {
	s.mu.Lock()
	failing := s.failing == read
	blocked, started, releaseErr := s.blocked == read, s.started, s.releaseErr
	if blocked {
		s.blocked = ""
	}
	s.mu.Unlock()
	if failing {
		return fmt.Errorf("%s: %w", read, errStoreDown)
	}
	if blocked {
		close(started)
		select {
		case <-ctx.Done():
		case <-time.After(neverReleased):
			return fmt.Errorf("%s: blocked read never released", read)
		}
		if releaseErr != nil {
			return fmt.Errorf("%s: %w", read, releaseErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", read, err)
	}
	return nil
}

func (s *ctxStore) ListHubs(ctx context.Context, guildID string) ([]store.Hub, error) {
	if err := s.gate(ctx, "ListHubs"); err != nil {
		return nil, err
	}
	return s.Fake.ListHubs(ctx, guildID)
}

func (s *ctxStore) GetGuildModeratorRoles(ctx context.Context, guildID string) ([]string, error) {
	if err := s.gate(ctx, "GetGuildModeratorRoles"); err != nil {
		return nil, err
	}
	return s.Fake.GetGuildModeratorRoles(ctx, guildID)
}

func (s *ctxStore) ListModeratorChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListModeratorChanges"); err != nil {
		return nil, err
	}
	return s.Fake.ListModeratorChanges(ctx, limit)
}

func (s *ctxStore) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListChangeLog"); err != nil {
		return nil, err
	}
	return s.Fake.ListChangeLog(ctx, hubID, limit)
}

// newCtxWorld is the test world over a ctxStore holding the given hubs, and
// that store. The world's st is nil; a test reads back through the store's
// Fake.
func newCtxWorld(t *testing.T, hubs ...store.Hub) (*testWorld, *ctxStore) {
	t.Helper()
	st := &ctxStore{Fake: store.NewFake()}
	for _, h := range hubs {
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
	}
	return newTestWorldOver(t, st, newFakeForum(t)), st
}

// failureOf returns the data-failure on <main>: which failure page this is.
// The values are a test contract; the page's wording is not.
func failureOf(t *testing.T, doc *html.Node) string {
	t.Helper()
	m := findElement(doc, "main", "", "")
	if m == nil {
		t.Fatal("page has no <main>")
	}
	kind, ok := attrValue(m, "data-failure")
	if !ok {
		t.Fatal("<main> carries no data-failure")
	}
	return kind
}

// assertSignedInPage checks the page keeps the rail's signed-in identity.
func assertSignedInPage(t *testing.T, doc *html.Node) {
	t.Helper()
	name := findElement(doc, "", "data-field", "username")
	if name == nil {
		t.Fatal("page has no signed-in identity under data-field=username")
	}
	if got := textOf(name); got != testUsername {
		t.Errorf("username shown = %q, want %q", got, testUsername)
	}
}

func TestHubPageReadFailureIsReportedAndSaysSo(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := captureSentry(t)
	w.discord.setListErr(errors.New("discord: 503"))

	res := w.b.get("/")

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := failureOf(t, doc); got != "read-failed" {
		t.Errorf("failure page = %q, want read-failed", got)
	}
	assertSignedInPage(t, doc)
	if n := len(reported.errors()); n != 1 {
		t.Errorf("sent %d Sentry events, want 1", n)
	}
}

func TestHubPageThatRunsOutOfTimeIsReportedAndSaysSo(t *testing.T) {
	w, st := newCtxWorld(t, testHub())
	w.p.pageBudget = 10 * time.Millisecond
	signIn(t, w.forum, w.b)
	reported := captureSentry(t)
	st.blockRead("ListHubs", nil)

	res := w.b.get("/")

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := failureOf(t, doc); got != "too-slow" {
		t.Errorf("failure page = %q, want too-slow", got)
	}
	assertSignedInPage(t, doc)
	errs := reported.errors()
	if len(errs) != 1 {
		t.Fatalf("sent %d Sentry events, want 1", len(errs))
	}
	if !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Errorf("reported %v, want an error wrapping context.DeadlineExceeded", errs[0])
	}
}

// retryOf returns where the error page's Try again link leads, found by its
// data-retry hook rather than its label.
func retryOf(t *testing.T, doc *html.Node) string {
	t.Helper()
	a := findElement(doc, "a", "data-retry", "")
	if a == nil {
		t.Fatal("error page has no Try again link under data-retry")
	}
	href, _ := attrValue(a, "href")
	return href
}

func TestHubPageFailureOffersTryAgainAtTheGETAddress(t *testing.T) {
	cases := []struct {
		name string
		// load is the request whose hub page fails.
		load func(w *testWorld, hubPath string) *http.Response
		// retry is the page's GET address, from the stored hub's ID.
		retry func(hubID string) string
	}{
		{"the hub list",
			func(w *testWorld, _ string) *http.Response { return w.b.get("/") },
			func(string) string { return "/" }},
		{"a hub's edit form",
			func(w *testWorld, hubPath string) *http.Response {
				return w.b.get("/?hub=" + strings.TrimPrefix(hubPath, "/hubs/"))
			},
			func(id string) string { return "/?hub=" + id }},
		{"a refused hub save",
			func(w *testWorld, hubPath string) *http.Response {
				form := updateForm()
				form.Set("base_string", "   ")
				return w.b.postForm(hubPath, form)
			},
			func(id string) string { return "/?hub=" + id }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, st := newCtxWorld(t, testHub())
			signIn(t, w.forum, w.b)
			path := hubPath(t, st.Fake, "hub-1")
			st.failRead("ListModeratorChanges")

			res := tc.load(w, path)

			if res.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", res.StatusCode)
			}
			if got, want := retryOf(t, parseHTML(t, res)), tc.retry(strings.TrimPrefix(path, "/hubs/")); got != want {
				t.Errorf("Try again leads to %q, want %q", got, want)
			}
		})
	}
}

// A Discord read takes no context, so the budget can run out while one is
// in flight. The page must then fail under that read's name, the name its
// own failure is reported under, and not under the store read that follows
// it. The store here honours the context the way Postgres does, so a later
// read would fail with the deadline too.
func TestDiscordReadThatRunsOutOfTimeIsReportedUnderItsOwnName(t *testing.T) {
	errDiscord := errors.New("discord: 503 Service Unavailable")
	cases := []struct {
		name string
		fail func(*fakeDiscord, error)
		slow func(*fakeDiscord, time.Duration)
	}{
		{"the channel list", (*fakeDiscord).setListErr, (*fakeDiscord).setListDelay},
		{"the guild read", (*fakeDiscord).setGuildErr, (*fakeDiscord).setGuildDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failing, _ := newCtxWorld(t, testHub())
			signIn(t, failing.forum, failing.b)
			reported := captureSentry(t)
			tc.fail(failing.discord, errDiscord)
			failing.b.get("/")
			errs := reported.errors()
			if len(errs) != 1 {
				t.Fatalf("the read's own failure sent %d Sentry events, want 1", len(errs))
			}
			name, ok := strings.CutSuffix(errs[0].Error(), errDiscord.Error())
			if !ok || name == "" {
				t.Fatalf("the read's own failure reported %q, want its name before %q", errs[0], errDiscord)
			}

			slow, _ := newCtxWorld(t, testHub())
			slow.p.pageBudget = 100 * time.Millisecond
			signIn(t, slow.forum, slow.b)
			reported = captureSentry(t)
			tc.slow(slow.discord, 300*time.Millisecond)

			slow.b.get("/")

			errs = reported.errors()
			if len(errs) != 1 {
				t.Fatalf("sent %d Sentry events, want 1", len(errs))
			}
			if !errors.Is(errs[0], context.DeadlineExceeded) {
				t.Errorf("reported %v, want an error wrapping context.DeadlineExceeded", errs[0])
			}
			if !strings.HasPrefix(errs[0].Error(), name) {
				t.Errorf("reported %q, want it under the read's own name %q", errs[0], name)
			}
		})
	}
}

// closeMidLoad loads the page and closes the connection once the blocked
// read is waiting, which net/http turns into a cancelled request context.
// It returns when the handler does.
func closeMidLoad(t *testing.T, w *testWorld, target string, started <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.b.doContext(ctx, http.MethodGet, target, nil, nil)
	}()
	select {
	case <-started:
	case <-time.After(neverReleased):
		t.Fatal("the page never reached the blocked read")
	}
	cancel()
	<-done
}

// abandonedLines returns the INFO records of an abandoned page load, found
// by the level and the fields the brief fixes, never by the sentence: a
// step, and the signed-in user.
func abandonedLines(records []map[string]string) []map[string]string {
	var out []map[string]string
	for _, r := range records {
		if _, ok := r["step"]; ok && r["level"] == "INFO" &&
			r["username"] == testUsername && r["forum_user_id"] == strconv.Itoa(testUserID) {
			out = append(out, r)
		}
	}
	return out
}

func TestAbandonedHubPageLoadIsLoggedNotReported(t *testing.T) {
	w, st := newCtxWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := captureSentry(t)
	logs := captureLogs(t)
	started := st.blockRead("ListHubs", nil)

	closeMidLoad(t, w, "/", started)

	if n := len(reported.errors()); n != 0 {
		t.Errorf("sent %d Sentry events, want none", n)
	}
	if lines := abandonedLines(logs()); len(lines) != 1 {
		t.Errorf("logged %d INFO lines with a step and the user, want 1", len(lines))
	}
}

// The read's error decides, not whether the connection is still open: a
// read that fails for its own reason is a panel failure even with nobody
// waiting.
func TestReadFailureAfterTheConnectionClosedIsStillReported(t *testing.T) {
	w, st := newCtxWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := captureSentry(t)
	started := st.blockRead("ListHubs", errStoreDown)

	closeMidLoad(t, w, "/", started)

	errs := reported.errors()
	if len(errs) != 1 {
		t.Fatalf("sent %d Sentry events, want 1", len(errs))
	}
	if !errors.Is(errs[0], errStoreDown) {
		t.Errorf("reported %v, want the read's own failure", errs[0])
	}
}
