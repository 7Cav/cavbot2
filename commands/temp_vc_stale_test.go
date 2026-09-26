package commands

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- Stale voice state (#319): discordgo fills its state cache in gateway
// order before it starts any handler goroutine, and the runtime's own record
// can lag it. Each decision is checked against one copied snapshot of the
// cache. Reversed tests preload the fake cache with the final gateway state
// and deliver the handlers in reverse; in-order tests go through
// fake.deliver, which applies each event to the cache first. ---

// logRecordsWith returns the sink's records at the given level that carry
// every listed field with that value. The brief fixes a line's level and its
// ID fields, not its wording, so a test finds a line by those and never by
// its sentence.
func logRecordsWith(t *testing.T, logs *syncBuffer, level string, fields map[string]string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, record := range decodeLogRecords(t, logs) {
		if record["level"] != level {
			continue
		}
		match := true
		for k, v := range fields {
			if record[k] != v {
				match = false
			}
		}
		if match {
			out = append(out, record)
		}
	}
	return out
}

// readyWith builds the Ready payload Discord sends on connect, whose guild
// list carries an unavailable stub per guild until its GUILD_CREATE lands.
func readyWith(guilds ...*discordgo.Guild) *discordgo.Ready {
	return &discordgo.Ready{Guilds: guilds}
}

// feed applies one gateway payload to a session's state the way discordgo's
// reader does, and fails the test when the state refuses it.
func feed(t *testing.T, dg *discordgo.Session, payload any) {
	t.Helper()
	if err := dg.State.OnInterface(dg, payload); err != nil {
		t.Fatalf("State.OnInterface(%T): %v", payload, err)
	}
}

func sessionVoiceEvent(guildID, userID, channelID string) *discordgo.VoiceStateUpdate {
	return &discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: guildID, UserID: userID, ChannelID: channelID,
	}}
}

func TestSessionTempVCManagerVoiceStatesReadsTheStateCache(t *testing.T) {
	newSession := func(t *testing.T) *discordgo.Session {
		t.Helper()
		dg, err := discordgo.New("Bot test")
		if err != nil {
			t.Fatalf("discordgo.New: %v", err)
		}
		return dg
	}

	t.Run("a present guild gives every connected member's channel", func(t *testing.T) {
		dg := newSession(t)
		feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{ID: "g"}})
		feed(t, dg, sessionVoiceEvent("g", "user-a", "chan-1"))
		feed(t, dg, sessionVoiceEvent("g", "user-b", "chan-2"))
		feed(t, dg, sessionVoiceEvent("g", "user-c", "chan-1"))
		feed(t, dg, sessionVoiceEvent("g", "user-c", ""))

		snap := NewSessionTempVCManager(dg).VoiceStates("g")

		if !snap.Present {
			t.Fatal("snapshot reads the guild as missing, want present")
		}
		want := map[string]string{"user-a": "chan-1", "user-b": "chan-2"}
		if len(snap.ChannelByUser) != len(want) {
			t.Errorf("ChannelByUser = %v, want %v", snap.ChannelByUser, want)
		}
		for user, channel := range want {
			if got := snap.ChannelByUser[user]; got != channel {
				t.Errorf("ChannelByUser[%s] = %q, want %q", user, got, channel)
			}
		}
	})

	// The restart sweep's gone test reads the channel set, so the adapter
	// must fill it: left empty, every row would be judged gone.
	t.Run("a present guild gives the guild's channel set", func(t *testing.T) {
		dg := newSession(t)
		feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{
			ID:       "g",
			Channels: []*discordgo.Channel{{ID: "chan-1", GuildID: "g"}, {ID: "chan-2", GuildID: "g"}},
		}})

		snap := NewSessionTempVCManager(dg).VoiceStates("g")

		if !snap.Present {
			t.Fatal("snapshot reads the guild as missing, want present")
		}
		if len(snap.Channels) != 2 || !snap.hasChannel("chan-1") || !snap.hasChannel("chan-2") {
			t.Errorf("Channels = %v, want chan-1 and chan-2", snap.Channels)
		}
	})

	t.Run("a guild the cache never held is missing", func(t *testing.T) {
		dg := newSession(t)
		feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{ID: "g"}})

		if snap := NewSessionTempVCManager(dg).VoiceStates("other"); snap.Present {
			t.Errorf("snapshot of an unknown guild = %+v, want missing", snap)
		}
	})

	t.Run("an unavailable stub is missing until its GUILD_CREATE lands", func(t *testing.T) {
		dg := newSession(t)
		feed(t, dg, readyWith(&discordgo.Guild{ID: "g", Unavailable: true}))
		mgr := NewSessionTempVCManager(dg)

		if snap := mgr.VoiceStates("g"); snap.Present {
			t.Errorf("snapshot of an unavailable guild = %+v, want missing", snap)
		}

		feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{
			ID:          "g",
			VoiceStates: []*discordgo.VoiceState{{GuildID: "g", UserID: "user-a", ChannelID: "chan-1"}},
		}})

		snap := mgr.VoiceStates("g")
		if !snap.Present || snap.ChannelByUser["user-a"] != "chan-1" {
			t.Errorf("snapshot after GUILD_CREATE = %+v, want present with user-a in chan-1", snap)
		}
	})

	t.Run("a session with voice tracking off reads as missing", func(t *testing.T) {
		dg := newSession(t)
		feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{
			ID:          "g",
			VoiceStates: []*discordgo.VoiceState{{GuildID: "g", UserID: "user-a", ChannelID: "chan-1"}},
		}})
		dg.State.TrackVoice = false

		if snap := NewSessionTempVCManager(dg).VoiceStates("g"); snap.Present {
			t.Errorf("snapshot with TrackVoice off = %+v, want missing: the cache would never move", snap)
		}
	})

	// discordgo's reader applies events one at a time on one goroutine while
	// handlers read on theirs. The adapter reads under the state's lock, so
	// -race is the assertion here: every snapshot is present and names only
	// channels the writer used.
	t.Run("snapshots race a sequential writer cleanly", func(t *testing.T) {
		dg := newSession(t)
		feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{ID: "g"}})
		mgr := NewSessionTempVCManager(dg)
		channels := map[string]bool{"chan-1": true, "chan-2": true}
		users := []string{"user-a", "user-b", "user-c"}

		var wg sync.WaitGroup
		done := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			// t.Fatalf may not be called off the test goroutine, so the
			// writer reports through t.Errorf.
			for i := 0; i < 200; i++ {
				user := users[i%len(users)]
				for _, channel := range []string{"chan-1", "chan-2", ""} {
					if err := dg.State.OnInterface(dg, sessionVoiceEvent("g", user, channel)); err != nil {
						t.Errorf("State.OnInterface: %v", err)
						return
					}
				}
			}
		}()
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					snap := mgr.VoiceStates("g")
					if !snap.Present {
						t.Error("a snapshot read the seeded guild as missing")
						return
					}
					for user, channel := range snap.ChannelByUser {
						if !channels[channel] {
							t.Errorf("snapshot holds %s in %q, which no event named", user, channel)
							return
						}
					}
					select {
					case <-done:
						return
					default:
					}
				}
			}()
		}
		wg.Wait()

		if snap := mgr.VoiceStates("g"); len(snap.ChannelByUser) != 0 {
			t.Errorf("final snapshot = %v, want nobody connected after every leave", snap.ChannelByUser)
		}
	})
}

// Scenario 1 reversed: the member joined the hub and moved on to a channel
// of their own choice at once. The move handler ran first; the hub join
// handler runs now, against a cache that already shows the member in the
// channel they chose. It is stale and spawns nothing.
func TestTempVCReversedHubJoinSpawnsNothing(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	a := member("A", testRankSGT)

	fake.setVoice("user-a", "lobby")
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", "lobby", a))
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))

	if creates := fake.recordedCreates(); len(creates) != 0 {
		t.Errorf("created %d channels for a member who had already left the hub, want 0", len(creates))
	}
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none: the member stays in the channel they chose", moves)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

// Scenario 2 reversed: a member joined a spawned channel and left it at
// once. The leave handler ran first; the join handler runs now, against a
// cache that shows the member gone. It is stale, so occupancy does not end
// one member high, and the channel is deleted when its owner leaves.
func TestTempVCReversedJoinToSpawnedChannelIsDropped(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	a, b := member("A", testRankSGT), member("B")
	spawnInto(tv, fake, "user-a", "new-chan", a)

	fake.setVoice("user-b", "")
	tv.HandleVoiceStateUpdate(voiceEvent("user-b", "", b))
	tv.HandleVoiceStateUpdate(voiceEvent("user-b", "new-chan", b))
	fake.deliver(tv, voiceEvent("user-a", "", a))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted = %v, want new-chan: a ghost occupant must not keep it alive", ids)
	}
	if _, ok := rowFor(t, st, "new-chan"); ok {
		t.Error("row for new-chan still stored after the delete")
	}
}

// The member leaves the hub after the handler passed its entry check and
// before the create goes out. The cache shows it, so no channel is created
// for them and nothing is said in the hub chat.
func TestTempVCMemberGoneBeforeCreateSpawnsNothing(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	fake.channelHook = func() { fake.setVoice("user-a", "lobby") }

	fake.deliver(tv, voiceEvent("user-a", testTempVCHub, member("A", testRankSGT)))

	if creates := fake.recordedCreates(); len(creates) != 0 {
		t.Errorf("created %d channels for a member the cache shows gone from the hub, want 0", len(creates))
	}
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none", moves)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	if msgs := fake.recordedMessages(); len(msgs) != 0 {
		t.Errorf("messages = %+v, want none: nothing failed", msgs)
	}
}

// Check-then-act: the member leaves the hub between the create and the
// move-into. The pre-move check sees it, the move is abandoned, and the
// compensating delete removes the fresh channel. Nothing failed, so nobody
// is messaged and Sentry hears nothing; the abandoned move is one INFO line
// naming the member and the channel.
func TestTempVCMemberGoneBeforeMoveAbandonsAndDeletes(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	captures := countCaptures(t)
	logs := captureLogs(t)
	fake.createHook = func() { fake.setVoice("user-a", "lobby") }

	fake.deliver(tv, voiceEvent("user-a", testTempVCHub, member("A", testRankSGT)))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted = %v, want the fresh channel new-chan", ids)
	}
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none: the member is not in the hub", moves)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	if msgs := fake.recordedMessages(); len(msgs) != 0 {
		t.Errorf("messages = %+v, want none: nothing failed and the member is gone", msgs)
	}
	if *captures != 0 {
		t.Errorf("captures = %d, want 0", *captures)
	}
	if lines := logRecordsWith(t, logs, "INFO", map[string]string{"user_id": "user-a", "channel_id": "new-chan"}); len(lines) != 1 {
		t.Errorf("INFO lines naming user-a and new-chan = %d, want one for the abandoned move", len(lines))
	}
}

// Scenario 3 reversed: the creator joined the hub, moved away by hand, and
// then the bot's move-into landed. The move-into handler ran first; the
// move-away handler runs now, against a cache that shows the creator in the
// fresh channel. It is stale, so the fresh channel is not deleted with the
// creator inside.
func TestTempVCReversedMoveAwayDoesNotDeleteTheFreshChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	a := member("A", testRankSGT)
	fake.deliver(tv, voiceEvent("user-a", testTempVCHub, a))

	fake.setVoice("user-a", "new-chan")
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", "lobby", a))

	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts = %d, want 0: the creator is inside", n)
	}
	if owner, tracked := tv.Owner("new-chan"); !tracked || owner != "user-a" {
		t.Errorf("Owner(new-chan) = %q, %v, want user-a, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "user-a" {
		t.Errorf("row = %+v (present %v), want kept with owner user-a", row, ok)
	}
}

// Scenario 4 reversed, across two members: B joined the owner's channel,
// then the owner A left. A's leave handler ran first, and the record shows
// the channel empty, but the cache shows B inside. The delete is refused,
// the channel stays tracked with its row, and B's join then lands on it and
// takes the handover.
func TestTempVCLeaveHandledBeforeAnEarlierJoinKeepsTheChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	a, b := member("A", testRankSGT), member("B", testRankPVT)
	spawnInto(tv, fake, "user-a", "new-chan", a)

	fake.setVoice("user-a", "")
	fake.setVoice("user-b", "new-chan")
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", "", a))
	tv.HandleVoiceStateUpdate(voiceEvent("user-b", "new-chan", b))

	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts = %d, want 0: B is inside per the cache", n)
	}
	if owner, tracked := tv.Owner("new-chan"); !tracked || owner != "user-b" {
		t.Errorf("Owner(new-chan) = %q, %v, want user-b, tracked: B's join was applied", owner, tracked)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "user-b" {
		t.Errorf("row = %+v (present %v), want kept with owner user-b", row, ok)
	}
	notices := noticesIn(t, fake, "new-chan")
	if len(notices) != 2 || !strings.Contains(notices[1].data.Content, "user-b") {
		t.Errorf("notices = %d, want the create notice and a handover notice naming user-b", len(notices))
	}
}

// A move Discord refuses leaves a fresh channel the creator never entered,
// and another member walked into it before the compensating delete. The
// delete is refused and the channel stays tracked as an ordinary spawned
// channel: its row is written, its owner is elected from the occupants the
// cache confirms, never the creator, and the create notice follows the row.
func TestTempVCRefusedCompensatingDeleteKeepsTheChannelWithAnOwner(t *testing.T) {
	cases := []struct {
		name      string
		joiner    *discordgo.Member
		wantOwner string
	}{
		{"the joiner holds a rank role and owns it", member("B", testRankPVT), "user-b"},
		{"the joiner holds no rank role and nobody owns it", member("B"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			st := seedStore(t, testHub())
			tv := newTestTempVC(t, fake, st)
			captures := countCaptures(t)
			logs := captureLogs(t)
			a := member("A", testRankSGT)
			fake.moveErr = restError(http.StatusBadRequest, discordgo.ErrCodeTargetIsNotConnectedToVoice, "Target user is not connected to voice")
			fake.moveHook = func() {
				fake.deliver(tv, voiceEvent("user-a", "", a))
				fake.deliver(tv, voiceEvent("user-b", "new-chan", tc.joiner))
			}

			fake.deliver(tv, voiceEvent("user-a", testTempVCHub, a))

			if n := fake.deleteCallCount(); n != 0 {
				t.Errorf("delete attempts = %d, want 0: B is inside", n)
			}
			if owner, tracked := tv.Owner("new-chan"); !tracked || owner != tc.wantOwner {
				t.Errorf("Owner(new-chan) = %q, %v, want %q, tracked", owner, tracked, tc.wantOwner)
			}
			row, ok := rowFor(t, st, "new-chan")
			if !ok || row.OwnerUserID != tc.wantOwner || row.Number != 1 || row.HubID != storedHubID(t, st) {
				t.Errorf("row = %+v (present %v), want number 1 on the hub with owner %q", row, ok, tc.wantOwner)
			}
			notices := noticesIn(t, fake, "new-chan")
			if len(notices) != 1 {
				t.Fatalf("notices in new-chan = %d, want the create notice alone", len(notices))
			}
			if strings.Contains(notices[0].data.Content, "user-a") {
				t.Errorf("notice %q names the creator, who never arrived", notices[0].data.Content)
			}
			if tc.wantOwner != "" && !strings.Contains(notices[0].data.Content, tc.wantOwner) {
				t.Errorf("notice %q does not name the owner %s", notices[0].data.Content, tc.wantOwner)
			}
			if tc.wantOwner == "" && notices[0].data.Content != ownershipNoticeLine(noticeCreate, "", true) {
				t.Errorf("notice %q, want the ownerless create line", notices[0].data.Content)
			}
			if lines := logRecordsWith(t, logs, "INFO", map[string]string{"reason": "channel occupied", "channel_id": "new-chan"}); len(lines) != 1 {
				t.Errorf("INFO lines with reason \"channel occupied\" for new-chan = %d, want one", len(lines))
			}
			if *captures != 0 {
				t.Errorf("captures = %d, want 0", *captures)
			}
		})
	}
}

// Handover with an absent successor: the CPT who would take over already
// left, but their leave handler has not run when the owner's does. The
// election runs over the recorded occupants the cache confirms, so the
// present PVT takes over, or nobody when the present occupant holds no rank.
func TestTempVCHandoverSkipsASuccessorTheCacheShowsGone(t *testing.T) {
	cases := []struct {
		name      string
		present   *discordgo.Member
		wantOwner string
	}{
		{"the present occupant holds a rank role", member("C", testRankPVT), "user-c"},
		{"the present occupant holds none", member("C"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			st := seedStore(t, testHub())
			tv := newTestTempVC(t, fake, st)
			a, b := member("A", testRankSGT), member("B", testRankCPT)
			spawnInto(tv, fake, "user-a", "new-chan", a)
			fake.deliver(tv, voiceEvent("user-b", "new-chan", b))
			fake.deliver(tv, voiceEvent("user-c", "new-chan", tc.present))

			fake.setVoice("user-a", "")
			fake.setVoice("user-b", "")
			tv.HandleVoiceStateUpdate(voiceEvent("user-a", "", a))

			if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != tc.wantOwner {
				t.Errorf("row after the owner left = %+v (present %v), want owner %q", row, ok, tc.wantOwner)
			}
			notices := noticesIn(t, fake, "new-chan")
			if len(notices) != 2 {
				t.Fatalf("notices = %d, want the create notice and one handover notice", len(notices))
			}
			if notices[1].data.Content != ownershipNoticeLine(noticeHandover, tc.wantOwner, true) {
				t.Errorf("handover notice %q, want the line for owner %q", notices[1].data.Content, tc.wantOwner)
			}

			tv.HandleVoiceStateUpdate(voiceEvent("user-b", "", b))

			if owner, tracked := tv.Owner("new-chan"); !tracked || owner != tc.wantOwner {
				t.Errorf("Owner(new-chan) after the CPT's reversed leave = %q, %v, want %q, tracked", owner, tracked, tc.wantOwner)
			}
			if n := len(noticesIn(t, fake, "new-chan")); n != 2 {
				t.Errorf("notices after the CPT's reversed leave = %d, want still 2", n)
			}
		})
	}
}

// Guild missing from the cache, the interval between a GUILD_DELETE and the
// next GUILD_CREATE. Events count as current at entry and update the record,
// but no guarded action can be confirmed, so each is skipped: the owner's
// reversed leave commits no handover, and the last occupant's reversed leave
// deletes nothing. The GUILD_CREATE sweep then rebuilds the record from the
// payload, which is the repair path.
func TestTempVCGuildMissingSkipsActionsAndTheSweepRepairs(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	a, b := member("A", testRankSGT), member("B", testRankPVT)
	spawnInto(tv, fake, "user-a", "new-chan", a)
	fake.deliver(tv, voiceEvent("user-b", "new-chan", b))
	noticesBefore := len(noticesIn(t, fake, "new-chan"))

	fake.setGuildMissing(true)
	// Gateway order for each member is "leave, rejoin"; the handlers run
	// reversed, so the rejoin is a no-op and the leave lands last.
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))
	tv.HandleVoiceStateUpdate(voiceEvent("user-a", "", a))
	tv.HandleVoiceStateUpdate(voiceEvent("user-b", "new-chan", b))
	tv.HandleVoiceStateUpdate(voiceEvent("user-b", "", b))

	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts with the guild missing = %d, want 0: nothing can be confirmed", n)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "user-a" {
		t.Errorf("row = %+v (present %v), want kept with owner user-a: no handover was committed", row, ok)
	}
	if n := len(noticesIn(t, fake, "new-chan")); n != noticesBefore {
		t.Errorf("notices = %d, want %d: no handover was committed", n, noticesBefore)
	}

	fake.deliverGuildCreate(tv, guildCreate(
		[]*discordgo.Channel{
			voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
			voiceChannel("new-chan", testTempVCCategory, "Voice - 1"),
		},
		&discordgo.VoiceState{UserID: "user-a", ChannelID: "new-chan", Member: guildMember("user-a", testRankSGT)},
		&discordgo.VoiceState{UserID: "user-b", ChannelID: "new-chan", Member: guildMember("user-b", testRankPVT)},
	))

	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts after the sweep = %d, want 0: both members are inside", n)
	}
	if owner, tracked := tv.Owner("new-chan"); !tracked || owner != "user-a" {
		t.Errorf("Owner(new-chan) after the sweep = %q, %v, want user-a, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "user-a" {
		t.Errorf("row after the sweep = %+v (present %v), want owner user-a", row, ok)
	}
	if n := len(noticesIn(t, fake, "new-chan")); n != noticesBefore {
		t.Errorf("notices after the sweep = %d, want %d: a restored owner is no handover", n, noticesBefore)
	}
	// The record is rebuilt from the payload: B's leave now empties the
	// channel only after A's, and the delete goes out.
	fake.deliver(tv, voiceEvent("user-b", "", b))
	if n := fake.deleteCallCount(); n != 0 {
		t.Errorf("delete attempts after B left = %d, want 0: A is still inside per the rebuilt record", n)
	}
	fake.deliver(tv, voiceEvent("user-a", "", a))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted after both left = %v, want new-chan", ids)
	}
}
