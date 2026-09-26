package panel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/getsentry/sentry-go"
)

// A save runs to its end whether or not the browser waits (#356). These
// tests have the browser leave partway through a save the way net/http
// shows it, by cancelling the request's context, and check the save still
// takes effect with its one change log entry.

// departure is the browser leaving partway through one request. The request
// arms it with its context's cancel; leave, called from beneath the panel at
// the moment the test picks, cancels that context, as net/http does when the
// connection closes. Outside the request leave does nothing.
type departure struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	// left is whether the request's context was cancelled while it ran.
	left bool
}

func (d *departure) leave() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
}

func (d *departure) arm(cancel context.CancelFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancel = cancel
}

func (d *departure) disarm(left bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancel, d.left = nil, left
}

// postFormAndLeave submits a form the way postForm does, over a request
// whose context d cancels when something beneath the panel calls d.leave.
func (b *browser) postFormAndLeave(target string, form url.Values, d *departure) *http.Response {
	b.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.arm(cancel)
	res := b.doContext(ctx, http.MethodPost, target,
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, strings.NewReader(form.Encode()))
	d.disarm(ctx.Err() != nil)
	return res
}

// assertLeft fails the test unless the browser left while the request ran,
// so a leave point that stopped firing cannot pass a test by accident.
func assertLeft(t *testing.T, d *departure) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.left {
		t.Fatal("the browser never left during the save: the test's leave point did not fire")
	}
}

// An update whose browser leaves while Discord renames the hub channel
// still saves: the row holds the rest of the form, and the change log
// records the save with the rename in its diff.
func TestUpdateTheBrowserLeavesDuringTheRenameStillSaves(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	d := &departure{}
	w.discord.setDuringWrite(d.leave)
	form := updateForm()
	form.Set("channel_name", "Bravo Room")
	form.Set("base_string", "Bravo Voice")
	form.Set("user_limit", "5")

	res := w.b.postFormAndLeave(hubPath(t, w.st, "hub-1"), form, d)

	assertLeft(t, d)
	assertRedirect(t, res, "/")
	h := storedHubs(t, w.st)[0]
	if h.BaseString != "Bravo Voice" || h.UserLimit != 5 {
		t.Errorf("stored hub = %+v, want base string Bravo Voice and user limit 5", h)
	}
	entries := storedChangeLog(t, w.st, h.ID)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if entries[0].Action != store.ChangeUpdate {
		t.Errorf("entry action = %q, want update", entries[0].Action)
	}
	if got := decodeDiff(t, entries[0])["channel_name"]; got.Before != "Join to create" || got.After != "Bravo Room" {
		t.Errorf("diff channel_name = %+v, want before Join to create, after Bravo Room", got)
	}
}

// A create whose browser leaves while Discord creates the hub channel still
// saves: the hub stands on the new channel, which stays in the guild, and
// the change log records the create.
func TestCreateTheBrowserLeavesDuringTheChannelCreateStillSaves(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	d := &departure{}
	w.discord.setDuringWrite(d.leave)

	res := w.b.postFormAndLeave("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"), d)

	assertLeft(t, d)
	assertRedirect(t, res, "/")
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 || hubs[0].HubChannelID != "spawn-1" {
		t.Fatalf("stored hubs = %+v, want one on the created channel spawn-1", hubs)
	}
	if _, err := w.discord.Channel("spawn-1"); err != nil {
		t.Errorf("the created channel spawn-1 is gone from the guild: %v", err)
	}
	entries := storedChangeLog(t, w.st, hubs[0].ID)
	if len(entries) != 1 || entries[0].Action != store.ChangeCreate {
		t.Errorf("the hub's entries = %+v, want one create", entries)
	}
}

// leavingStore is the store fake with the browser leaving as anything first
// calls it: each call runs d.leave and then goes through with the context it
// was handed. Every method is written out, with no embedded store to fall
// through to, so the leave fires at a save's first store call, whichever
// that is.
type leavingStore struct {
	st store.Store
	d  *departure
}

func (s leavingStore) GetHub(ctx context.Context, id int64) (store.Hub, error) {
	s.d.leave()
	return s.st.GetHub(ctx, id)
}

func (s leavingStore) ListHubs(ctx context.Context, guildID string) ([]store.Hub, error) {
	s.d.leave()
	return s.st.ListHubs(ctx, guildID)
}

func (s leavingStore) UpsertHub(ctx context.Context, hub store.Hub) (store.Hub, error) {
	s.d.leave()
	return s.st.UpsertHub(ctx, hub)
}

func (s leavingStore) DeleteHub(ctx context.Context, id int64) error {
	s.d.leave()
	return s.st.DeleteHub(ctx, id)
}

func (s leavingStore) UpsertSpawnedChannel(ctx context.Context, sc store.SpawnedChannel) error {
	s.d.leave()
	return s.st.UpsertSpawnedChannel(ctx, sc)
}

func (s leavingStore) SetSpawnedChannelLock(ctx context.Context, channelID string, lock store.ChannelLock) error {
	s.d.leave()
	return s.st.SetSpawnedChannelLock(ctx, channelID, lock)
}

func (s leavingStore) DeleteSpawnedChannel(ctx context.Context, channelID string) error {
	s.d.leave()
	return s.st.DeleteSpawnedChannel(ctx, channelID)
}

func (s leavingStore) ListSpawnedChannels(ctx context.Context) ([]store.SpawnedChannel, error) {
	s.d.leave()
	return s.st.ListSpawnedChannels(ctx)
}

func (s leavingStore) GetGuildModeratorRoles(ctx context.Context, guildID string) ([]string, error) {
	s.d.leave()
	return s.st.GetGuildModeratorRoles(ctx, guildID)
}

func (s leavingStore) SetGuildModeratorRoles(ctx context.Context, guildID string, roleIDs []string) error {
	s.d.leave()
	return s.st.SetGuildModeratorRoles(ctx, guildID, roleIDs)
}

func (s leavingStore) AppendChangeLog(ctx context.Context, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.AppendChangeLog(ctx, entry)
}

func (s leavingStore) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.ListChangeLog(ctx, hubID, limit)
}

func (s leavingStore) ListModeratorChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.ListModeratorChanges(ctx, limit)
}

// A save whose browser leaves as the save first calls the store still
// takes effect, with exactly one change log entry recording it.
func TestSaveTheBrowserLeavesAtItsFirstStoreCallStillTakesEffect(t *testing.T) {
	cases := []struct {
		name string
		hubs []store.Hub
		// path is the route the save posts to.
		path func(t *testing.T, st store.Store) string
		form url.Values
		// checkSaved checks the store holds the save and returns the hub
		// its entry references, zero for none.
		checkSaved func(t *testing.T, st store.Store) int64
		action     store.ChangeAction
	}{
		{
			name: "register",
			path: func(*testing.T, store.Store) string { return "/hubs" },
			form: registerForm("vc-2", "Squad Voice"),
			checkSaved: func(t *testing.T, st store.Store) int64 {
				return storedHubID(t, st, "vc-2")
			},
			action: store.ChangeRegister,
		},
		{
			name: "remove",
			hubs: []store.Hub{testHub()},
			path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") + "/remove" },
			checkSaved: func(t *testing.T, st store.Store) int64 {
				if hubs := storedHubs(t, st); len(hubs) != 0 {
					t.Errorf("stored hubs after the remove = %+v, want none", hubs)
				}
				return 0
			},
			action: store.ChangeRemove,
		},
		{
			name: "moderators",
			hubs: []store.Hub{testHub()},
			path: func(*testing.T, store.Store) string { return "/moderators" },
			form: moderatorsForm("role-hq"),
			checkSaved: func(t *testing.T, st store.Store) int64 {
				if got := storedGuildRoles(t, st); !sameSet(got, []string{"role-hq"}) {
					t.Errorf("stored guild roles = %v, want role-hq alone", got)
				}
				return 0
			},
			action: store.ChangeModerators,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := store.NewFake()
			for _, h := range tc.hubs {
				if _, err := fake.UpsertHub(context.Background(), h); err != nil {
					t.Fatalf("UpsertHub: %v", err)
				}
			}
			d := &departure{}
			w := newTestWorldOver(t, leavingStore{st: fake, d: d}, newFakeForum(t))
			signIn(t, w.forum, w.b)

			res := w.b.postFormAndLeave(tc.path(t, fake), tc.form, d)

			assertLeft(t, d)
			assertRedirect(t, res, "/")
			hubID := tc.checkSaved(t, fake)
			entries := storedChangeLog(t, fake, hubID)
			if len(entries) != 1 || entries[0].Action != tc.action {
				t.Errorf("entries under hub %d = %+v, want one %s", hubID, entries, tc.action)
			}
		})
	}
}

// sentryRecorder keeps the original error of every event the panel sends to
// Sentry. It sits at the SDK, where the panel's captures end, and nothing
// leaves the process.
type sentryRecorder struct {
	mu   sync.Mutex
	errs []error
}

// recordSentry binds a Sentry client that records into the returned
// recorder for the rest of the test.
func recordSentry(t *testing.T) *sentryRecorder {
	t.Helper()
	rec := &sentryRecorder{}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://key@sentry.test/1",
		Transport: discardTransport{},
		BeforeSend: func(_ *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			var original error
			if hint != nil {
				original = hint.OriginalException
			}
			rec.errs = append(rec.errs, original)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return rec
}

// recorded returns the original error of every event recorded, in order.
func (r *sentryRecorder) recorded() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// discardTransport sends nothing. BeforeSend drops every event first; the
// transport keeps the client from building a real one.
type discardTransport struct{}

func (discardTransport) Flush(time.Duration) bool              { return true }
func (discardTransport) FlushWithContext(context.Context) bool { return true }
func (discardTransport) Configure(sentry.ClientOptions)        {}
func (discardTransport) SendEvent(*sentry.Event)               {}
func (discardTransport) Close()                                {}

// stallLimit is how long a stalled store call waits for a deadline that
// never comes before it gives up, so a call made with none fails the test
// instead of hanging the suite.
const stallLimit = 2 * time.Second

// stallingStore is the store fake with its hub write stalled: the call
// answers only when its context is done, with the context's error, the way
// a Postgres write blocked on a lock does.
type stallingStore struct {
	store.Store
}

func (stallingStore) UpsertHub(ctx context.Context, _ store.Hub) (store.Hub, error) {
	select {
	case <-ctx.Done():
		return store.Hub{}, ctx.Err()
	case <-time.After(stallLimit):
		return store.Hub{}, errors.New("stalled store: the call reached no deadline")
	}
}

// A save whose store call stalls past its deadline fails, and the failure
// reaches Sentry once as the deadline running out.
func TestSaveStoreCallPastItsDeadlineFailsTheSave(t *testing.T) {
	w := newTestWorldOver(t, stallingStore{store.NewFake()}, newFakeForum(t))
	w.p.hubs.storeTimeout = 20 * time.Millisecond
	signIn(t, w.forum, w.b)
	rec := recordSentry(t)

	res := w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))

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
}
