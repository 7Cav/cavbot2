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

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// ctxStore is the store fake with the page's reads under the test's control.
// Like the fake and Postgres, a read whose context is done fails with an
// error that wraps the context's error. On top of that, one read can be
// armed to block until its context is done, the way a read waiting on a lock
// does, and one can be set to fail outright.
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
	// timed names the read that takes takes on clock each time it runs.
	timed string
	takes time.Duration
	clock *testClock
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

// readTakes makes every call of the named read take d on the panel's clock
// before it answers.
func (s *ctxStore) readTakes(read string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timed, s.takes = read, d
}

// runOutOnceStarted moves the clock on to at, where the page's time budget
// runs out, once the blocked read has started: the read is still waiting
// when it does.
func (s *ctxStore) runOutOnceStarted(started <-chan struct{}, at time.Time) {
	go func() {
		select {
		case <-started:
			s.clock.advanceTo(at)
		case <-time.After(neverReleased):
		}
	}()
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
	takes := time.Duration(0)
	if s.timed == read {
		takes = s.takes
	}
	s.mu.Unlock()
	if takes > 0 {
		s.clock.advance(takes)
	}
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

func (s *ctxStore) GetGuildModeratorRoles(ctx context.Context, guildID string) (store.GuildModeratorRoles, error) {
	if err := s.gate(ctx, "GetGuildModeratorRoles"); err != nil {
		return store.GuildModeratorRoles{}, err
	}
	return s.Fake.GetGuildModeratorRoles(ctx, guildID)
}

func (s *ctxStore) ListModeratorChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListModeratorChanges"); err != nil {
		return nil, err
	}
	return s.Fake.ListModeratorChanges(ctx, limit)
}

func (s *ctxStore) GetRecordingRoles(ctx context.Context, guildID string) (store.RecordingRoles, error) {
	if err := s.gate(ctx, "GetRecordingRoles"); err != nil {
		return store.RecordingRoles{}, err
	}
	return s.Fake.GetRecordingRoles(ctx, guildID)
}

func (s *ctxStore) ListRecordingRoleChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListRecordingRoleChanges"); err != nil {
		return nil, err
	}
	return s.Fake.ListRecordingRoleChanges(ctx, limit)
}

func (s *ctxStore) ListFoxholeRecords(ctx context.Context, guildID string) ([]store.FoxholeRecord, error) {
	if err := s.gate(ctx, "ListFoxholeRecords"); err != nil {
		return nil, err
	}
	return s.Fake.ListFoxholeRecords(ctx, guildID)
}

func (s *ctxStore) ListFoxholeChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListFoxholeChanges"); err != nil {
		return nil, err
	}
	return s.Fake.ListFoxholeChanges(ctx, limit)
}

func (s *ctxStore) SetFoxholeRecordNames(ctx context.Context, guildID string, names []store.MemberNames) error {
	if err := s.gate(ctx, "SetFoxholeRecordNames"); err != nil {
		return err
	}
	return s.Fake.SetFoxholeRecordNames(ctx, guildID, names)
}

func (s *ctxStore) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListChangeLog"); err != nil {
		return nil, err
	}
	return s.Fake.ListChangeLog(ctx, hubID, limit)
}

func (s *ctxStore) ListHubRemovals(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	if err := s.gate(ctx, "ListHubRemovals"); err != nil {
		return nil, err
	}
	return s.Fake.ListHubRemovals(ctx, limit)
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
	w := newTestWorldOver(t, st, newFakeForum(t))
	st.clock = w.clock
	return w, st
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
	w, st := newCtxWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	st.failRead("ListHubs")

	res := w.b.get("/")

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := failureOf(t, doc); got != "read-failed" {
		t.Errorf("failure page = %q, want read-failed", got)
	}
	assertSignedInPage(t, doc)
	if n := len(reported.recorded()); n != 1 {
		t.Errorf("sent %d Sentry events, want 1", n)
	}
}

func TestHubPageThatRunsOutOfTimeIsReportedAndSaysSo(t *testing.T) {
	w, st := newCtxWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	st.runOutOnceStarted(st.blockRead("ListHubs", nil), w.clock.Now().Add(hubPageBudget))

	res := w.b.get("/")

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := failureOf(t, doc); got != "too-slow" {
		t.Errorf("failure page = %q, want too-slow", got)
	}
	assertSignedInPage(t, doc)
	errs := reported.recorded()
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

// Try again on the error page loads the page afresh by GET, so a refused
// save is never posted twice, and opens where that page opens: a hub's edit
// form at the form, sections below the top, and the hub list at its top.
func TestHubPageFailureOffersTryAgainAtTheGETAddress(t *testing.T) {
	cases := []struct {
		name string
		// fail is the request whose hub page fails, made with the failure
		// it meets armed.
		fail func(t *testing.T, w *testWorld, st *ctxStore, hubPath string) *http.Response
		// atEditForm is whether Try again opens at the hub's edit form
		// rather than at the top of the hub list.
		atEditForm bool
	}{
		{"the hub list",
			func(_ *testing.T, w *testWorld, st *ctxStore, _ string) *http.Response {
				st.failRead("ListModeratorChanges")
				return w.b.get("/")
			}, false},
		{"a hub's edit form",
			func(_ *testing.T, w *testWorld, st *ctxStore, hubPath string) *http.Response {
				st.failRead("ListModeratorChanges")
				return w.b.get("/?hub=" + strings.TrimPrefix(hubPath, "/hubs/"))
			}, true},
		{"a refused hub save",
			func(t *testing.T, w *testWorld, st *ctxStore, hubPath string) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("base_string", "   ")
				st.failRead("ListModeratorChanges")
				return w.b.postForm(hubPath, form)
			}, true},
		{"a hub save that found no guild data",
			func(t *testing.T, w *testWorld, st *ctxStore, hubPath string) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("base_string", "Bravo Voice")
				w.discord.setGuild(commands.GuildDataAbsent)
				return w.b.postForm(hubPath, form)
			}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, st := newCtxWorld(t, testHub())
			signIn(t, w.forum, w.b)
			path := hubPath(t, st.Fake, "hub-1")
			res := tc.fail(t, w, st, path)
			if res.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", res.StatusCode)
			}
			retry := retryOf(t, parseHTML(t, res))
			st.failRead("")
			w.discord.setGuild(commands.GuildDataPresent)

			doc, fragment := landing(t, w.b, retry, w.b.get(retry))

			if !tc.atEditForm {
				if fragment != "" {
					t.Errorf("Try again opens at fragment %q, want the top of the hub list", fragment)
				}
				return
			}
			section := hubSection(t, doc, storedHubID(t, st.Fake, "hub-1"))
			if findElement(section, "", "data-field", "save") == nil {
				t.Fatal("Try again opens a page whose hub section shows no edit form")
			}
			if target := landedOn(doc, fragment); target == nil || !within(target, section) {
				t.Errorf("Try again opens at fragment %q, want the hub's edit form", fragment)
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
	reported := recordSentry(t)
	logs := captureLogs(t)
	started := st.blockRead("ListHubs", nil)

	closeMidLoad(t, w, "/", started)

	if n := len(reported.recorded()); n != 0 {
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
	reported := recordSentry(t)
	started := st.blockRead("ListHubs", errStoreDown)

	closeMidLoad(t, w, "/", started)

	errs := reported.recorded()
	if len(errs) != 1 {
		t.Fatalf("sent %d Sentry events, want 1", len(errs))
	}
	if !errors.Is(errs[0], errStoreDown) {
		t.Errorf("reported %v, want the read's own failure", errs[0])
	}
}
