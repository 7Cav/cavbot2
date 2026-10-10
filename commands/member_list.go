package commands

import (
	"cmp"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The member list (#440, GLOSSARY.md): the bot's copy of every member of the
// guild, which the Foxhole page reads its holders from. Without the
// presences intent a GUILD_CREATE carries the bot and the members in voice,
// so after each one the bot asks the gateway for the whole list, and
// discordgo applies each part Discord sends back to its state. The state
// does not record whether the parts are complete. The tracker here does, by
// counting the parts that answer its own request. A READY empties the list
// and an outage's GUILD_CREATE resets it, both in the state; a resumed
// session keeps it. A GUILD_CREATE names each member in voice twice, and
// the tracker keeps one entry of each (#549). Spec #434, "The member list",
// sets out the rules.

// memberListStallLimit is how long the bot waits for the next part of the
// member list before it asks again. Discord allows one full-list request
// per guild every 30 s, so asking sooner would only be refused.
const memberListStallLimit = 30 * time.Second

// memberListLateAfter is how long after its GUILD_CREATE a member list
// still partial counts as late, and reaches Sentry.
const memberListLateAfter = 5 * time.Minute

// opRequestGuildMembers is the gateway opcode of the member request, which
// a RATE_LIMITED dispatch names when it refuses one.
const opRequestGuildMembers = 8

// askReason says why the bot asked for the member list, in the request's
// log line.
type askReason string

const (
	askGuildCreate askReason = "guild_create"
	askRefused     askReason = "refused"
	askStalled     askReason = "stalled"
)

// errMemberListLate is the Sentry event of a member list still partial
// memberListLateAfter after its GUILD_CREATE.
var errMemberListLate = errors.New("member list still partial")

// memberRequester is the subset of *discordgo.Session the member list uses:
// the gateway's request for guild members, opcode 8. The tests fake it to
// record each request's nonce.
type memberRequester interface {
	RequestGuildMembers(guildID, query string, limit int, nonce string, presences bool) error
}

// MemberListStatus is how far the member list has arrived.
type MemberListStatus int

const (
	// MemberListNoGuild is a guild Discord has not sent: the state holds the
	// unavailable placeholder a READY leaves, or nothing after an outage's
	// GUILD_DELETE, or never held it.
	MemberListNoGuild MemberListStatus = iota
	// MemberListArriving is a list whose parts are on their way. The
	// snapshot counts the parts received, and the parts expected once the
	// first part says how many.
	MemberListArriving
	// MemberListRefused is a list whose request Discord refused under its
	// one-request-per-30-s limit. The snapshot says when the bot asks again.
	MemberListRefused
	// MemberListLate is a list still partial memberListLateAfter after its
	// GUILD_CREATE. The bot keeps asking, and while a refusal is pending the
	// snapshot says when it asks again.
	MemberListLate
	// MemberListComplete is a list whose every part has arrived. Gateway
	// events keep it current from then on.
	MemberListComplete
)

// MemberListSnapshot is one copy of the guild's member list from
// discordgo's state, taken under one lock. Nothing in it is shared with the
// state.
type MemberListSnapshot struct {
	Status MemberListStatus
	// Connected is false while the bot's gateway connection is down. A
	// complete list then holds the members as they were when it dropped,
	// and the events missed meanwhile arrive once the session resumes.
	Connected bool
	// PartsReceived and PartsExpected count the parts of the request in
	// flight. PartsExpected is 0 until the first part arrives.
	PartsReceived int
	PartsExpected int
	// RetryAt is when the bot asks again after a refusal, set while a
	// refusal is pending and the status reads refused or late.
	RetryAt time.Time
	// Members is every member of the guild, once each, set only while the
	// status is complete, so no reader can show a partial list.
	Members []ListedMember
}

// Member finds a member in the snapshot. A list that isn't complete holds
// no members, so it finds nobody.
func (s MemberListSnapshot) Member(id string) (ListedMember, bool) {
	for _, m := range s.Members {
		if m.ID == id {
			return m, true
		}
	}
	return ListedMember{}, false
}

// DisplayName is a user's display name in the snapshot, or their Discord ID
// when it doesn't hold them: someone who left the server, or a list not yet
// complete.
func (s MemberListSnapshot) DisplayName(userID string) string {
	if m, ok := s.Member(userID); ok {
		return m.DisplayName()
	}
	return userID
}

// ListedMember is one member of the member list.
type ListedMember struct {
	ID         string
	Username   string
	GlobalName string
	// Nick is the member's server nickname, empty when they have none.
	Nick string
	// AvatarURL is the avatar the member shows in the server: their server
	// avatar, else their own, else Discord's default.
	AvatarURL string
	JoinedAt  time.Time
	RoleIDs   []string
}

// DisplayName is the name a member shows in the server: their server
// nickname, else their global name, else their username.
func (m ListedMember) DisplayName() string {
	return cmp.Or(m.Nick, m.GlobalName, m.Username)
}

// memberList tracks whether the state holds the whole member list of one
// guild, and at each GUILD_CREATE trims the state's members to one entry
// per member. Its handlers run on discordgo's handler goroutines, one per event
// in no fixed order, and its waits on timer goroutines, so mu guards every
// field below it, and each handler and wait decides and starts its request
// in one critical section. Lock order: mu, then the state's lock, read or
// write, never the reverse. discordgo releases the state's lock before it
// starts any handler.
type memberList struct {
	guildID string
	req     memberRequester
	state   *discordgo.State

	mu sync.Mutex
	// guild is the state's guild that the last GUILD_CREATE left, which the
	// requests ask about. A READY, or an outage's GUILD_DELETE and
	// GUILD_CREATE, replaces it in the state, and the tracker's count then
	// belongs to a list that is gone.
	guild *discordgo.Guild
	// requests numbers the requests sent, for their nonces.
	requests uint64
	// nonce is the request in flight's, sentAt when it went out, seen the
	// chunk indexes received for it and expected their count.
	nonce    string
	sentAt   time.Time
	seen     map[int]struct{}
	expected int
	complete bool
	// retryAt is when the bot asks again after a refusal, zero when no
	// refusal is pending.
	retryAt   time.Time
	stopRetry func()
	// stallSeq numbers each stall wait, so a wait that fires after it was
	// replaced or ended does nothing.
	stallSeq  uint64
	stopStall func()
	// episode numbers each GUILD_CREATE, and late is set once the list it
	// asked for is still partial memberListLateAfter after it.
	episode  uint64
	late     bool
	stopLate func()
}

// newSessionTempVCManager builds the production adapter with the given
// member request, which is the session itself outside the tests.
// NewSessionTempVCManager wires its gateway handler.
func newSessionTempVCManager(s *discordgo.Session, guildID string, req memberRequester) *sessionTempVCManager {
	return &sessionTempVCManager{s: s, members: &memberList{guildID: guildID, req: req, state: s.State}}
}

// onGatewayEvent is the adapter's one gateway handler. discordgo hands it
// every event, after applying the event to its state: Connect and
// Disconnect for the connection flag, and GUILD_CREATE, the member chunks
// and the raw RATE_LIMITED dispatch for the member list. discordgo has no
// type for RATE_LIMITED, so it arrives only as the raw event.
func (m *sessionTempVCManager) onGatewayEvent(_ *discordgo.Session, e any) {
	switch e := e.(type) {
	case *discordgo.Connect:
		m.connected.Store(true)
	case *discordgo.Disconnect:
		m.connected.Store(false)
	case *discordgo.GuildCreate:
		m.members.guildCreated(e.Guild)
	case *discordgo.GuildMembersChunk:
		m.members.chunkArrived(e)
	case *discordgo.Event:
		if e.Type == "RATE_LIMITED" {
			m.members.refused(e.RawData)
		}
	}
}

// MemberList copies the tracker's count, then the state's members under
// State.RLock. It scans State.Guilds itself, as GuildData does and for the
// same reasons. discordgo writes cached members in place under State.Lock,
// so each one is copied while the read lock is held.
func (m *sessionTempVCManager) MemberList(guildID string) MemberListSnapshot {
	snap := m.members.snapshot(guildID)
	snap.Connected = m.connected.Load()
	return snap
}

// guildCreated starts a new episode for the guild's GUILD_CREATE: it records
// the guild the state now holds, keeps one entry per member in its members,
// starts the late wait over and asks for the whole list, all in one
// critical section, so no read sees the new guild with the old list's
// count. Any other guild's GUILD_CREATE is ignored.
func (l *memberList) guildCreated(g *discordgo.Guild) {
	if g == nil || g.ID != l.guildID {
		return
	}
	l.mu.Lock()
	l.state.Lock()
	l.guild = availableGuild(l.state, l.guildID)
	if l.guild != nil {
		l.guild.Members = keepLastEntryPerMember(l.guild.Members)
	}
	l.state.Unlock()
	if l.stopLate != nil {
		l.stopLate()
	}
	l.episode++
	l.late = false
	episode := l.episode
	l.stopLate = tempVCAfterFunc(memberListLateAfter, func() { l.lateCheck(episode) })
	nonce := l.startRequestLocked()
	l.mu.Unlock()

	l.send(nonce, askGuildCreate)
}

// keepLastEntryPerMember returns members with each member's earlier entries
// dropped, keeping the last, in order. Discord's GUILD_CREATE names each
// member in voice twice (discord/discord-api-docs#997, #549), and discordgo
// keeps both in Guild.Members while its member map holds the later one,
// which every member update reaches. The earlier one would never change
// again, and a removal would take it out of the slice and leave the later
// one behind. The kept entries go in a new slice, so another handler
// holding the old one never sees its entries shift.
func keepLastEntryPerMember(members []*discordgo.Member) []*discordgo.Member {
	last := make(map[string]int, len(members))
	for i, m := range members {
		if m != nil && m.User != nil {
			last[m.User.ID] = i
		}
	}
	if len(last) == len(members) {
		return members
	}
	kept := make([]*discordgo.Member, 0, len(last))
	for i, m := range members {
		if m == nil || m.User == nil || last[m.User.ID] == i {
			kept = append(kept, m)
		}
	}
	return kept
}

// startRequestLocked starts a new request for the whole list with a nonce
// of its own, clears the count, ends any refusal's wait and starts the
// stall wait. A request the gateway write then fails to send is waited on
// all the same, so the stall wait asks again. Caller holds mu, and sends
// the request once it has released it.
func (l *memberList) startRequestLocked() (nonce string) {
	l.requests++
	l.nonce = "members-" + strconv.FormatUint(l.requests, 10)
	l.sentAt = tempVCNow()
	l.seen = map[int]struct{}{}
	l.expected = 0
	l.complete = false
	l.retryAt = time.Time{}
	if l.stopRetry != nil {
		l.stopRetry()
		l.stopRetry = nil
	}
	l.waitForPartLocked()
	return l.nonce
}

// send sends the request for the whole list, an empty query with limit 0,
// and logs it with why it was sent.
func (l *memberList) send(nonce string, reason askReason) {
	if err := l.req.RequestGuildMembers(l.guildID, "", 0, nonce, false); err != nil {
		utils.Warn("Member list request not sent", "guild_id", l.guildID, "nonce", nonce, "reason", string(reason), "error", err)
		return
	}
	utils.Info("Member list requested", "guild_id", l.guildID, "nonce", nonce, "reason", string(reason))
}

// askAgain sends a new request when the wait that fired still holds: the
// list is partial, current reports the wait is the one in force, and the
// state still holds the guild the requests ask about. The guild's next
// GUILD_CREATE asks for itself. current runs with mu held.
func (l *memberList) askAgain(reason askReason, current func() bool) {
	l.mu.Lock()
	if l.complete || !current() || !l.holdsGuildLocked() {
		l.mu.Unlock()
		return
	}
	nonce := l.startRequestLocked()
	l.mu.Unlock()

	l.send(nonce, reason)
}

// chunkArrived counts a part answering the request in flight, by chunk
// index against chunk count, so parts arriving out of order or twice count
// once each. The list completes with the last one. Parts answering any
// other request count for nothing.
func (l *memberList) chunkArrived(c *discordgo.GuildMembersChunk) {
	l.mu.Lock()
	if c.GuildID != l.guildID || c.Nonce == "" || c.Nonce != l.nonce || l.complete {
		l.mu.Unlock()
		return
	}
	l.seen[c.ChunkIndex] = struct{}{}
	l.expected = c.ChunkCount
	if len(l.seen) < l.expected {
		l.waitForPartLocked()
		l.mu.Unlock()
		return
	}
	l.complete = true
	l.stopStallLocked()
	took := tempVCNow().Sub(l.sentAt)
	l.mu.Unlock()
	// The live guild's chunk time is measured from this line (ADR 0011).
	utils.Info("Member list complete", "guild_id", l.guildID, "nonce", c.Nonce, "chunks", c.ChunkCount, "duration_ms", took.Milliseconds())
}

// rateLimitedDispatch is the body of Discord's RATE_LIMITED dispatch, as
// the test-guild probe recorded it (#420): retry_after in seconds, and the
// refused request's nonce under meta.
type rateLimitedDispatch struct {
	RetryAfter float64 `json:"retry_after"`
	Opcode     int     `json:"opcode"`
	Meta       struct {
		Nonce string `json:"nonce"`
	} `json:"meta"`
}

// refused handles a RATE_LIMITED dispatch. One that refuses the member
// request in flight ends its stall wait, and the bot asks again once
// retry_after has passed. Any other is ignored.
func (l *memberList) refused(raw json.RawMessage) {
	var rl rateLimitedDispatch
	if err := json.Unmarshal(raw, &rl); err != nil {
		return
	}
	l.mu.Lock()
	if rl.Opcode != opRequestGuildMembers || rl.Meta.Nonce == "" || rl.Meta.Nonce != l.nonce || l.complete {
		l.mu.Unlock()
		return
	}
	wait := time.Duration(rl.RetryAfter * float64(time.Second))
	nonce := l.nonce
	l.stopStallLocked()
	l.retryAt = tempVCNow().Add(wait)
	l.stopRetry = tempVCAfterFunc(wait, func() { l.retry(nonce) })
	l.mu.Unlock()
	utils.Warn("Member list request refused", "guild_id", l.guildID, "nonce", nonce, "retry_after_ms", wait.Milliseconds())
}

// retry asks again once the wait of the refusal of request nonce has
// passed, unless another request has gone out meanwhile.
func (l *memberList) retry(nonce string) {
	l.askAgain(askRefused, func() bool { return l.nonce == nonce && !l.retryAt.IsZero() })
}

// waitForPartLocked starts the stall wait over: if no part arrives within
// memberListStallLimit, the bot asks again. Caller holds mu.
func (l *memberList) waitForPartLocked() {
	l.stopStallLocked()
	seq := l.stallSeq
	l.stopStall = tempVCAfterFunc(memberListStallLimit, func() { l.stalled(seq) })
}

// stopStallLocked ends the stall wait. Caller holds mu.
func (l *memberList) stopStallLocked() {
	if l.stopStall != nil {
		l.stopStall()
		l.stopStall = nil
	}
	l.stallSeq++
}

// stalled asks again when the stall wait numbered seq ends with no part. It
// never gives up while the state holds the guild.
func (l *memberList) stalled(seq uint64) {
	l.askAgain(askStalled, func() bool { return seq == l.stallSeq && l.retryAt.IsZero() })
}

// lateCheck marks the list late, and reaches Sentry once, when the list the
// GUILD_CREATE numbered episode asked for is still partial. A guild the
// state no longer holds is one Discord has not sent again, and no fault of
// the list.
func (l *memberList) lateCheck(episode uint64) {
	l.mu.Lock()
	if episode != l.episode || l.complete || !l.holdsGuildLocked() {
		l.mu.Unlock()
		return
	}
	l.late = true
	received, expected := len(l.seen), l.expected
	l.mu.Unlock()
	captureError("Member list still partial after GUILD_CREATE", errMemberListLate,
		"guild_id", l.guildID, "late_after", memberListLateAfter.String(),
		"parts_received", received, "parts_expected", expected)
}

// holdsGuildLocked reports whether the state still holds the guild the
// requests ask about. After a READY it holds an unavailable placeholder,
// and after an outage's GUILD_DELETE nothing, until the guild's next
// GUILD_CREATE. Caller holds mu.
func (l *memberList) holdsGuildLocked() bool {
	l.state.RLock()
	defer l.state.RUnlock()
	g := availableGuild(l.state, l.guildID)
	return g != nil && g == l.guild
}

// availableGuild returns the state's guild, or nil when the state holds no
// guild by that ID or holds its unavailable placeholder. Caller holds the
// state's read lock. It scans State.Guilds itself, since State.Guild takes
// that lock again.
func availableGuild(st *discordgo.State, guildID string) *discordgo.Guild {
	for _, g := range st.Guilds {
		if g.ID == guildID && !g.Unavailable {
			return g
		}
	}
	return nil
}

// snapshot reads the tracker's count, then the state. A guild the state
// holds that is not the one the requests ask about has a GUILD_CREATE
// whose handler has not run yet: its list is still to come, whatever the
// count says about the list before it.
func (l *memberList) snapshot(guildID string) MemberListSnapshot {
	l.mu.Lock()
	snap := MemberListSnapshot{Status: MemberListArriving, PartsReceived: len(l.seen), PartsExpected: l.expected}
	if !l.retryAt.IsZero() {
		snap.Status = MemberListRefused
		snap.RetryAt = l.retryAt
	}
	if l.late {
		snap.Status = MemberListLate
	}
	complete := l.complete
	requested := l.guild
	l.mu.Unlock()

	st := l.state
	st.RLock()
	defer st.RUnlock()
	g := availableGuild(st, l.guildID)
	if guildID != l.guildID || g == nil {
		return MemberListSnapshot{}
	}
	if g != requested {
		return MemberListSnapshot{Status: MemberListArriving}
	}
	if !complete {
		return snap
	}
	snap.Status = MemberListComplete
	snap.Members = make([]ListedMember, 0, len(g.Members))
	for _, member := range g.Members {
		if member == nil || member.User == nil {
			continue
		}
		// A member from the GUILD_CREATE payload carries no guild ID, and
		// a server avatar's URL needs one.
		withGuild := *member
		withGuild.GuildID = l.guildID
		snap.Members = append(snap.Members, ListedMember{
			ID:         member.User.ID,
			Username:   member.User.Username,
			GlobalName: member.User.GlobalName,
			Nick:       member.Nick,
			AvatarURL:  withGuild.AvatarURL(""),
			JoinedAt:   member.JoinedAt,
			RoleIDs:    append([]string(nil), member.Roles...),
		})
	}
	return snap
}
