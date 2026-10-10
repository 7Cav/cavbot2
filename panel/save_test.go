package panel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
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
	form := updateForm(t, w.st)
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

func (s leavingStore) SaveHub(ctx context.Context, hub store.Hub, entry store.ChangeLogEntry) (store.Hub, error) {
	s.d.leave()
	return s.st.SaveHub(ctx, hub, entry)
}

func (s leavingStore) RemoveHub(ctx context.Context, id int64, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.RemoveHub(ctx, id, entry)
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

func (s leavingStore) GetGuildModeratorRoles(ctx context.Context, guildID string) (store.GuildModeratorRoles, error) {
	s.d.leave()
	return s.st.GetGuildModeratorRoles(ctx, guildID)
}

func (s leavingStore) SaveGuildModeratorRoles(ctx context.Context, guildID string, roles store.GuildModeratorRoles, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.SaveGuildModeratorRoles(ctx, guildID, roles, entry)
}

func (s leavingStore) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.ListChangeLog(ctx, hubID, limit)
}

func (s leavingStore) ListModeratorChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.ListModeratorChanges(ctx, limit)
}

func (s leavingStore) GetRecordingRoles(ctx context.Context, guildID string) (store.RecordingRoles, error) {
	s.d.leave()
	return s.st.GetRecordingRoles(ctx, guildID)
}

func (s leavingStore) SaveRecordingRoles(ctx context.Context, guildID string, roles store.RecordingRoles, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.SaveRecordingRoles(ctx, guildID, roles, entry)
}

func (s leavingStore) ListRecordingRoleChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.ListRecordingRoleChanges(ctx, limit)
}

func (s leavingStore) StartRecording(ctx context.Context, rec store.Recording) (store.Recording, error) {
	s.d.leave()
	return s.st.StartRecording(ctx, rec)
}

func (s leavingStore) StopRecording(ctx context.Context, id int64, at time.Time) error {
	s.d.leave()
	return s.st.StopRecording(ctx, id, at)
}

func (s leavingStore) ListRecordings(ctx context.Context, guildID string) ([]store.Recording, error) {
	s.d.leave()
	return s.st.ListRecordings(ctx, guildID)
}

func (s leavingStore) ListFoxholeRecords(ctx context.Context, guildID string) ([]store.FoxholeRecord, error) {
	s.d.leave()
	return s.st.ListFoxholeRecords(ctx, guildID)
}

func (s leavingStore) SaveFoxholeNote(ctx context.Context, guildID string, save store.NoteSave, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.SaveFoxholeNote(ctx, guildID, save, entry)
}

func (s leavingStore) ApproveFoxholeMembers(ctx context.Context, guildID string, members []store.MemberNames, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.ApproveFoxholeMembers(ctx, guildID, members, entry)
}

func (s leavingStore) ClearFoxholeApprovals(ctx context.Context, guildID string, memberIDs []string, entry store.ChangeLogEntry) error {
	s.d.leave()
	return s.st.ClearFoxholeApprovals(ctx, guildID, memberIDs, entry)
}

func (s leavingStore) ClearFoxholeApprovalForRemoval(ctx context.Context, guildID, memberID string) (bool, error) {
	s.d.leave()
	return s.st.ClearFoxholeApprovalForRemoval(ctx, guildID, memberID)
}

func (s leavingStore) SetFoxholeRecordNames(ctx context.Context, guildID string, names []store.MemberNames) error {
	s.d.leave()
	return s.st.SetFoxholeRecordNames(ctx, guildID, names)
}

func (s leavingStore) ListFoxholeChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.ListFoxholeChanges(ctx, limit)
}

func (s leavingStore) StartFoxholeReport(ctx context.Context, entry store.ChangeLogEntry) (store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.StartFoxholeReport(ctx, entry)
}

func (s leavingStore) UpdateFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	s.d.leave()
	return s.st.UpdateFoxholeReport(ctx, id, diff)
}

func (s leavingStore) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	s.d.leave()
	return s.st.EndFoxholeReport(ctx, id, diff)
}

func (s leavingStore) RunningFoxholeReports(ctx context.Context) ([]store.ChangeLogEntry, error) {
	s.d.leave()
	return s.st.RunningFoxholeReports(ctx)
}

func (s leavingStore) LastFoxholeReport(ctx context.Context) (store.FoxholeReport, error) {
	s.d.leave()
	return s.st.LastFoxholeReport(ctx)
}

func (s leavingStore) FoxholeReport(ctx context.Context, id int64) (store.FoxholeReport, error) {
	s.d.leave()
	return s.st.FoxholeReport(ctx, id)
}

// A save whose browser leaves as the save first calls the store still
// takes effect, with exactly one change log entry recording it.
func TestSaveTheBrowserLeavesAtItsFirstStoreCallStillTakesEffect(t *testing.T) {
	cases := []struct {
		name string
		hubs []store.Hub
		// path is the route the save posts to.
		path func(t *testing.T, st store.Store) string
		form func(t *testing.T, st store.Store) url.Values
		// checkSaved checks the store holds the save and returns the hub
		// its entry references, zero for none.
		checkSaved func(t *testing.T, st store.Store) int64
		action     store.ChangeAction
	}{
		{
			name: "register",
			path: func(*testing.T, store.Store) string { return "/hubs" },
			form: fixedForm(registerForm("vc-2", "Squad Voice")),
			checkSaved: func(t *testing.T, st store.Store) int64 {
				return storedHubID(t, st, "vc-2")
			},
			action: store.ChangeRegister,
		},
		{
			name: "remove",
			hubs: []store.Hub{testHub()},
			path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") + "/remove" },
			form: fixedForm(nil),
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
			form: func(t *testing.T, st store.Store) url.Values { return moderatorsForm(t, st, "role-hq") },
			checkSaved: func(t *testing.T, st store.Store) int64 {
				if got := storedGuildRoles(t, st); !sameSet(got, []string{"role-hq"}) {
					t.Errorf("stored guild roles = %v, want role-hq alone", got)
				}
				return 0
			},
			action: store.ChangeModerators,
		},
		{
			name: "recording roles",
			path: func(*testing.T, store.Store) string { return "/recording-roles" },
			form: func(t *testing.T, st store.Store) url.Values { return recordingRolesForm(t, st, "role-hq") },
			checkSaved: func(t *testing.T, st store.Store) int64 {
				if got := storedRecordingRoles(t, st).RoleIDs; !sameSet(got, []string{"role-hq"}) {
					t.Errorf("stored recording roles = %v, want role-hq alone", got)
				}
				return 0
			},
			action: store.ChangeRecordingRoles,
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

			res := w.b.postFormAndLeave(tc.path(t, fake), tc.form(t, fake), d)

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

// fixedForm is a form that is the same whatever the store holds, for a
// table whose other forms are read off the store.
func fixedForm(form url.Values) func(*testing.T, store.Store) url.Values {
	return func(*testing.T, store.Store) url.Values { return form }
}

// sentryRecorder keeps the original error of every event the panel sends to
// Sentry, and the event as JSON, the way the SDK sends it. It sits at the
// SDK, where the panel's captures end, and nothing leaves the process.
type sentryRecorder struct {
	mu     sync.Mutex
	errs   []error
	events [][]byte
}

// recordSentry binds a Sentry client that records into the returned
// recorder for the rest of the test.
func recordSentry(t *testing.T) *sentryRecorder {
	t.Helper()
	rec := &sentryRecorder{}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://key@sentry.test/1",
		Transport: discardTransport{},
		BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			var original error
			if hint != nil {
				original = hint.OriginalException
			}
			rec.errs = append(rec.errs, original)
			sent, err := json.Marshal(event)
			if err != nil {
				t.Errorf("the event does not marshal the way the SDK sends it: %v", err)
			}
			rec.events = append(rec.events, sent)
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

// sent returns every event recorded as the SDK would send it, in order.
func (r *sentryRecorder) sent() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.events...)
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
// a Postgres write blocked on a lock does. While it stalls, the panel's
// clock passes the store timeout.
type stallingStore struct {
	store.Store
	clock *testClock
}

func (s stallingStore) SaveHub(ctx context.Context, _ store.Hub, _ store.ChangeLogEntry) (store.Hub, error) {
	s.clock.advance(storeTimeout)
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
	w := newTestWorldOver(t, stallingStore{store.NewFake(), installClock(t)}, newFakeForum(t))
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

// errStoreRefused is what a refusing store answers: the database gone away
// at the moment the save writes.
var errStoreRefused = errors.New("store: connection refused")

// refusingStore is the store fake with every save's write refused: each
// write that carries a save's settings and its change log entry. Reads go
// through to the fake.
type refusingStore struct {
	*store.Fake
}

func (refusingStore) SaveHub(context.Context, store.Hub, store.ChangeLogEntry) (store.Hub, error) {
	return store.Hub{}, errStoreRefused
}

func (refusingStore) RemoveHub(context.Context, int64, store.ChangeLogEntry) error {
	return errStoreRefused
}

func (refusingStore) SaveGuildModeratorRoles(context.Context, string, store.GuildModeratorRoles, store.ChangeLogEntry) error {
	return errStoreRefused
}

func (refusingStore) SaveRecordingRoles(context.Context, string, store.RecordingRoles, store.ChangeLogEntry) error {
	return errStoreRefused
}

// savedState is everything a save can write, read back through the store:
// the test guild's hubs, its guild-wide moderator roles, its recording
// roles, and the change log under each hub and under none.
type savedState struct {
	Hubs      []store.Hub
	Roles     store.GuildModeratorRoles
	Recording store.RecordingRoles
	Entries   map[int64][]store.ChangeLogEntry
}

// readSavedState reads the store's savedState, hubs in ID order.
func readSavedState(t *testing.T, st store.Store) savedState {
	t.Helper()
	hubs := storedHubs(t, st)
	slices.SortFunc(hubs, func(a, b store.Hub) int { return cmp.Compare(a.ID, b.ID) })
	roles, err := st.GetGuildModeratorRoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetGuildModeratorRoles: %v", err)
	}
	state := savedState{Hubs: hubs, Roles: roles, Recording: storedRecordingRoles(t, st),
		Entries: map[int64][]store.ChangeLogEntry{0: storedChangeLog(t, st, 0)}}
	for _, h := range hubs {
		state.Entries[h.ID] = storedChangeLog(t, st, h.ID)
	}
	return state
}

// A save whose store write fails answers 500 and reaches Sentry once. The
// store and its change log are as they were, and the running bot keeps
// acting on the settings from before the save: it never learns of a write
// that did not land (#362).
func TestSaveWhoseStoreWriteFailsLeavesTheRuntimeAsItWas(t *testing.T) {
	cases := []struct {
		name string
		path func(t *testing.T, st store.Store) string
		form func(t *testing.T, st store.Store) url.Values
		// checkRuntime checks the runtime still acts on the settings from
		// before the save. Nil for a save no runtime acts on.
		checkRuntime func(t *testing.T, w *testWorld)
	}{
		{
			name: "create",
			path: func(*testing.T, store.Store) string { return "/hubs" },
			form: fixedForm(createForm("cat-1", "Squad Join", "Squad Voice")),
			checkRuntime: func(t *testing.T, w *testWorld) {
				w.join("user-a", "spawn-1")
				if n := w.discord.createCount(); n != 1 {
					t.Errorf("a join to the channel the failed create made led to %d creates in all, want 1: the channel alone", n)
				}
			},
		},
		{
			name: "register",
			path: func(*testing.T, store.Store) string { return "/hubs" },
			form: fixedForm(registerForm("vc-2", "Squad Voice")),
			checkRuntime: func(t *testing.T, w *testWorld) {
				w.join("user-a", "vc-2")
				if n := w.discord.createCount(); n != 0 {
					t.Errorf("a join to the channel of the failed register made %d creates, want 0", n)
				}
			},
		},
		{
			name: "update",
			path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") },
			form: func(t *testing.T, st store.Store) url.Values {
				form := updateForm(t, st)
				form.Set("base_string", "Bravo Voice")
				form.Set("user_limit", "5")
				return form
			},
			checkRuntime: func(t *testing.T, w *testWorld) {
				w.join("user-a", "hub-1")
				creates := w.discord.creates()
				if len(creates) != 1 {
					t.Fatalf("a join after the failed update made %d creates, want 1", len(creates))
				}
				if !strings.Contains(creates[0].Name, "Arma Voice") || creates[0].UserLimit != 0 {
					t.Errorf("create payload = name %q, user limit %d; want the old base string Arma Voice and no limit", creates[0].Name, creates[0].UserLimit)
				}
			},
		},
		{
			name: "remove",
			path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") + "/remove" },
			form: fixedForm(nil),
			checkRuntime: func(t *testing.T, w *testWorld) {
				w.join("user-a", "hub-1")
				if n := w.discord.createCount(); n != 1 {
					t.Errorf("a join after the failed remove made %d creates, want 1: the hub still spawns", n)
				}
			},
		},
		{
			name: "moderators",
			path: func(*testing.T, store.Store) string { return "/moderators" },
			form: func(t *testing.T, st store.Store) url.Values { return moderatorsForm(t, st, "role-hq") },
			checkRuntime: func(t *testing.T, w *testWorld) {
				w.joinAs("user-owner", "hub-1", testRankSGT)
				w.joinAs("user-owner", "spawn-1", testRankSGT)
				w.joinAs("user-mod", "spawn-1", "role-hq")
				if _, err := w.runtime.Rename(commands.Invoker{UserID: "user-mod", Roles: []string{"role-hq"}}, "Alpha"); err == nil {
					t.Error("a rename by a holder of the role the failed save named passed, want a refusal")
				}
			},
		},
		{
			// The recording runtime reads the recording roles from the
			// store at each start, so no runtime holds them.
			name: "recording roles",
			path: func(*testing.T, store.Store) string { return "/recording-roles" },
			form: func(t *testing.T, st store.Store) url.Values { return recordingRolesForm(t, st, "role-hq") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := store.NewFake()
			if _, err := fake.UpsertHub(context.Background(), testHub()); err != nil {
				t.Fatalf("UpsertHub: %v", err)
			}
			w := newTestWorldOver(t, refusingStore{fake}, newFakeForum(t))
			signIn(t, w.forum, w.b)
			rec := recordSentry(t)
			before := readSavedState(t, fake)

			res := w.b.postForm(tc.path(t, fake), tc.form(t, fake))

			if !isServerError(res.StatusCode) {
				t.Errorf("status = %d, want 5xx", res.StatusCode)
			}
			if errs := rec.recorded(); len(errs) != 1 {
				t.Errorf("Sentry got %d events, want 1", len(errs))
			}
			if after := readSavedState(t, fake); !reflect.DeepEqual(after, before) {
				t.Errorf("the store after the failed save = %+v, want it as before, %+v", after, before)
			}
			if tc.checkRuntime != nil {
				tc.checkRuntime(t, w)
			}
		})
	}
}

// An update that renamed the hub channel and then fails its store write
// leaves the channel renamed (#356) and the store and its change log as
// they were, and the one Sentry event's error names both channel names, so
// the mismatch between Discord and the store can be traced.
func TestUpdateThatRenamedAndFailsItsWriteKeepsTheRenameAndNamesBothNames(t *testing.T) {
	fake := store.NewFake()
	if _, err := fake.UpsertHub(context.Background(), testHub()); err != nil {
		t.Fatalf("UpsertHub: %v", err)
	}
	w := newTestWorldOver(t, refusingStore{fake}, newFakeForum(t))
	signIn(t, w.forum, w.b)
	rec := recordSentry(t)
	form := updateForm(t, fake)
	form.Set("channel_name", "Bravo Room")
	before := readSavedState(t, fake)

	res := w.b.postForm(hubPath(t, fake, "hub-1"), form)

	if !isServerError(res.StatusCode) {
		t.Errorf("status = %d, want 5xx", res.StatusCode)
	}
	if after := readSavedState(t, fake); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the failed save = %+v, want it as before, %+v", after, before)
	}
	ch, err := w.discord.Channel("hub-1")
	if err != nil {
		t.Fatalf("Channel(hub-1): %v", err)
	}
	if ch.Name != "Bravo Room" {
		t.Errorf("hub-1 is named %q, want Bravo Room: the rename stays", ch.Name)
	}
	errs := rec.recorded()
	if len(errs) != 1 {
		t.Fatalf("Sentry got %d events, want 1", len(errs))
	}
	if msg := errs[0].Error(); !strings.Contains(msg, "Join to create") || !strings.Contains(msg, "Bravo Room") {
		t.Errorf("the event's error %q does not name both Join to create and Bravo Room", msg)
	}
}
