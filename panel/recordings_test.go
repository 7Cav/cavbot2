package panel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
	"golang.org/x/net/html"
)

// The Recordings page (#390). Every recording a case reads is made by the
// real recording runtime over the panel's own store and recordings
// directory: a fake voice connection hands it a frame, and a fake mixer
// writes known bytes as the mix. No recording's files or row are written by
// hand. Who a signed-in forum user is in Discord comes from the 7Cav API's
// milpac for their forum username, which a test server answers.

// milpacProfilePath is where the 7Cav API answers a forum username's
// milpac.
const milpacProfilePath = "/milpacs/profile/username/"

// milpacAPI plays the 7Cav API's milpac lookup by forum username: each
// username it knows answers a milpac carrying its Discord ID, a username
// given a status answers that status, and any other answers 404.
type milpacAPI struct {
	mu         sync.Mutex
	discordIDs map[string]string
	statuses   map[string]int
}

// serveMilpacs points the 7Cav API at a milpac lookup that knows no one yet,
// for the rest of the test.
func serveMilpacs(t *testing.T) *milpacAPI {
	t.Helper()
	api := &milpacAPI{discordIDs: map[string]string{}, statuses: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
	return api
}

func (a *milpacAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	username, ok := strings.CutPrefix(r.URL.Path, milpacProfilePath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if status, ok := a.statuses[username]; ok {
		w.WriteHeader(status)
		return
	}
	id, ok := a.discordIDs[username]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(utils.ProfileResponse{User: utils.User{Username: username}, DiscordID: id})
}

// set makes a forum username's milpac carry the Discord ID given.
func (a *milpacAPI) set(username, discordID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discordIDs[username] = discordID
}

// fail makes the lookup of a forum username answer the status given.
func (a *milpacAPI) fail(username string, status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statuses[username] = status
}

// The fixture's recording setup: the recorder, the channel recorded, which
// is not a hub, and the recording role every starter holds.
const (
	testRecorder      = "user-recorder"
	testRecordChannel = "vc-2"
	testRecordingRole = "role-recording"
)

// testMix is what the fake mixer writes as every recording's mix.
var testMix = []byte("the fake mixer's mix")

// starter is a forum user who starts recordings: the account the fake forum
// signs them in with, and the Discord ID their milpac carries.
type starter struct {
	account   *forumAccount
	discordID string
}

// addStarter makes the forum know an outsider whose milpac carries the
// Discord ID given.
func (w *testWorld) addStarter(userID int, username, discordID string) starter {
	a := w.forum.addUser(userID, username, 2, []int{35})
	w.milpacs.set(username, discordID)
	return starter{account: a, discordID: discordID}
}

// fakeRecorders stands in for the voice adapter: one recorder, and the
// connection of its latest join.
type fakeRecorders struct {
	mu   sync.Mutex
	conn *fakeVoiceConn
}

func (v *fakeRecorders) UserIDs() []string { return []string{testRecorder} }

func (v *fakeRecorders) Join(context.Context, string, string, string) (voice.Conn, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.conn = &fakeVoiceConn{}
	return v.conn, nil
}

// hear hands a frame to the runtime through the latest join's connection,
// the way the voice adapter delivers one.
func (v *fakeRecorders) hear(t *testing.T, f voice.Frame) {
	t.Helper()
	v.mu.Lock()
	conn := v.conn
	v.mu.Unlock()
	conn.mu.Lock()
	handle := conn.handle
	conn.mu.Unlock()
	if handle == nil {
		t.Fatal("the recorder is not receiving")
	}
	handle(f)
}

// fakeVoiceConn is the recorder in a channel. handle is what the runtime
// passed to Receive.
type fakeVoiceConn struct {
	mu     sync.Mutex
	handle func(voice.Frame)
}

func (c *fakeVoiceConn) Leave(context.Context) {}

func (c *fakeVoiceConn) Receive(handle func(voice.Frame)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handle = handle
}

// fakeMixer stands in for ffmpeg: every mix is testMix.
type fakeMixer struct{}

func (fakeMixer) Mix(_ context.Context, _ []string, out string) error {
	return os.WriteFile(out, testMix, 0o600)
}

// mixWatch is the store with a signal each time the runtime writes a
// recording's mix state, so a test knows the mix has landed with no sleep.
type mixWatch struct {
	store.Store
	mixed chan struct{}
}

func (s mixWatch) SetRecordingMix(ctx context.Context, id int64, mix store.MixState) error {
	err := s.Store.SetRecordingMix(ctx, id, mix)
	if err == nil {
		s.mixed <- struct{}{}
	}
	return err
}

// recordingRig is the real recording runtime over the world's store and
// recordings directory, linking to the test panel.
type recordingRig struct {
	voice *fakeRecorders
	rt    *commands.RecordingRuntime
	mixed chan struct{}
}

// recorder builds the world's recording runtime the first time a test
// records, with the recording role saved.
func (w *testWorld) recorder(t *testing.T) *recordingRig {
	t.Helper()
	if w.rig != nil {
		return w.rig
	}
	ctx := context.Background()
	roles, err := w.store.GetRecordingRoles(ctx, testGuildID)
	if err != nil {
		t.Fatalf("GetRecordingRoles: %v", err)
	}
	entry := store.ChangeLogEntry{ForumUserID: testUserID, ForumUsername: testUsername, Action: store.ChangeRecordingRoles, Diff: json.RawMessage(`{}`)}
	roles.RoleIDs = []string{testRecordingRole}
	if err := w.store.SaveRecordingRoles(ctx, testGuildID, roles, entry); err != nil {
		t.Fatalf("SaveRecordingRoles: %v", err)
	}
	rig := &recordingRig{voice: &fakeRecorders{}, mixed: make(chan struct{}, 16)}
	rig.rt = commands.NewRecordingRuntime(w.discord, mixWatch{Store: w.store, mixed: rig.mixed}, w.runtime, testGuildID,
		rig.voice, fakeMixer{}, w.recordingsDir, testBaseURL)
	w.rig = rig
	return rig
}

// record has the member with the Discord ID given start a recording in the
// fixture's channel, say one frame and stop it, and waits for its mix. It
// returns the recording's ID.
func (w *testWorld) record(t *testing.T, discordID string) int64 {
	t.Helper()
	rig := w.recorder(t)
	by := commands.Invoker{UserID: discordID, Username: "tester", Roles: []string{testRecordingRole, testRankSGT}}
	w.discord.setVoice(discordID, testRecordChannel)
	if _, err := rig.rt.Start(by, ""); err != nil {
		t.Fatalf("starting a recording as %s: %v", discordID, err)
	}
	rig.voice.hear(t, voice.Frame{UserID: discordID, SSRC: 1, Opus: []byte{0xF8, 'x'}})
	if _, err := rig.rt.Stop(by); err != nil {
		t.Fatalf("stopping %s's recording: %v", discordID, err)
	}
	w.discord.setVoice(discordID, "")
	select {
	case <-rig.mixed:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s's recording was never mixed", discordID)
	}
	return newestRecordingOf(t, w.store, discordID).ID
}

// newestRecordingOf is the latest recording the member with the Discord ID
// given started, as the store holds it.
func newestRecordingOf(t *testing.T, st store.Store, discordID string) store.Recording {
	t.Helper()
	recs, err := st.ListRecordings(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	recs = slices.DeleteFunc(recs, func(rec store.Recording) bool { return rec.StarterID != discordID })
	if len(recs) == 0 {
		t.Fatalf("the store holds no recording %s started", discordID)
	}
	return slices.MaxFunc(recs, func(a, b store.Recording) int { return int(a.ID - b.ID) })
}

// listedRecordings maps each recording a page lists, by ID, to its element.
func listedRecordings(t *testing.T, doc *html.Node) map[int64]*html.Node {
	t.Helper()
	out := map[int64]*html.Node{}
	eachElement(doc, func(n *html.Node) {
		raw, ok := attrValue(n, "data-recording")
		if !ok {
			return
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("data-recording=%q is not a recording ID", raw)
		}
		out[id] = n
	})
	return out
}

// With two starters, A signs in and lands on the Recordings page, which
// lists A's recording and not B's.
func TestRecordingsPageListsTheStartersOwnRecordingsOnly(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	b := w.addStarter(5002, "Baker.B", "discord-b")
	recA := w.record(t, a.discordID)
	recB := w.record(t, b.discordID)

	doc := parseHTML(t, follow(t, w.b, signInAs(t, w.forum, w.b, a.account)))

	if got := pageOf(t, doc); got != pageRecordings {
		t.Fatalf("A landed on page %q, want %s", got, pageRecordings)
	}
	listed := listedRecordings(t, doc)
	if _, ok := listed[recA]; !ok {
		t.Errorf("A's page lists recordings %v, want A's %d among them", slices.Sorted(maps.Keys(listed)), recA)
	}
	if _, ok := listed[recB]; ok {
		t.Errorf("A's page lists B's recording %d", recB)
	}
}

// viewAllHref is the address the Recordings page's switch to every
// recording leads to, and fails the test when the page carries none.
func viewAllHref(t *testing.T, doc *html.Node) string {
	t.Helper()
	link := findElement(doc, "a", "data-field", "view-all")
	if link == nil {
		t.Fatal("the Recordings page has no switch to every recording")
	}
	href, _ := attrValue(link, "href")
	return href
}

// A panel admin sees their own recordings by default. The switch the page
// carries shows every recording, each with its starter.
func TestRecordingsPageSwitchShowsAPanelAdminEveryRecordingWithItsStarter(t *testing.T) {
	w := newTestWorld(t)
	w.milpacs.set(testUsername, "discord-admin")
	b := w.addStarter(5002, "Baker.B", "discord-b")
	own := w.record(t, "discord-admin")
	other := w.record(t, b.discordID)
	signIn(t, w.forum, w.b)

	doc := parseHTML(t, w.b.get(recordingsPath))

	if got := slices.Sorted(maps.Keys(listedRecordings(t, doc))); !slices.Equal(got, []int64{own}) {
		t.Errorf("the admin's default view lists recordings %v, want their own %d alone", got, own)
	}
	all := listedRecordings(t, parseHTML(t, w.b.get(viewAllHref(t, doc))))
	for id, starterID := range map[int64]string{own: "discord-admin", other: b.discordID} {
		row, ok := all[id]
		if !ok {
			t.Errorf("the all view doesn't list recording %d", id)
			continue
		}
		if got := valuesOf(row, "data-starter"); !slices.Equal(got, []string{starterID}) {
			t.Errorf("recording %d's starter in the all view = %v, want %s", id, got, starterID)
		}
	}
}

// The switch's address, opened by a starter who isn't a panel admin, shows
// them the Recordings page with their own recordings and nobody else's.
func TestRecordingsAllViewOpenedByAStarterListsTheirOwnOnly(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	b := w.addStarter(5002, "Baker.B", "discord-b")
	recA := w.record(t, a.discordID)
	recB := w.record(t, b.discordID)
	signIn(t, w.forum, w.b)
	href := viewAllHref(t, parseHTML(t, w.b.get(recordingsPath)))
	starterA := newBrowser(t, w.p)
	signInAs(t, w.forum, starterA, a.account)

	doc := parseHTML(t, starterA.get(href))

	if got := pageOf(t, doc); got != pageRecordings {
		t.Fatalf("A opening the all view landed on page %q, want %s", got, pageRecordings)
	}
	listed := listedRecordings(t, doc)
	if _, ok := listed[recA]; !ok {
		t.Errorf("A's all view lists recordings %v, want A's %d among them", slices.Sorted(maps.Keys(listed)), recA)
	}
	if _, ok := listed[recB]; ok {
		t.Errorf("A's all view lists B's recording %d", recB)
	}
}

// recordingRoute is one route a recording's starter and panel admins reach:
// how to read its address for a recording off the Recordings page the
// starter sees, and the check that a response is what the route serves.
type recordingRoute struct {
	name   string
	href   func(t *testing.T, page *html.Node, id int64) string
	served func(t *testing.T, who string, res *http.Response, id int64)
}

// downloadHref reads the address of a download link, by its data-field,
// off a recording's block on the Recordings page.
func downloadHref(field string) func(t *testing.T, page *html.Node, id int64) string {
	return func(t *testing.T, page *html.Node, id int64) string {
		t.Helper()
		row, ok := listedRecordings(t, page)[id]
		if !ok {
			t.Fatalf("the Recordings page doesn't list recording %d", id)
		}
		link := findElement(row, "a", "data-field", field)
		if link == nil {
			t.Fatalf("recording %d's block has no %s link", id, field)
		}
		href, _ := attrValue(link, "href")
		return href
	}
}

// readBody reads a response's whole body.
func readBody(t *testing.T, res *http.Response) []byte {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

// recordingRoutes is a case for each route that serves one recording: its
// page, the notice link's target, and its two downloads.
var recordingRoutes = []recordingRoute{
	{
		name: "page",
		href: func(_ *testing.T, _ *html.Node, id int64) string { return commands.RecordingPath(id) },
		served: func(t *testing.T, who string, res *http.Response, id int64) {
			t.Helper()
			doc := parseHTML(t, res)
			if got := pageOf(t, doc); got != pageRecordings {
				t.Errorf("%s opened recording %d's page and got page %q, want %s", who, id, got, pageRecordings)
			}
			if _, ok := listedRecordings(t, doc)[id]; !ok {
				t.Errorf("recording %d's page, as %s sees it, doesn't show it", id, who)
			}
		},
	},
	{
		name: "mix",
		href: downloadHref("mix-download"),
		served: func(t *testing.T, who string, res *http.Response, id int64) {
			t.Helper()
			if body := readBody(t, res); res.StatusCode != http.StatusOK || !bytes.Equal(body, testMix) {
				t.Errorf("%s downloading recording %d's mix got %d with %q, want 200 with the mixer's %q", who, id, res.StatusCode, body, testMix)
			}
		},
	},
	{
		name: "zip",
		href: downloadHref("zip-download"),
		served: func(t *testing.T, who string, res *http.Response, id int64) {
			t.Helper()
			body := readBody(t, res)
			zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if res.StatusCode != http.StatusOK || err != nil || len(zr.File) == 0 {
				t.Errorf("%s downloading recording %d's zip got %d, a zip read error %v, want 200 with a zip holding its files", who, id, res.StatusCode, err)
			}
		},
	},
}

// assertRecordingRefused checks a response to a recording's route is the
// no-access page, and carries neither the mix nor a zip.
func assertRecordingRefused(t *testing.T, res *http.Response) {
	t.Helper()
	body := readBody(t, res)
	if bytes.Equal(body, testMix) {
		t.Fatal("the refused request got the mix")
	}
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err == nil {
		t.Fatal("the refused request got a zip")
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	assertNoAccessPage(t, doc)
}

// A recording's page and its downloads are served to its starter and to a
// panel admin, and refused to anyone else, here a starter of another
// recording.
func TestRecordingIsServedToItsStarterAndPanelAdminsOnly(t *testing.T) {
	for _, route := range recordingRoutes {
		t.Run(route.name, func(t *testing.T) {
			w := newTestWorld(t)
			a := w.addStarter(5001, "Able.A", "discord-a")
			b := w.addStarter(5002, "Baker.B", "discord-b")
			recA := w.record(t, a.discordID)
			w.record(t, b.discordID)
			signInAs(t, w.forum, w.b, a.account)
			href := route.href(t, parseHTML(t, w.b.get(recordingsPath)), recA)
			admin := newBrowser(t, w.p)
			signIn(t, w.forum, admin)
			other := newBrowser(t, w.p)
			signInAs(t, w.forum, other, b.account)

			route.served(t, "the starter", w.b.get(href), recA)
			route.served(t, "a panel admin", admin.get(href), recA)
			assertRecordingRefused(t, other.get(href))
		})
	}
}

// A starter whose milpac lookup fails at sign-in, or finds no milpac, still
// signs in. Their Recordings page is the no-access page, not the error
// page, and doesn't list the recording they started.
func TestRecordingsPageAfterAFailedMilpacLookupIsTheNoAccessPage(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			w := newTestWorld(t)
			x := w.addStarter(5003, "Xray.X", "discord-x")
			rec := w.record(t, x.discordID)
			w.milpacs.fail("Xray.X", status)

			res := signInAs(t, w.forum, w.b, x.account)

			assertRedirect(t, res, "/")
			doc := parseHTML(t, follow(t, w.b, res))
			if m := findElement(doc, "main", "", ""); m != nil {
				if failure, ok := attrValue(m, "data-failure"); ok {
					t.Errorf("X got the error page, data-failure=%q, want the no-access page", failure)
				}
			}
			assertNoAccessPage(t, doc)
			if _, ok := listedRecordings(t, doc)[rec]; ok {
				t.Errorf("X's page lists their recording %d, which the panel couldn't know was theirs", rec)
			}
		})
	}
}

// panelLink matches a link into the test panel.
var panelLink = regexp.MustCompile(regexp.QuoteMeta(testBaseURL) + `[^\s<>]*`)

// The link a stop puts on the recording notice, opened by the starter,
// shows that recording.
func TestRecordingNoticeLinkOpensTheRecordingForItsStarter(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	edits := w.discord.editedContents()
	if len(edits) == 0 {
		t.Fatal("the stop edited no message")
	}
	link := panelLink.FindString(edits[len(edits)-1])
	if link == "" {
		t.Fatalf("the notice edit %q carries no link into the panel", edits[len(edits)-1])
	}
	signInAs(t, w.forum, w.b, a.account)

	doc := parseHTML(t, w.b.get(strings.TrimPrefix(link, testBaseURL)))

	if got := pageOf(t, doc); got != pageRecordings {
		t.Fatalf("the link %s lands on page %q, want %s", link, got, pageRecordings)
	}
	if _, ok := listedRecordings(t, doc)[rec]; !ok {
		t.Errorf("the link %s doesn't show recording %d", link, rec)
	}
}

// parseDatetime reads a <time> element's datetime as an instant, in either
// form HTML gives a date: a full RFC 3339 time or a date alone.
func parseDatetime(t *testing.T, el *html.Node) time.Time {
	t.Helper()
	raw, _ := attrValue(el, "datetime")
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if at, err := time.Parse(layout, raw); err == nil {
			return at
		}
	}
	t.Fatalf("datetime=%q is neither an RFC 3339 time nor a date", raw)
	return time.Time{}
}

// The page shows a stopped recording's deletion date: 30 days after it
// stopped, as the recording notice promises.
func TestRecordingsPageShowsTheDeletionDateThirtyDaysAfterTheStop(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	stopped := newestRecordingOf(t, w.store, a.discordID).StoppedAt
	signInAs(t, w.forum, w.b, a.account)

	row, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]

	if !ok {
		t.Fatalf("the page doesn't list recording %d", rec)
	}
	el := findElement(row, "time", "data-field", "deletes")
	if el == nil {
		t.Fatalf("recording %d's block shows no deletion date", rec)
	}
	want := stopped.Add(30 * 24 * time.Hour).UTC().Format(time.DateOnly)
	if got := parseDatetime(t, el).UTC().Format(time.DateOnly); got != want {
		t.Errorf("recording %d is deleted on %s, want %s, 30 days after its stop at %v", rec, got, want, stopped)
	}
}
