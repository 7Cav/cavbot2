package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// --- Spawn in flight and the restart sweep (#325, #335): a voice handler
// that runs while the sweep lists the stored rows must not be lost to the
// sweep's rebuild. The replays here land events inside that window through
// a store wrapper whose list call runs a test hook. In-order events go
// through fake.deliver, which applies each to the fake cache first, the
// order discordgo keeps. ---

// windowStore wraps a Fake and runs a hook around its list call, on the
// sweep's goroutine, so a test can land a gateway event inside the sweep's
// window. Both hooks sit inside that window: beforeList runs before the
// rows are read, afterList once they are. Each gets the call number, from 1.
// The runtime alone reads through the wrapper; a test reads the rows from
// the Fake beneath, so its own reads fire no hook.
type windowStore struct {
	*store.Fake
	beforeList func(n int)
	afterList  func(n int)
	// failList, when set, is the error the list returns instead of the
	// rows, by call number; nil lets the call through.
	failList func(n int) error
	lists    int
}

func (w *windowStore) ListSpawnedChannels(ctx context.Context) ([]store.SpawnedChannel, error) {
	w.lists++
	n := w.lists
	if w.beforeList != nil {
		w.beforeList(n)
	}
	if w.failList != nil {
		if err := w.failList(n); err != nil {
			return nil, err
		}
	}
	rows, err := w.Fake.ListSpawnedChannels(ctx)
	if w.afterList != nil {
		w.afterList(n)
	}
	return rows, err
}

// newWindowedTempVC is the one-hub fixture for the replays here: the fake
// manager, the test hub stored behind a windowStore, and the runtime over
// both.
func newWindowedTempVC(t *testing.T) (*fakeTempVCManager, *windowStore, *TempVC) {
	t.Helper()
	fake := newFakeTempVCManager()
	st := &windowStore{Fake: seedStore(t, testHub())}
	return fake, st, newTestTempVC(t, fake, st)
}

// seedRow stores one spawned channel row on the test hub.
func seedRow(t *testing.T, st *store.Fake, channelID string, number int, owner string) {
	t.Helper()
	row := store.SpawnedChannel{ChannelID: channelID, HubID: storedHubID(t, st), Number: number, OwnerUserID: owner}
	if err := st.UpsertSpawnedChannel(context.Background(), row); err != nil {
		t.Fatalf("UpsertSpawnedChannel: %v", err)
	}
}

// sweepPayload builds the GUILD_CREATE for the test guild with the hub and
// the given spawned channels present, and the given members inside them.
func sweepPayload(channels []string, voice map[string]string) *discordgo.GuildCreate {
	chans := []*discordgo.Channel{voiceChannel(testTempVCHub, testTempVCCategory, "Hub")}
	for _, id := range channels {
		chans = append(chans, voiceChannel(id, testTempVCCategory, id))
	}
	var states []*discordgo.VoiceState
	for user, ch := range voice {
		states = append(states, &discordgo.VoiceState{UserID: user, ChannelID: ch})
	}
	return guildCreate(chans, states...)
}

// Sequence 3: B joins A's channel while the sweep lists the rows. The
// payload predates B, so a sweep that re-seeded from it would count one
// member too few: A's leave would be refused by the cache and B's leave
// would not be attributed to the channel, which would then linger empty.
func TestTempVCJoinInsideTheSweepWindowCountsInOccupancy(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	a, b := member("A", testRankSGT), member("B")
	spawnInto(tv, fake, "user-a", "chan-x", a)
	st.afterList = func(int) { fake.deliver(tv, voiceEvent("user-b", "chan-x", b)) }

	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x"}, map[string]string{"user-a": "chan-x"}))

	fake.deliver(tv, voiceEvent("user-a", "", a))
	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts after A left = %d, want 0: B is inside", n)
	}
	fake.deliver(tv, voiceEvent("user-b", "", b))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "chan-x" {
		t.Errorf("deleted after B left = %v, want chan-x: the last occupant left", ids)
	}
	if _, ok := rowFor(t, st.Fake, "chan-x"); ok {
		t.Error("row for chan-x still stored after the delete")
	}
}

// Sequence 4: B leaves A's channel while the sweep lists the rows. The
// payload still holds B, so a sweep that took the union of the payload and
// the cache would count one member too many, and A's leave would not empty
// the channel. Occupancy comes from the cache alone.
func TestTempVCLeaveInsideTheSweepWindowCountsInOccupancy(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	a, b := member("A", testRankSGT), member("B")
	spawnInto(tv, fake, "user-a", "chan-x", a)
	fake.deliver(tv, voiceEvent("user-b", "chan-x", b))
	st.afterList = func(int) { fake.deliver(tv, voiceEvent("user-b", "", b)) }

	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x"}, map[string]string{"user-a": "chan-x", "user-b": "chan-x"}))

	fake.deliver(tv, voiceEvent("user-a", "", a))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "chan-x" {
		t.Errorf("deleted after A left = %v, want chan-x: B had already left", ids)
	}
	if _, ok := rowFor(t, st.Fake, "chan-x"); ok {
		t.Error("row for chan-x still stored after the delete")
	}
}

// Sequence 1: A joins the hub while the sweep lists the rows, and the
// spawn's row is written after the list returned. The fresh channel is a
// spawn in flight: the sweep leaves it alone, so it keeps its number, its
// creator's leave deletes it with its row, and the sweep's line counts it
// as protected and as tracked although no row was listed.
func TestTempVCHubJoinInsideTheSweepWindowStaysTracked(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	logs := captureLogs(t)
	a, b := member("A", testRankSGT), member("B")
	st.afterList = func(int) { fake.deliver(tv, voiceEvent("user-a", testTempVCHub, a)) }

	fake.deliverGuildCreate(tv, sweepPayload(nil, nil))

	if owner, tracked := tv.Owner("new-chan"); !tracked || owner != "user-a" {
		t.Errorf("Owner(new-chan) after the sweep = %q, %v, want user-a, tracked", owner, tracked)
	}
	if lines := logRecordsWith(t, logs, "INFO", map[string]string{"rows": "0", "protected": "1", "tracked": "1"}); len(lines) != 1 {
		t.Errorf("sweep lines with rows=0 protected=1 tracked=1 = %d, want one", len(lines))
	}

	// The spawn in flight holds number 1, so the next spawn is 2.
	fake.setNextChannel("chan-2")
	fake.deliver(tv, voiceEvent("user-b", testTempVCHub, b))
	if names := fake.createdNames(); len(names) != 2 || names[1] != "Voice - 2" {
		t.Errorf("created = %v, want Voice - 2 second: number 1 is held by the spawn in flight", names)
	}

	// The creator's move-into lands, then they leave: the channel goes
	// with its row.
	fake.deliver(tv, voiceEvent("user-a", "new-chan", a))
	fake.deliver(tv, voiceEvent("user-a", "", a))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted = %v, want new-chan once its creator left", ids)
	}
	if _, ok := rowFor(t, st.Fake, "new-chan"); ok {
		t.Error("row for new-chan still stored after the delete")
	}
}

// Sequence 2: A joins the hub while the sweep runs and the spawn's row is
// written before the rows are read, but the fresh channel has not reached
// the cache, so the sweep's gone test would take the row for a channel that
// no longer exists. The row belongs to a spawn in flight: the sweep skips
// it, and the channel goes with its row when its creator leaves.
func TestTempVCRowOfASpawnInFlightIsNotJudgedGone(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	a := member("A", testRankSGT)
	st.beforeList = func(int) { fake.deliver(tv, voiceEvent("user-a", testTempVCHub, a)) }

	fake.deliverGuildCreate(tv, sweepPayload(nil, nil))

	if row, ok := rowFor(t, st.Fake, "new-chan"); !ok || row.OwnerUserID != "user-a" {
		t.Errorf("row after the sweep = %+v (present %v), want kept with owner user-a", row, ok)
	}

	fake.deliver(tv, voiceEvent("user-a", "new-chan", a))
	fake.deliver(tv, voiceEvent("user-a", "", a))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted = %v, want new-chan once its creator left", ids)
	}
	if _, ok := rowFor(t, st.Fake, "new-chan"); ok {
		t.Error("row for new-chan still stored after the delete")
	}
}

// A hub join inside the window whose move-into is abandoned. When the
// compensating delete goes through, the sweep meets a marker for a channel
// nothing tracks: one delete in total, no row, nothing tracked, protected
// zero. When it is refused because another member walked in, the channel
// stays tracked with its row, and the marker ends with the sweep: the next
// sweep judges the row like any other.
func TestTempVCCompensatingDeleteInsideTheSweepWindow(t *testing.T) {
	t.Run("goes through", func(t *testing.T) {
		fake, st, tv := newWindowedTempVC(t)
		logs := captureLogs(t)
		a := member("A", testRankSGT)
		fake.createHook = func() { fake.setVoice("user-a", "lobby") }
		st.afterList = func(int) { fake.deliver(tv, voiceEvent("user-a", testTempVCHub, a)) }

		fake.deliverGuildCreate(tv, sweepPayload(nil, nil))

		if n := fake.deleteCallCount(); n != 1 {
			t.Errorf("delete attempts = %d, want the compensating delete alone", n)
		}
		if _, tracked := tv.Owner("new-chan"); tracked {
			t.Error("new-chan is tracked after the sweep, want untracked: its compensating delete went through")
		}
		if rows := spawnedRows(t, st.Fake); len(rows) != 0 {
			t.Errorf("rows = %+v, want none", rows)
		}
		if lines := logRecordsWith(t, logs, "INFO", map[string]string{"rows": "0", "protected": "0"}); len(lines) != 1 {
			t.Errorf("sweep lines with rows=0 protected=0 = %d, want one: a marker for an untracked channel protects nothing", len(lines))
		}
	})

	t.Run("is refused", func(t *testing.T) {
		fake, st, tv := newWindowedTempVC(t)
		a, b := member("A", testRankSGT), member("B", testRankPVT)
		fake.moveErr = restError(http.StatusBadRequest, discordgo.ErrCodeTargetIsNotConnectedToVoice, "Target user is not connected to voice")
		fake.moveHook = func() {
			fake.deliver(tv, voiceEvent("user-a", "", a))
			fake.deliver(tv, voiceEvent("user-b", "new-chan", b))
		}
		st.beforeList = func(int) { fake.deliver(tv, voiceEvent("user-a", testTempVCHub, a)) }

		fake.deliverGuildCreate(tv, sweepPayload(nil, nil))

		if n := fake.deleteCallCount(); n != 0 {
			t.Errorf("delete attempts = %d, want 0: B is inside", n)
		}
		if owner, tracked := tv.Owner("new-chan"); !tracked || owner != "user-b" {
			t.Errorf("Owner(new-chan) after the sweep = %q, %v, want user-b, tracked", owner, tracked)
		}
		if row, ok := rowFor(t, st.Fake, "new-chan"); !ok || row.OwnerUserID != "user-b" {
			t.Errorf("row after the sweep = %+v (present %v), want kept with owner user-b", row, ok)
		}

		// The next connect finds B gone and the channel deleted by hand: the
		// row is gone like any other, because the spawn settled.
		st.beforeList = nil
		fake.deliverGuildCreate(tv, sweepPayload(nil, nil))
		if _, ok := rowFor(t, st.Fake, "new-chan"); ok {
			t.Error("row for new-chan survived a sweep that found the channel gone: the marker outlived its spawn")
		}
		if _, tracked := tv.Owner("new-chan"); tracked {
			t.Error("new-chan is tracked after the second sweep, want untracked")
		}
	})
}

// A member who sat in the hub since before the connect is not seeded into
// the record, so their next voice event in the hub, a mute toggle here,
// reads as a join once and spawns.
func TestTempVCHubOccupantAtTheConnectSpawnsOnTheirNextEvent(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	fake.deliverGuildCreate(tv, sweepPayload(nil, map[string]string{"user-a": testTempVCHub}))

	fake.deliver(tv, voiceEvent("user-a", testTempVCHub, member("A", testRankSGT)))

	if creates := fake.recordedCreates(); len(creates) != 1 {
		t.Fatalf("created %d channels for a hub occupant's toggle, want 1", len(creates))
	}
	if moves := fake.recordedMoves(); len(moves) != 1 || moves[0].userID != "user-a" || *moves[0].channelID != "new-chan" {
		t.Errorf("moves = %+v, want user-a moved into new-chan", moves)
	}
}

// awaitClosed waits for a channel a goroutine closes when it is done, and
// fails the test rather than hanging when it never closes.
func awaitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish within 5s", what)
	}
}

// Two GUILD_CREATEs in a row, the second carrying fresher ranks: X was
// promoted between them. Sweep 1 blocks in its list call and sweep 2 is
// started behind it. The sweeps serialize: sweep 2 does not read the rows
// while sweep 1 holds the sweep, and its payload is applied after sweep
// 1's, so the owner's leave hands the channel to X, the CPT of the later
// payload, not to Y. The bounded wait is what detects an unserialized
// sweep 2: the rank assertion alone lets it through whenever sweep 1
// finishes first, which is most runs.
func TestTempVCSweepsSerialize(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	seedRow(t, st.Fake, "chan-x", 1, "user-a")
	inside := map[string]string{"user-a": "chan-x", "user-x": "chan-x", "user-y": "chan-x"}
	first := sweepPayload([]string{"chan-x"}, inside)
	first.Members = []*discordgo.Member{guildMember("user-a", testRankSGT), guildMember("user-x", testRankPVT), guildMember("user-y", testRankSGT)}
	second := sweepPayload([]string{"chan-x"}, inside)
	second.Members = []*discordgo.Member{guildMember("user-a", testRankSGT), guildMember("user-x", testRankCPT), guildMember("user-y", testRankSGT)}

	inList, release, secondListed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	st.afterList = func(n int) {
		switch n {
		case 1:
			close(inList)
			<-release
		case 2:
			close(secondListed)
		}
	}
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(firstDone)
		fake.deliverGuildCreate(tv, first)
	}()
	awaitClosed(t, inList, "sweep 1's list call")
	go func() {
		defer close(secondDone)
		tv.handleGuildCreate(second)
	}()
	select {
	case <-secondListed:
		t.Fatal("sweep 2 read the rows while sweep 1 held the sweep")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	awaitClosed(t, firstDone, "sweep 1")
	awaitClosed(t, secondDone, "sweep 2")

	fake.deliver(tv, voiceEvent("user-a", "", member("A", testRankSGT)))
	if owner, tracked := tv.Owner("chan-x"); !tracked || owner != "user-x" {
		t.Errorf("Owner(chan-x) after A left = %q, %v, want user-x, tracked: the queued sweep's ranks apply last", owner, tracked)
	}
}

// A sweep whose list fails ends like any other: it releases the sweep, so
// a GUILD_CREATE queued behind it runs in full, and it drops the active
// flag, so a spawn that settles after it keeps no marker for the next
// sweep to skip.
func TestTempVCFailedListEndsTheSweep(t *testing.T) {
	t.Run("a queued sweep runs after it", func(t *testing.T) {
		fake, st, tv := newWindowedTempVC(t)
		seedRow(t, st.Fake, "chan-e", 1, "")
		countCaptures(t)
		inList, release := make(chan struct{}), make(chan struct{})
		st.failList = func(n int) error {
			if n != 1 {
				return nil
			}
			close(inList)
			<-release
			return errors.New("connection refused")
		}
		payload := sweepPayload([]string{"chan-e"}, nil)

		firstDone, secondDone := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(firstDone)
			fake.deliverGuildCreate(tv, payload)
		}()
		awaitClosed(t, inList, "sweep 1's list call")
		go func() {
			defer close(secondDone)
			tv.handleGuildCreate(payload)
		}()
		close(release)
		awaitClosed(t, firstDone, "sweep 1")
		awaitClosed(t, secondDone, "sweep 2")

		if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "chan-e" {
			t.Errorf("deleted = %v, want chan-e: the queued sweep found it empty", ids)
		}
		if rows := spawnedRows(t, st.Fake); len(rows) != 0 {
			t.Errorf("rows = %+v, want none after the queued sweep", rows)
		}
	})

	t.Run("a spawn after it settles at once", func(t *testing.T) {
		fake, st, tv := newWindowedTempVC(t)
		countCaptures(t)
		st.failList = func(n int) error {
			if n == 1 {
				return errors.New("connection refused")
			}
			return nil
		}
		fake.deliverGuildCreate(tv, sweepPayload(nil, nil))

		a := member("A", testRankSGT)
		spawnInto(tv, fake, "user-a", "new-chan", a)

		// The next connect finds the channel gone: its row goes with it,
		// because no marker outlived the failed sweep.
		fake.deliverGuildCreate(tv, sweepPayload(nil, nil))
		if _, ok := rowFor(t, st.Fake, "new-chan"); ok {
			t.Error("row for new-chan survived a sweep that found the channel gone: a marker outlived the failed sweep")
		}
		if _, tracked := tv.Owner("new-chan"); tracked {
			t.Error("new-chan is tracked after the sweep, want untracked")
		}
	})
}

// A row-backed channel whose CHANNEL_DELETE reached the cache inside the
// window, before its handler ran, while the payload still lists it. The
// gone test reads the cache, so the sweep makes no delete call for a
// channel that is already gone and drops the row.
func TestTempVCSweepGoneTestReadsTheCache(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	seedRow(t, st.Fake, "chan-g", 1, "")
	st.afterList = func(int) { fake.dropChannel("chan-g") }

	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-g"}, nil))

	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts = %d, want 0: the cache shows the channel gone", n)
	}
	if rows := spawnedRows(t, st.Fake); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: a gone channel loses its row", rows)
	}
	if _, tracked := tv.Owner("chan-g"); tracked {
		t.Error("chan-g is tracked after the sweep, want untracked")
	}
}

// roundTripperFunc adapts a function to http.RoundTripper, so a session
// under test can refuse every REST call without touching the network.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sessionGuildCreate builds a GUILD_CREATE for a real session's state, the
// guild with a category, the hub and one spawned channel, members holding
// rank roles, and one member in the spawned channel. discordgo stores the
// payload pointer, so its slices are the state's from then on.
func sessionGuildCreate() *discordgo.GuildCreate {
	return &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: "g",
		Channels: []*discordgo.Channel{
			{ID: "cat", GuildID: "g", Type: discordgo.ChannelTypeGuildCategory},
			{ID: "hub", GuildID: "g", ParentID: "cat", Type: discordgo.ChannelTypeGuildVoice},
			{ID: "chan-p", GuildID: "g", ParentID: "cat", Type: discordgo.ChannelTypeGuildVoice},
		},
		Members: []*discordgo.Member{
			guildMember("user-a", testRankSGT),
			guildMember("user-b", testRankPVT),
			guildMember("user-c"),
		},
		VoiceStates: []*discordgo.VoiceState{{GuildID: "g", UserID: "user-a", ChannelID: "chan-p"}},
	}}
}

// The sweep over the production adapter, racing discordgo's own writes.
// discordgo shares the GUILD_CREATE payload's slices with its state cache
// and writes into them under State.Lock: a voice event replaces or removes
// an element, CHANNEL_CREATE appends a channel, and a member update writes
// over the shared member. The sweep must read none of them without the
// state's lock, and -race is the assertion. Every REST call the session
// might make is refused at the transport, so nothing leaves the process.
func TestTempVCSweepOverTheSessionAdapterRacesTheCacheCleanly(t *testing.T) {
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	dg.Client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no network in tests")
	})}
	payload := sessionGuildCreate()
	feed(t, dg, payload)
	st := store.NewFake()
	if err := st.UpsertSpawnedChannel(context.Background(), store.SpawnedChannel{ChannelID: "chan-gone", HubID: 1, Number: 1}); err != nil {
		t.Fatalf("UpsertSpawnedChannel: %v", err)
	}
	tv, err := NewTempVC(NewSessionTempVCManager(dg), st, "g")
	if err != nil {
		t.Fatalf("NewTempVC: %v", err)
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		users := []string{"user-a", "user-b", "user-c"}
		for i := 0; i < 200; i++ {
			user := users[i%len(users)]
			for _, channel := range []string{"chan-p", "hub", ""} {
				vs := sessionVoiceEvent("g", user, channel)
				vs.Member = guildMember(user, testRankCPT)
				if err := dg.State.OnInterface(dg, vs); err != nil {
					t.Errorf("State.OnInterface(voice): %v", err)
					return
				}
			}
			if err := dg.State.OnInterface(dg, &discordgo.ChannelCreate{Channel: &discordgo.Channel{
				ID: fmt.Sprintf("chan-%d", i), GuildID: "g", ParentID: "cat", Type: discordgo.ChannelTypeGuildVoice,
			}}); err != nil {
				t.Errorf("State.OnInterface(channel): %v", err)
				return
			}
			if err := dg.State.OnInterface(dg, &discordgo.GuildMemberUpdate{Member: &discordgo.Member{
				GuildID: "g", User: &discordgo.User{ID: user}, Roles: []string{testRankCPT},
			}}); err != nil {
				t.Errorf("State.OnInterface(member): %v", err)
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			tv.handleGuildCreate(payload)
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	wg.Wait()

	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the gone row is deleted by every sweep", rows)
	}
}

// A guild missing from the cache at the rebuild, a GUILD_DELETE that landed
// between the payload's cache apply and the sweep, or voice tracking off,
// confirms nothing: no channel can be judged gone or empty. The sweep
// leaves the record and the rows as they are, and the next GUILD_CREATE
// retries.
func TestTempVCSweepWithTheGuildMissingTouchesNothing(t *testing.T) {
	fake, st, tv := newWindowedTempVC(t)
	seedRow(t, st.Fake, "chan-e", 1, "")
	fake.setGuildMissing(true)

	tv.handleGuildCreate(sweepPayload([]string{"chan-e"}, nil))

	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts = %d, want 0: nothing can be confirmed", n)
	}
	if rows := spawnedRows(t, st.Fake); len(rows) != 1 || rows[0].ChannelID != "chan-e" {
		t.Errorf("rows = %+v, want the one row untouched", rows)
	}
}
