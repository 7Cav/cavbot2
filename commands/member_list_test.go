package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// The member list (#440): the bot asks Discord for the guild's whole member
// list after every GUILD_CREATE and knows when the last part has arrived.
// These tests drive the production adapter over a real session whose REST
// calls fail the test, feeding the gateway events discordgo's reader would.
// The one fake is the gateway write that sends the member request: it
// records each request, and every chunk a test feeds carries the nonce read
// off that record.

// fakeMemberRequester records each member request in place of the gateway
// write.
type fakeMemberRequester struct {
	mu   sync.Mutex
	sent []memberRequest
	// failNext makes the next request fail to send, as the gateway write
	// does while the connection is down. The request is still recorded.
	failNext error
}

// memberRequest is one member request as the gateway would have been sent
// it.
type memberRequest struct {
	guildID, query string
	limit          int
	nonce          string
}

func (f *fakeMemberRequester) RequestGuildMembers(guildID, query string, limit int, nonce string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, memberRequest{guildID: guildID, query: query, limit: limit, nonce: nonce})
	err := f.failNext
	f.failNext = nil
	return err
}

// requests returns the member requests sent so far, oldest first.
func (f *fakeMemberRequester) requests() []memberRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]memberRequest(nil), f.sent...)
}

// memberListWorld is the production adapter for the test guild over a real
// session, on the package's fake clock.
type memberListWorld struct {
	t     *testing.T
	dg    *discordgo.Session
	mgr   *sessionTempVCManager
	req   *fakeMemberRequester
	clock *fakeClock
}

// newMemberListWorld builds the adapter as NewSessionTempVCManager does,
// with the recording fake as the gateway write, and connects it.
func newMemberListWorld(t *testing.T) *memberListWorld {
	t.Helper()
	clock := installFakeClock(t)
	dg := stateSession(t, refusingAPI(t))
	req := &fakeMemberRequester{}
	w := &memberListWorld{t: t, dg: dg, mgr: newSessionTempVCManager(dg, testTempVCGuild, req), req: req, clock: clock}
	w.deliver(&discordgo.Connect{})
	return w
}

// deliver hands one gateway event to the adapter the way discordgo's reader
// does: the session's state takes it first, then the handler
// NewSessionTempVCManager wires.
func (w *memberListWorld) deliver(e any) {
	w.t.Helper()
	feed(w.t, w.dg, e)
	w.mgr.onGatewayEvent(w.dg, e)
}

// lastNonce is the nonce of the latest member request, read off the
// recording fake.
func (w *memberListWorld) lastNonce() string {
	w.t.Helper()
	sent := w.req.requests()
	if len(sent) == 0 {
		w.t.Fatal("no member request sent, want one")
	}
	return sent[len(sent)-1].nonce
}

// read takes the member list as the panel would.
func (w *memberListWorld) read() MemberListSnapshot {
	return w.mgr.MemberList(testTempVCGuild)
}

// listGuildCreate is the test guild's GUILD_CREATE. Without the presences
// intent it carries the bot alone, whatever the guild's size.
func listGuildCreate() *discordgo.GuildCreate {
	return &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID:      testTempVCGuild,
		Members: []*discordgo.Member{listMember("user-bot", "cavbot")},
	}}
}

// listChunk is one GUILD_MEMBERS_CHUNK for the test guild.
func listChunk(nonce string, index, count int, members ...*discordgo.Member) *discordgo.GuildMembersChunk {
	return &discordgo.GuildMembersChunk{
		GuildID: testTempVCGuild, Members: members, ChunkIndex: index, ChunkCount: count, Nonce: nonce,
	}
}

// rateLimited is the RATE_LIMITED dispatch Discord sends for a full-list
// request inside its 30 s window, shaped as the test-guild probe recorded
// it (#420), and decoded from its wire frame the way discordgo's reader
// decodes every dispatch. discordgo has no type for it, so it reaches
// handlers only as the raw event.
func rateLimited(t *testing.T, nonce, retryAfter string) *discordgo.Event {
	t.Helper()
	frame := `{"op":0,"s":42,"t":"RATE_LIMITED","d":{"retry_after":` + retryAfter +
		`,"opcode":8,"meta":{"nonce":"` + nonce + `","guild_id":"` + testTempVCGuild + `"}}}`
	var e *discordgo.Event
	if err := json.Unmarshal([]byte(frame), &e); err != nil {
		t.Fatalf("decode %s: %v", frame, err)
	}
	return e
}

// listedIn returns the member with the ID from a snapshot, or nil.
func listedIn(snap MemberListSnapshot, id string) *ListedMember {
	for i := range snap.Members {
		if snap.Members[i].ID == id {
			return &snap.Members[i]
		}
	}
	return nil
}

// listMember is a guild member with a username and nothing else set.
func listMember(id, username string) *discordgo.Member {
	return &discordgo.Member{User: &discordgo.User{ID: id, Username: username}}
}

func TestMemberListAsksForTheWholeListAfterTheGuildsGuildCreate(t *testing.T) {
	w := newMemberListWorld(t)

	w.deliver(&discordgo.GuildCreate{Guild: &discordgo.Guild{ID: "guild-other"}})

	if sent := w.req.requests(); len(sent) != 0 {
		t.Fatalf("after another guild's GUILD_CREATE, sent %+v, want no member request", sent)
	}

	w.deliver(listGuildCreate())

	sent := w.req.requests()
	if len(sent) != 1 {
		t.Fatalf("after the guild's GUILD_CREATE, sent %+v, want one member request", sent)
	}
	if r := sent[0]; r.guildID != testTempVCGuild || r.query != "" || r.limit != 0 || r.nonce == "" {
		t.Errorf("request = %+v, want the whole list of %s: an empty query, limit 0 and a nonce", r, testTempVCGuild)
	}
	if snap := w.read(); snap.Status != MemberListArriving || snap.PartsReceived != 0 || snap.PartsExpected != 0 {
		t.Errorf("snapshot = status %v, %d of %d parts, want arriving with no part count yet", snap.Status, snap.PartsReceived, snap.PartsExpected)
	}
}

// The list completes when every part carrying the request's nonce has
// arrived, counted by chunk index against chunk count, in whatever order
// Discord sends them. Parts answering another request count for nothing.
// Until it completes, the snapshot holds no members, so no reader can show a
// partial list.
func TestMemberListCompletesWhenEveryPartOfTheRequestHasArrived(t *testing.T) {
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	nonce := w.lastNonce()
	joined := time.Date(2024, time.March, 2, 15, 4, 5, 0, time.UTC)
	doe := &discordgo.Member{
		User:     &discordgo.User{ID: "user-doe", Username: "doe.j", GlobalName: "John Doe", Avatar: "userhash-doe"},
		Nick:     "Doe.J",
		Avatar:   "guildhash-doe",
		JoinedAt: joined,
		Roles:    []string{"role-a", "role-b"},
	}
	roe := &discordgo.Member{User: &discordgo.User{ID: "user-roe", Username: "roe.r", Avatar: "userhash-roe"}}

	w.deliver(listChunk(nonce, 0, 3, doe))

	if snap := w.read(); snap.Status != MemberListArriving || snap.PartsReceived != 1 || snap.PartsExpected != 3 || snap.Members != nil {
		t.Fatalf("after part 1, snapshot = status %v, %d of %d parts, %d members, want arriving 1 of 3 with no members",
			snap.Status, snap.PartsReceived, snap.PartsExpected, len(snap.Members))
	}

	w.deliver(listChunk("nonce-of-another-request", 1, 3, listMember("user-x", "x")))
	w.deliver(listChunk("nonce-of-another-request", 2, 3, listMember("user-y", "y")))

	if snap := w.read(); snap.Status != MemberListArriving || snap.PartsReceived != 1 {
		t.Fatalf("after another request's parts, snapshot = status %v, %d of %d parts, want arriving 1 of 3",
			snap.Status, snap.PartsReceived, snap.PartsExpected)
	}

	w.deliver(listChunk(nonce, 2, 3, roe))

	if snap := w.read(); snap.Status != MemberListArriving || snap.PartsReceived != 2 {
		t.Fatalf("after the last index with one part missing, snapshot = status %v, %d of %d parts, want arriving 2 of 3",
			snap.Status, snap.PartsReceived, snap.PartsExpected)
	}

	w.deliver(listChunk(nonce, 1, 3, listMember("user-poe", "poe.p")))

	snap := w.read()
	if snap.Status != MemberListComplete {
		t.Fatalf("after every part, status = %v, want complete", snap.Status)
	}
	for _, id := range []string{"user-bot", "user-doe", "user-roe", "user-poe"} {
		if listedIn(snap, id) == nil {
			t.Errorf("complete list has no %s", id)
		}
	}
	got := listedIn(snap, "user-doe")
	if got == nil {
		t.FailNow()
	}
	if got.Username != "doe.j" || got.GlobalName != "John Doe" || got.Nick != "Doe.J" || !got.JoinedAt.Equal(joined) {
		t.Errorf("user-doe = %+v, want username doe.j, global name John Doe, nickname Doe.J, joined %v", got, joined)
	}
	if !slices.Equal(got.RoleIDs, []string{"role-a", "role-b"}) {
		t.Errorf("user-doe's roles = %v, want role-a and role-b", got.RoleIDs)
	}
	if !strings.Contains(got.AvatarURL, "guildhash-doe") {
		t.Errorf("user-doe's avatar = %q, want the server avatar guildhash-doe", got.AvatarURL)
	}
	if roeGot := listedIn(snap, "user-roe"); roeGot != nil && !strings.Contains(roeGot.AvatarURL, "userhash-roe") {
		t.Errorf("user-roe's avatar = %q, want the user avatar userhash-roe", roeGot.AvatarURL)
	}
}

// voiceMemberJSON is user-voice, a member in a voice channel, as a
// GUILD_CREATE carries them.
const voiceMemberJSON = `{"user":{"id":"user-voice","username":"voice.v"},"nick":"Voice","roles":["role-a","role-b"]}`

// voiceGuildCreate is the test guild's GUILD_CREATE while user-voice is in
// a voice channel, decoded from its wire frame the way discordgo's reader
// decodes every dispatch. Discord names each member in voice twice in its
// members (discord/discord-api-docs#997), as the test guild's did in #549.
func voiceGuildCreate(t *testing.T) *discordgo.GuildCreate {
	t.Helper()
	frame := `{"op":0,"s":2,"t":"GUILD_CREATE","d":{"id":"` + testTempVCGuild + `",` +
		`"voice_states":[{"user_id":"user-voice","channel_id":"voice-1","session_id":"session-1"}],` +
		`"members":[{"user":{"id":"user-bot","username":"cavbot"}},` + voiceMemberJSON + `,` + voiceMemberJSON + `]}}`
	var e *discordgo.Event
	if err := json.Unmarshal([]byte(frame), &e); err != nil {
		t.Fatalf("decode %s: %v", frame, err)
	}
	gc := &discordgo.GuildCreate{}
	if err := json.Unmarshal(e.RawData, gc); err != nil {
		t.Fatalf("decode the GUILD_CREATE in %s: %v", frame, err)
	}
	return gc
}

// voiceMember is user-voice as a part of the member list carries them.
func voiceMember() *discordgo.Member {
	return &discordgo.Member{User: &discordgo.User{ID: "user-voice", Username: "voice.v"}, Nick: "Voice", Roles: []string{"role-a", "role-b"}}
}

// timesListed counts the entries a snapshot holds for the member.
func timesListed(snap MemberListSnapshot, id string) int {
	n := 0
	for _, m := range snap.Members {
		if m.ID == id {
			n++
		}
	}
	return n
}

// completeVoiceList delivers voiceGuildCreate and the one part that
// completes its list.
func (w *memberListWorld) completeVoiceList() {
	w.t.Helper()
	w.deliver(voiceGuildCreate(w.t))
	w.deliver(listChunk(w.lastNonce(), 0, 1, listMember("user-bot", "cavbot"), voiceMember(), listMember("user-other", "other.o")))
	if snap := w.read(); snap.Status != MemberListComplete {
		w.t.Fatalf("after the only part, status = %v, want complete", snap.Status)
	}
}

// A member in voice when the GUILD_CREATE arrives is listed once, though
// Discord names them twice in it, so the Foxhole page lists them on one row
// and its filters count them once (#549).
func TestMemberListHoldsAMemberInVoiceAtGuildCreateOnce(t *testing.T) {
	w := newMemberListWorld(t)
	w.completeVoiceList()

	snap := w.read()
	for _, id := range []string{"user-bot", "user-voice", "user-other"} {
		if n := timesListed(snap, id); n != 1 {
			t.Errorf("complete list holds %s %d times, want once", id, n)
		}
	}
}

// The one entry of a member in voice at the GUILD_CREATE follows their
// updates and their leaving, as every member's does. A copy frozen at the
// GUILD_CREATE would keep listing them as holding a role they lost, or as
// in the server after they left (#549).
func TestMemberListFollowsAMemberInVoiceAtGuildCreate(t *testing.T) {
	w := newMemberListWorld(t)
	w.completeVoiceList()

	lostRoleB := voiceMember()
	lostRoleB.GuildID, lostRoleB.Roles = testTempVCGuild, []string{"role-a"}
	w.deliver(&discordgo.GuildMemberUpdate{Member: lostRoleB})

	snap := w.read()
	if n := timesListed(snap, "user-voice"); n != 1 {
		t.Fatalf("after an update, the list holds user-voice %d times, want once", n)
	}
	got, ok := snap.Member("user-voice")
	if !ok || !slices.Equal(got.RoleIDs, []string{"role-a"}) {
		t.Errorf("after role-b was removed, user-voice = %+v (found %v), want role-a alone", got, ok)
	}

	w.deliver(&discordgo.GuildMemberRemove{Member: &discordgo.Member{GuildID: testTempVCGuild, User: &discordgo.User{ID: "user-voice"}}})

	if n := timesListed(w.read(), "user-voice"); n != 0 {
		t.Errorf("after user-voice left, the list holds them %d times, want none", n)
	}
}

// Completion leaves one INFO record with the part count, which is how
// Grafana learns the live guild's chunk time.
func TestMemberListLogsItsCompletionWithThePartCount(t *testing.T) {
	logs := captureLogs(t)
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	nonce := w.lastNonce()

	w.deliver(listChunk(nonce, 0, 3, listMember("user-a", "a")))
	w.deliver(listChunk(nonce, 1, 3, listMember("user-b", "b")))
	w.deliver(listChunk(nonce, 2, 3, listMember("user-c", "c")))

	fields := map[string]string{"guild_id": testTempVCGuild, "nonce": nonce, "chunks": "3"}
	got := logRecordsWith(t, logs, "INFO", fields)
	if len(got) != 1 {
		t.Fatalf("INFO records carrying %v = %v, want one", fields, got)
	}
	if got[0]["duration_ms"] == "" {
		t.Errorf("completion record %v carries no time from request to last part", got[0])
	}
}

// Discord refuses a full-list request inside its 30 s window with a
// RATE_LIMITED dispatch. The bot asks again once retry_after has passed,
// never before, and the snapshot says when, so the page can tell a manager
// how long to wait. A refusal of some other request changes nothing.
func TestMemberListAsksAgainOnceARefusalsWaitHasPassed(t *testing.T) {
	logs := captureLogs(t)
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	refused := w.lastNonce()
	start := w.clock.read()
	const wait = 12500 * time.Millisecond

	w.deliver(rateLimited(t, "nonce-of-another-request", "12.5"))

	if snap := w.read(); snap.Status != MemberListArriving {
		t.Fatalf("after another request's refusal, status = %v, want arriving", snap.Status)
	}

	w.deliver(rateLimited(t, refused, "12.5"))

	snap := w.read()
	if snap.Status != MemberListRefused {
		t.Fatalf("after the refusal, status = %v, want refused", snap.Status)
	}
	if snap.RetryAt.Before(start.Add(wait)) || snap.RetryAt.After(start.Add(wait+time.Second)) {
		t.Errorf("retry at %v, want 12.5 s after %v, or within a second after that", snap.RetryAt, start)
	}
	if got := logRecordsWith(t, logs, "WARN", map[string]string{"nonce": refused}); len(got) != 1 {
		t.Errorf("WARN records carrying the refused nonce %s = %v, want one", refused, got)
	}

	w.clock.advance(wait - time.Millisecond)

	if sent := w.req.requests(); len(sent) != 1 {
		t.Fatalf("before retry_after passed, sent %+v, want the first request alone", sent)
	}

	w.clock.advance(snap.RetryAt.Sub(w.clock.read()))

	sent := w.req.requests()
	if len(sent) != 2 {
		t.Fatalf("at the retry time, sent %+v, want a second request", sent)
	}
	retry := sent[1].nonce
	if retry == refused || retry == "" {
		t.Errorf("the second request's nonce = %q, want one of its own", retry)
	}
	if got := logRecordsWith(t, logs, "INFO", map[string]string{"nonce": retry}); len(got) != 1 {
		t.Errorf("INFO records carrying the second request's nonce %s = %v, want one", retry, got)
	}
	if snap := w.read(); snap.Status != MemberListArriving {
		t.Errorf("after asking again, status = %v, want arriving", snap.Status)
	}
}

// When the parts stop coming, the bot asks again once the stall limit
// passes with no new part, and keeps asking: it never gives up. Each part
// restarts the wait.
func TestMemberListAsksAgainWhenThePartsStall(t *testing.T) {
	logs := captureLogs(t)
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	first := w.lastNonce()

	w.clock.advance(memberListStallLimit - time.Second)
	w.deliver(listChunk(first, 0, 3, listMember("user-a", "a")))
	w.clock.advance(memberListStallLimit - time.Second)

	if sent := w.req.requests(); len(sent) != 1 {
		t.Fatalf("with a part inside each stall limit, sent %+v, want the first request alone", sent)
	}

	w.clock.advance(2 * time.Second)

	sent := w.req.requests()
	if len(sent) != 2 {
		t.Fatalf("past the stall limit after the last part, sent %+v, want a second request", sent)
	}
	if got := logRecordsWith(t, logs, "INFO", map[string]string{"nonce": sent[1].nonce}); len(got) != 1 {
		t.Errorf("INFO records carrying the second request's nonce %s = %v, want one", sent[1].nonce, got)
	}

	w.clock.advance(memberListStallLimit + time.Second)

	sent = w.req.requests()
	if len(sent) != 3 {
		t.Fatalf("past another stall limit with no part at all, sent %+v, want a third request", sent)
	}
	if got := logRecordsWith(t, logs, "INFO", map[string]string{"nonce": sent[2].nonce}); len(got) != 1 {
		t.Errorf("INFO records carrying the third request's nonce %s = %v, want one", sent[2].nonce, got)
	}
}

// A list still partial at the late limit after its GUILD_CREATE reaches
// Sentry once, however long it stays partial, and reads late from then on.
// A list that completed in time reaches Sentry not at all.
func TestMemberListStillPartialAtTheLateLimitReachesSentryOnce(t *testing.T) {
	t.Run("a list still partial", func(t *testing.T) {
		rec := recordSentry(t)
		w := newMemberListWorld(t)
		w.deliver(listGuildCreate())
		w.deliver(listChunk(w.lastNonce(), 0, 3, listMember("user-a", "a")))

		w.clock.advance(memberListLateAfter - time.Second)

		if n := len(rec.Events()); n != 0 {
			t.Fatalf("before the late limit, %d Sentry events, want none", n)
		}

		w.clock.advance(2 * time.Second)

		if snap := w.read(); snap.Status != MemberListLate {
			t.Errorf("past the late limit, status = %v, want late", snap.Status)
		}
		if n := len(rec.Events()); n != 1 {
			t.Fatalf("past the late limit, %d Sentry events, want one", n)
		}

		w.clock.advance(3 * memberListLateAfter)

		if n := len(rec.Events()); n != 1 {
			t.Errorf("long past the late limit, %d Sentry events, want still one", n)
		}
	})

	t.Run("a list complete in time", func(t *testing.T) {
		rec := recordSentry(t)
		w := newMemberListWorld(t)
		w.deliver(listGuildCreate())
		w.deliver(listChunk(w.lastNonce(), 0, 1, listMember("user-a", "a")))

		w.clock.advance(memberListLateAfter + time.Second)

		if n := len(rec.Events()); n != 0 {
			t.Errorf("past the late limit with the list complete, %d Sentry events, want none", n)
		}
		if snap := w.read(); snap.Status != MemberListComplete {
			t.Errorf("past the late limit with the list complete, status = %v, want complete", snap.Status)
		}
	})
}

// Regression pin: green on arrival, no code was written for it. A refusal
// past the late mark reads late, and the snapshot still says when the bot
// asks again, so the page can skip a wait the retry would outlast (#495).
func TestMemberListPastTheLateMarkKeepsAPendingRefusalsRetryTime(t *testing.T) {
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	w.clock.advance(memberListLateAfter + time.Second)
	refusedAt := w.clock.read()
	const wait = 25 * time.Second

	w.deliver(rateLimited(t, w.lastNonce(), "25"))

	snap := w.read()
	if snap.Status != MemberListLate {
		t.Errorf("after a refusal past the late mark, status = %v, want late", snap.Status)
	}
	if snap.RetryAt.Before(refusedAt.Add(wait)) || snap.RetryAt.After(refusedAt.Add(wait+time.Second)) {
		t.Errorf("retry at %v, want 25 s after %v, or within a second after that", snap.RetryAt, refusedAt)
	}
}

// A READY swaps in an unavailable placeholder for the guild, which empties
// the list. Until Discord sends the guild again, the bot neither asks for
// its members nor reports the list late: a guild Discord never sent is no
// broken list.
func TestMemberListWaitsForTheGuildAfterAReady(t *testing.T) {
	rec := recordSentry(t)
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	w.deliver(listChunk(w.lastNonce(), 0, 3, listMember("user-a", "a")))

	w.deliver(readyWith(&discordgo.Guild{ID: testTempVCGuild, Unavailable: true}))

	if snap := w.read(); snap.Status != MemberListNoGuild || snap.Members != nil {
		t.Errorf("after READY, snapshot = status %v with %d members, want no guild data", snap.Status, len(snap.Members))
	}

	w.clock.advance(memberListLateAfter + time.Second)

	if sent := w.req.requests(); len(sent) != 1 {
		t.Errorf("with the guild not sent since READY, sent %+v, want the first request alone", sent)
	}
	if n := len(rec.Events()); n != 0 {
		t.Errorf("with the guild not sent since READY, %d Sentry events, want none", n)
	}
}

// A refusal's wait that ends after a READY asks nothing either: the
// GUILD_CREATE that follows asks for the list itself.
func TestMemberListDropsARefusalsRetryAfterAReady(t *testing.T) {
	w := newMemberListWorld(t)
	w.deliver(listGuildCreate())
	w.deliver(rateLimited(t, w.lastNonce(), "12.5"))
	retryAt := w.read().RetryAt

	w.deliver(readyWith(&discordgo.Guild{ID: testTempVCGuild, Unavailable: true}))
	w.clock.advance(retryAt.Sub(w.clock.read()) + time.Second)

	if sent := w.req.requests(); len(sent) != 1 {
		t.Errorf("with the guild not sent since READY, sent %+v at the retry time, want the first request alone", sent)
	}
}

// completeList delivers the test guild's GUILD_CREATE and a one-part reply
// holding user-a.
func (w *memberListWorld) completeList() {
	w.t.Helper()
	w.deliver(listGuildCreate())
	w.deliver(listChunk(w.lastNonce(), 0, 1, listMember("user-a", "a")))
}

// The snapshot says whether the gateway connection is up. A list complete
// before a disconnect stays complete through it, so a blip hides nothing.
func TestMemberListSaysWhetherTheGatewayIsConnected(t *testing.T) {
	w := newMemberListWorld(t)
	w.completeList()

	if snap := w.read(); !snap.Connected {
		t.Error("while connected, the snapshot reads disconnected")
	}

	w.deliver(&discordgo.Disconnect{})

	snap := w.read()
	if snap.Connected {
		t.Error("after Disconnect, the snapshot reads connected")
	}
	if snap.Status != MemberListComplete || listedIn(snap, "user-a") == nil {
		t.Errorf("after Disconnect, snapshot = status %v with %d members, want the complete list", snap.Status, len(snap.Members))
	}
}

// Regression pin: green on arrival, no code was written for it. It is the
// proof that a resumed session sends no request and keeps the list.
// discordgo fires Connect on a resume as on a fresh session, and a resumed
// session gets no GUILD_CREATE, so asking again on Connect would throw a
// complete list away.
func TestMemberListKeepsTheListThroughAResume(t *testing.T) {
	w := newMemberListWorld(t)
	w.completeList()

	w.deliver(&discordgo.Disconnect{})
	w.deliver(&discordgo.Connect{})
	w.deliver(&discordgo.Resumed{})

	if sent := w.req.requests(); len(sent) != 1 {
		t.Errorf("after a resume, sent %+v, want the first request alone", sent)
	}
	snap := w.read()
	if snap.Status != MemberListComplete || listedIn(snap, "user-a") == nil || !snap.Connected {
		t.Errorf("after a resume, snapshot = status %v with %d members, connected %v, want the complete list, connected",
			snap.Status, len(snap.Members), snap.Connected)
	}
}

// An outage's GUILD_DELETE takes the guild out of the state, and its
// GUILD_CREATE puts back the bot and little else, so the list starts over
// with a request of its own. Parts answering the request from before the
// outage count for nothing. In the moment between the GUILD_CREATE landing
// in the state and the bot handling it, the snapshot never passes the
// state's partial list off as the complete one from before.
func TestMemberListStartsOverAfterAnOutage(t *testing.T) {
	w := newMemberListWorld(t)
	w.completeList()
	before := w.lastNonce()

	w.deliver(&discordgo.GuildDelete{Guild: &discordgo.Guild{ID: testTempVCGuild, Unavailable: true}})

	if snap := w.read(); snap.Status != MemberListNoGuild {
		t.Errorf("during the outage, status = %v, want no guild data", snap.Status)
	}

	back := listGuildCreate()
	feed(t, w.dg, back)

	if snap := w.read(); snap.Status == MemberListComplete {
		t.Fatalf("with the GUILD_CREATE in the state and not yet handled, the snapshot reads complete with %d members, want the list still to come",
			len(snap.Members))
	}

	w.mgr.onGatewayEvent(w.dg, back)

	sent := w.req.requests()
	if len(sent) != 2 {
		t.Fatalf("after the outage's GUILD_CREATE, sent %+v, want a second request", sent)
	}
	after := sent[1].nonce
	if after == before {
		t.Errorf("the second request reuses the nonce %q, want one of its own", after)
	}

	w.deliver(listChunk(before, 0, 1, listMember("user-a", "a")))

	if snap := w.read(); snap.Status == MemberListComplete {
		t.Fatal("a part answering the request from before the outage completed the list")
	}

	w.deliver(listChunk(after, 0, 1, listMember("user-a", "a")))

	if snap := w.read(); snap.Status != MemberListComplete || listedIn(snap, "user-a") == nil {
		t.Errorf("after the new request's part, snapshot = status %v with %d members, want complete with user-a", snap.Status, len(snap.Members))
	}
}

// Regression pin: green on arrival, no code was written for it. It guards
// the copy under the state's lock. discordgo's reader writes cached members
// in place: a member update or a chunk copies over the cached member, an
// add appends to the slice and a remove shifts it. The readers use every field of what they were given
// after the read returned, outside any lock, so -race is the assertion that
// the snapshot copies each member under the state's lock and shares nothing
// with the state.
func TestMemberListRacesGatewayEventsCleanly(t *testing.T) {
	w := newMemberListWorld(t)
	w.completeList()

	events := func(i int) []any {
		id := fmt.Sprintf("user-%d", i)
		return []any{
			&discordgo.GuildMemberAdd{Member: &discordgo.Member{GuildID: testTempVCGuild,
				User: &discordgo.User{ID: id, Username: id}, Roles: []string{"role-a"}}},
			&discordgo.GuildMemberUpdate{Member: &discordgo.Member{GuildID: testTempVCGuild,
				User: &discordgo.User{ID: "user-a", Username: fmt.Sprintf("a-%d", i)}, Nick: fmt.Sprintf("Nick %d", i),
				Roles: []string{fmt.Sprintf("role-%d", i)}}},
			listChunk("nonce-of-another-request", 0, 1, &discordgo.Member{
				User: &discordgo.User{ID: "user-a", Username: "a"}, Roles: []string{"role-b"}}),
			&discordgo.GuildMemberRemove{Member: &discordgo.Member{GuildID: testTempVCGuild, User: &discordgo.User{ID: id}}},
		}
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		defer close(done)
		// t.Fatalf may not be called off the test goroutine, so the writer
		// reports through t.Errorf.
		for i := range 200 {
			for _, e := range events(i) {
				if err := w.dg.State.OnInterface(w.dg, e); err != nil {
					t.Errorf("State.OnInterface(%T): %v", e, err)
					return
				}
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			for {
				snap := w.read()
				for _, m := range snap.Members {
					if m.ID == "" || m.Username == "" || m.AvatarURL == "" {
						t.Errorf("read member %+v, which no event sent", m)
						return
					}
					for _, r := range m.RoleIDs {
						if r == "" {
							t.Errorf("read member %+v with an empty role ID", m)
							return
						}
					}
				}
				select {
				case <-done:
					return
				default:
				}
			}
		})
	}
	wg.Wait()
}

// Regression pin: green on arrival, no code was written for it. A request
// the gateway write fails to send, as it does while the connection is down,
// is waited on like any other, so the stall wait asks again.
func TestMemberListAsksAgainAfterARequestFailsToSend(t *testing.T) {
	w := newMemberListWorld(t)
	w.req.failNext = errors.New("websocket not open")
	w.deliver(listGuildCreate())

	w.clock.advance(memberListStallLimit + time.Second)

	if sent := w.req.requests(); len(sent) != 2 {
		t.Errorf("past the stall limit after a failed send, sent %+v, want a second request", sent)
	}
}
