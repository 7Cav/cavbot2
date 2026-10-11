package panel

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
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
