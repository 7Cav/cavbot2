package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Temporary voice channels (spec #285). The bot replaces MEE6's Temporary
// Channels plugin.
//
// A member joining a hub gets a spawned channel created under the hub
// channel's category and is moved into it. Hub settings come from the store
// (the panel edits them); the runtime holds them in memory and the panel's
// service layer applies each save in-process through ApplyHub and RemoveHub.
// The spawned channel is named "<base string> - n", numbered per hub from 1
// with freed numbers reused, and carries the hub's user limit and bitrate.
// Permission source "category" sends no overwrites, so Discord copies the
// category's; "hub_channel" copies the hub channel's own overwrite list. The
// bot authors no overwrite of its own. Ownership is a bot-internal marker
// that grants no Discord permission.
//
// One row per spawned channel lives in the store: channel ID, hub, number,
// owner. It is written at create and at every handover and deleted with the
// channel. Each create and each handover also posts the ownership notice in
// the spawned channel's text chat, naming the owner or saying there is none,
// and pinging nobody.
//
// A spawned channel is deleted the moment its last occupant leaves. The
// restart sweep on GUILD_CREATE reads the rows, restores each channel's owner
// from its row or elects one by the handover rule, and touches no channel it
// holds no row for.
//
// A spawn that fails is one create per join: no retry, no backoff, no
// auto-disable. The member gets one message in the hub channel's text chat
// that mentions them alone (the category is full, or the error has been
// reported to S6), no DM and no disconnect. The runtime keeps each hub's last spawn
// failure in memory for the panel, cleared by the next successful spawn from
// that hub. A failure Discord returned reaches Sentry once per streak per
// hub; a refusal the bot makes itself, a hub with no category, never does.
// temp_vc_errors.go classifies the Discord errors on these paths.
//
// docs/temp-vc-decisions.md records what is settled and where each decision
// came from. CONTEXT.md carries the vocabulary.
//
// GuildVoiceStates is an unprivileged intent already covered by
// IntentsAllWithoutPrivileged in main.go, so no identify or Developer Portal
// change is needed.

// discordChannelNameLimit is Discord's hard cap on channel name length.
const discordChannelNameLimit = 100

// TempVCOverwriteCeiling is the most the bot may ever write into a channel
// permission overwrite: Manage Channels, Move Members, Mute Members, Deafen
// Members, Connect, View Channel. It is a constant, never a panel setting.
// Today the bot authors no overwrite at all, so it bounds nothing yet; it is
// here so the limit is in code before anything reaches for it.
const TempVCOverwriteCeiling = discordgo.PermissionManageChannels |
	discordgo.PermissionVoiceMoveMembers |
	discordgo.PermissionVoiceMuteMembers |
	discordgo.PermissionVoiceDeafenMembers |
	discordgo.PermissionVoiceConnect |
	discordgo.PermissionViewChannel

// tempVCStoreTimeout bounds each store call the runtime makes, at startup and
// from gateway handlers, so a stalled database never hangs a goroutine.
const tempVCStoreTimeout = 5 * time.Second

// tempVCNow is the runtime's clock. A package var rather than a direct
// time.Now call so tests can pin the time a failure is recorded at, the same
// arrangement as telemetryNow.
var tempVCNow = time.Now

// SpawnFailureCause says why a hub's last spawn failed. The values are
// stable codes, worded so the panel can show them as they are or map them to
// longer wording. Nothing else reads them.
type SpawnFailureCause string

const (
	// SpawnFailureNoCategory is the bot's own refusal: the hub channel has no
	// parent, so there is nowhere to spawn under. No API call is made.
	SpawnFailureNoCategory SpawnFailureCause = "hub channel has no category"
	// SpawnFailureFull is the category or guild channel cap.
	SpawnFailureFull SpawnFailureCause = "this area is full"
	// SpawnFailureForbidden is a 403 on the create.
	SpawnFailureForbidden SpawnFailureCause = "missing permissions"
	// SpawnFailureRateLimited is a 429 on the create. The call is not retried.
	SpawnFailureRateLimited SpawnFailureCause = "rate limited"
	// SpawnFailureDiscordError is any other create failure: a 5xx, a
	// transport error, or a 4xx that is none of the above.
	SpawnFailureDiscordError SpawnFailureCause = "Discord error"
	// SpawnFailureMove is a create that succeeded and a move-into that failed.
	// The new channel was deleted.
	SpawnFailureMove SpawnFailureCause = "move failed"
)

// SpawnFailure is the last failed spawn of one hub: when, and why. The panel
// shows it beside the hub's live spawned count. It lives in memory only; a
// successful spawn from the hub or a restart clears it, and the store never
// holds it.
type SpawnFailure struct {
	At    time.Time
	Cause SpawnFailureCause
}

// Ownership follows the handover rule in CONTEXT.md. The owner always holds
// a rank role, or the channel has no owner: the creator at create if they
// hold one, and when the owner leaves, the highest-ranked occupant with a
// rank role, ties to the lowest user ID. A rank holder who joins a channel
// with no owner takes it. A handover is final, so a returning creator is an
// ordinary occupant. Ranks are read from the roles the gateway payloads
// carry, against the ladder below, most senior first; a member holding none
// of its roles is never a candidate. No API call per create or handover.

// rankRole pairs a rank abbreviation with its Discord role ID. The abbreviation
// is documentation only; the roleID is what the election matches against a
// member's roles.
type rankRole struct {
	abbrev string
	roleID string
}

// tempVCRankRoles are the rank roles most senior first (GOA highest, RCT lowest),
// the election key. AR (active reservist) is the reservist rank granted in lieu
// of retirement and sits just below PVT. A blank role ID would never be matched
// (that rank would not distinguish members); changing an ID later needs no
// other code change.
var tempVCRankRoles = []rankRole{
	{"GOA", "899324897925414993"},
	{"GEN", "899325051936079892"},
	{"LTG", "899325154402914315"},
	{"MG", "899325397391523920"},
	{"BG", "899325493600473088"},
	{"COL", "899325943179538432"},
	{"LTC", "899326048590766100"},
	{"MAJ", "899326126936190986"},
	{"CPT", "899326238685024267"},
	{"1LT", "899326360185610271"},
	{"2LT", "899326460974759966"},
	{"CW5", "899326766664003604"},
	{"CW4", "899326840487940137"},
	{"CW3", "899326922381746206"},
	{"CW2", "899327005122764852"},
	{"WO1", "899327096697024572"},
	{"CSM", "879186756937846796"},
	{"SGM", "899327773091459213"},
	{"1SG", "899327878615957535"},
	{"MSG", "899328027538907216"},
	{"SFC", "899328106366660638"},
	{"SSG", "899328187820044359"},
	{"SGT", "899328273752928318"},
	{"CPL", "899328353511813160"},
	{"SPC", "899328418766815283"},
	{"PFC", "899328498013966417"},
	{"PVT", "899328617081864202"},
	{"AR", "899328738335027250"},
	{"RCT", "899328824871882752"},
}

// noRankIndex sorts after every real entry, so a member holding no rank role is
// the lowest priority. (A slice length is not a constant expression, so this is
// a var.)
var noRankIndex = len(tempVCRankRoles)

// rankRoleIndex maps a role ID to its seniority index, derived from the ordered
// ladder so that slice is the single source of truth. Blank rank IDs (not yet
// configured) are skipped.
var rankRoleIndex = func() map[string]int {
	idx := make(map[string]int, len(tempVCRankRoles))
	for i, rr := range tempVCRankRoles {
		if rr.roleID != "" {
			idx[rr.roleID] = i
		}
	}
	return idx
}()

// lowestRoleIndex returns the smallest index among the member's roles that appear
// in idx, or fallback when the member holds none of them (or is nil).
func lowestRoleIndex(m *discordgo.Member, idx map[string]int, fallback int) int {
	best := fallback
	if m == nil {
		return best
	}
	for _, r := range m.Roles {
		if i, ok := idx[r]; ok && i < best {
			best = i
		}
	}
	return best
}

// TempVCManager is the subset of *discordgo.Session the temp-VC lifecycle
// uses. Command code depends on this interface so tests can substitute a fake
// that records calls and injects per-call errors without touching the live
// Discord gateway, the same seam pattern as GuildManager (/warden) and
// InteractionResponder (utils/discord_responder.go). The audit-log reason is a
// parameter because tests assert it; the other discordgo request options are
// the production adapter's alone.
type TempVCManager interface {
	// Channel reads a channel from discordgo's state cache, never the API. The
	// hub channel's parent and overwrite list are read here at each spawn.
	Channel(channelID string) (*discordgo.Channel, error)
	// GuildChannelCreateComplex creates a channel with the given audit-log
	// reason and no retry on rate limit.
	GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData, auditReason string) (*discordgo.Channel, error)
	// ChannelDelete deletes a channel with the given audit-log reason and no
	// retry on rate limit.
	ChannelDelete(channelID, auditReason string) (*discordgo.Channel, error)
	// GuildMemberMove moves a member between voice channels with no retry on
	// rate limit.
	GuildMemberMove(guildID, userID string, channelID *string) error
	// ChannelMessageSendComplex posts a message to a channel's text chat with
	// no retry on rate limit. The allowed mentions travel in data, so a test
	// can read who a message may ping.
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error)
	// GuildMember fetches one guild member from the API. The startup
	// Administrator check reads the bot's own member through it.
	GuildMember(guildID, userID string) (*discordgo.Member, error)
	// Guild fetches the guild from the API, roles included. A member carries
	// role IDs only, so the permission bits come from here.
	Guild(guildID string) (*discordgo.Guild, error)
}

// sessionTempVCManager adapts *discordgo.Session to TempVCManager. Each
// method is a one-line pass-through; keeping it trivial means the
// (hard-to-unit-test) wrapper adds negligible uncovered surface. Every REST
// call passes WithRetryOnRatelimit(false): a 429 is a failure the caller
// handles, never a sleeping gateway handler.
type sessionTempVCManager struct {
	s *discordgo.Session
}

// NewSessionTempVCManager wraps a real Discord session for production use.
func NewSessionTempVCManager(s *discordgo.Session) TempVCManager {
	return &sessionTempVCManager{s: s}
}

func (m *sessionTempVCManager) Channel(channelID string) (*discordgo.Channel, error) {
	return m.s.State.Channel(channelID)
}

func (m *sessionTempVCManager) GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData, auditReason string) (*discordgo.Channel, error) {
	return m.s.GuildChannelCreateComplex(guildID, data,
		discordgo.WithAuditLogReason(auditReason), discordgo.WithRetryOnRatelimit(false))
}

func (m *sessionTempVCManager) ChannelDelete(channelID, auditReason string) (*discordgo.Channel, error) {
	return m.s.ChannelDelete(channelID,
		discordgo.WithAuditLogReason(auditReason), discordgo.WithRetryOnRatelimit(false))
}

func (m *sessionTempVCManager) GuildMemberMove(guildID, userID string, channelID *string) error {
	return m.s.GuildMemberMove(guildID, userID, channelID, discordgo.WithRetryOnRatelimit(false))
}

func (m *sessionTempVCManager) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	return m.s.ChannelMessageSendComplex(channelID, data, discordgo.WithRetryOnRatelimit(false))
}

func (m *sessionTempVCManager) GuildMember(guildID, userID string) (*discordgo.Member, error) {
	return m.s.GuildMember(guildID, userID, discordgo.WithRetryOnRatelimit(false))
}

func (m *sessionTempVCManager) Guild(guildID string) (*discordgo.Guild, error) {
	return m.s.Guild(guildID, discordgo.WithRetryOnRatelimit(false))
}

// TempVC holds the feature's runtime state. All maps are guarded by mu:
// discordgo dispatches each gateway event on its own goroutine (SyncEvents is
// false by default), and the panel's service layer calls ApplyHub and
// RemoveHub from HTTP handler goroutines.
type TempVC struct {
	mgr     TempVCManager
	st      store.Store
	guildID string

	mu sync.Mutex
	// hubs maps a hub channel ID -> its settings. Loaded from the store at
	// startup and changed in-process by ApplyHub and RemoveHub.
	hubs map[string]store.Hub
	// userChannel tracks every member's current voice channel (any channel,
	// not just spawned ones) so a VOICE_STATE_UPDATE can be diffed into a
	// leave + join without relying on discordgo's state cache. Seeded from
	// GUILD_CREATE voice states, then maintained from events.
	userChannel map[string]string
	// occupants tracks membership per spawned channel. Presence of a key is
	// what marks a channel as spawned.
	occupants map[string]map[string]struct{}
	// owners maps spawned channel ID -> owner user ID. Absent when the channel
	// has no owner.
	owners map[string]string
	// channelHub maps a spawned channel ID -> the surrogate ID of the hub row
	// it was spawned from. Zero when the hub row is gone. Per-hub numbering
	// sees every live channel of a hub through it.
	channelHub map[string]int64
	// channelIndex maps a spawned channel ID -> the number in its name.
	channelIndex map[string]int
	// deleting marks spawned channels whose delete call the bot has in
	// flight. It decides one thing only: whether the CHANNEL_DELETE handler
	// logs a hand delete. Tracking and rows are handled the same either way.
	deleting map[string]struct{}
	// pending holds, per hub row ID, the numbers reserved for creates in
	// flight. The create is a network call made outside the lock, so without
	// this two members joining one hub at the same moment would both take the
	// smallest unused number.
	pending map[int64]map[int]struct{}
	// memberRank caches each seen member's index on the rank ladder, read
	// from the member object a gateway event carries, so a handover ranks
	// occupants without a REST call. Keyed by user ID; a member holding no
	// rank role is noRankIndex.
	memberRank map[string]int
	// lastFailure holds each hub's last spawn failure, keyed by hub row ID.
	// Absent once a spawn from that hub succeeds. Never written to the store.
	lastFailure map[int64]SpawnFailure
	// createCaptured marks the hubs whose current streak of spawn failures
	// has already reached Sentry. The first failure captures; the next capture
	// waits for a successful spawn from that hub, so a day-long break is one
	// event and not one per join. Keyed by hub row ID.
	createCaptured map[int64]struct{}
	// deleteCaptured is the same rule for delete failures: the first 403,
	// 5xx or transport failure on a hub's channel captures, and a successful
	// delete of one of that hub's channels opens the next. Keyed by hub row
	// ID, zero for a channel whose hub row is gone.
	deleteCaptured map[int64]struct{}
}

// newTempVC builds the runtime state around a manager and a store and loads
// the guild's hubs from the store. A store that cannot list them is an error:
// the bot must not run with no hubs when hubs exist.
func newTempVC(mgr TempVCManager, st store.Store, guildID string) (*TempVC, error) {
	t := &TempVC{
		mgr:            mgr,
		st:             st,
		guildID:        guildID,
		hubs:           make(map[string]store.Hub),
		userChannel:    make(map[string]string),
		occupants:      make(map[string]map[string]struct{}),
		owners:         make(map[string]string),
		channelHub:     make(map[string]int64),
		channelIndex:   make(map[string]int),
		pending:        make(map[int64]map[int]struct{}),
		deleting:       make(map[string]struct{}),
		memberRank:     make(map[string]int),
		lastFailure:    make(map[int64]SpawnFailure),
		createCaptured: make(map[int64]struct{}),
		deleteCaptured: make(map[int64]struct{}),
	}
	ctx, cancel := t.storeContext()
	defer cancel()
	hubs, err := st.ListHubs(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("load hubs: %w", err)
	}
	for _, h := range hubs {
		t.ApplyHub(h)
	}
	return t, nil
}

// ApplyHub puts a hub's settings into the runtime, replacing any earlier
// settings for the same hub channel. The startup load and the panel's service
// layer (after its store write succeeds) both come through here, so the
// runtime never polls the store. Live spawned channels keep their names and
// numbers: a changed base string names only channels spawned after it.
func (t *TempVC) ApplyHub(hub store.Hub) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hubs[hub.HubChannelID] = hub
}

// RemoveHub forgets a hub, so a join to its channel spawns nothing. Its live
// spawned channels stay tracked and die when empty, the same as any other.
// The panel's service layer calls this after the hub row is deleted.
func (t *TempVC) RemoveHub(hubChannelID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.hubs, hubChannelID)
}

// LastSpawnFailure reports a hub's last spawn failure since the last
// successful spawn from it, or false when there is none. The panel's hub list
// reads it per hub row ID.
func (t *TempVC) LastSpawnFailure(hubID int64) (SpawnFailure, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, ok := t.lastFailure[hubID]
	return f, ok
}

// Owner reports who owns a spawned channel: the owner's user ID, or empty
// when the channel has none, and whether the channel is a spawned channel the
// runtime tracks at all. A hub channel or any other channel is untracked. The
// rename command reads it to decide whether the invoker may rename.
func (t *TempVC) Owner(channelID string) (userID string, tracked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.occupants[channelID]; !ok {
		return "", false
	}
	return t.owners[channelID], true
}

// recordSpawnFailure notes why a hub's spawn just failed, stamped with the
// clock. Each failure replaces the last.
func (t *TempVC) recordSpawnFailure(hubID int64, cause SpawnFailureCause) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastFailure[hubID] = SpawnFailure{At: tempVCNow(), Cause: cause}
}

// captureOncePerStreak sends a failure to Sentry unless the hub's streak in
// the given set has already captured, and marks it captured. The set is
// createCaptured or deleteCaptured; the caller clears the hub's entry on the
// success that ends the streak.
func (t *TempVC) captureOncePerStreak(captured map[int64]struct{}, hubID int64, msg string, err error, kv ...any) {
	t.mu.Lock()
	_, done := captured[hubID]
	captured[hubID] = struct{}{}
	t.mu.Unlock()
	if !done {
		captureError(msg, err, kv...)
	}
}

// storeContext bounds one store call.
func (t *TempVC) storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), tempVCStoreTimeout)
}

// StartTempVC wires the temp voice channel feature onto a Discord session: a
// GUILD_CREATE handler that seeds occupancy and runs the restart sweep (fires
// on initial connect and again on any reconnect), and a VOICE_STATE_UPDATE
// handler that drives the create, cleanup and ownership lifecycle. Call
// before dg.Open(). The returned runtime is what the panel's service layer
// applies hub saves to.
func StartTempVC(dg *discordgo.Session, guildID string, st store.Store) (*TempVC, error) {
	t, err := newTempVC(NewSessionTempVCManager(dg), st, guildID)
	if err != nil {
		return nil, err
	}

	t.mu.Lock()
	hubCount := len(t.hubs)
	t.mu.Unlock()
	utils.Info("Starting temp voice channels", "hubs", hubCount)

	dg.AddHandler(func(_ *discordgo.Session, g *discordgo.GuildCreate) {
		defer utils.RecoverPanic("tempvc-guild-create")
		t.handleGuildCreate(g)
	})
	dg.AddHandler(func(_ *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		defer utils.RecoverPanic("tempvc-voice-state")
		t.handleVoiceStateUpdate(vs)
	})
	dg.AddHandler(func(_ *discordgo.Session, c *discordgo.ChannelDelete) {
		defer utils.RecoverPanic("tempvc-channel-delete")
		t.handleChannelDelete(c)
	})
	return t, nil
}

// handleChannelDelete untracks a spawned channel deleted outside the bot (an
// admin in Discord's UI) and deletes its row, freeing its number. It acts on
// tracked spawned channels only: a hub channel is never tracked here, so its
// deletion changes nothing and the hub row stays for the panel to show as
// broken. The bot's own deletes untrack before this fires, so the handler is
// a no-op for them.
func (t *TempVC) handleChannelDelete(c *discordgo.ChannelDelete) {
	if c == nil || c.Channel == nil || c.GuildID != t.guildID {
		return
	}
	t.mu.Lock()
	_, tracked := t.occupants[c.ID]
	_, byBot := t.deleting[c.ID]
	if tracked {
		t.untrackLocked(c.ID)
	}
	t.mu.Unlock()
	if !tracked {
		return
	}
	t.deleteRow(c.ID)
	// The gateway event for the bot's own delete can land before the delete
	// call returns; that path logs the delete itself.
	if !byBot {
		utils.Info("Temp VC spawned channel deleted by hand, untracked", "channel_id", c.ID)
	}
}

// handleGuildCreate seeds voice-state tracking from the GUILD_CREATE payload
// and runs the restart sweep over the stored rows. A row whose channel is
// absent from the guild loses its row. A present, empty channel is deleted
// with its row. A present, occupied channel is tracked again with its number
// from the row and its owner restored from the row; an owner absent from the
// channel counts as having left, and the handover rule elects from the
// occupants, whose ranks come from the payload's member list. No channel
// without a row is touched, so a channel a human made is never deleted.
// GUILD_CREATE re-fires on gateway reconnects, so this also rebuilds
// tracking after any missed events; a restored owner posts no notice, so a
// reconnect is silent.
//
// A store that cannot list the rows leaves the in-memory state as it was: on
// first connect that is empty, and on a reconnect it is the tracking from
// before the disconnect. The next GUILD_CREATE retries.
func (t *TempVC) handleGuildCreate(g *discordgo.GuildCreate) {
	if g.ID != t.guildID {
		return
	}

	ctx, cancel := t.storeContext()
	rows, err := t.st.ListSpawnedChannels(ctx)
	cancel()
	if err != nil {
		captureError("Temp VC restart sweep could not list spawned channels", err, "guild_id", g.ID)
		return
	}

	present := make(map[string]struct{}, len(g.Channels))
	for _, ch := range g.Channels {
		present[ch.ID] = struct{}{}
	}
	occupantsOf := make(map[string]map[string]struct{})
	for _, vs := range g.VoiceStates {
		if vs.ChannelID == "" {
			continue
		}
		if occupantsOf[vs.ChannelID] == nil {
			occupantsOf[vs.ChannelID] = make(map[string]struct{})
		}
		occupantsOf[vs.ChannelID][vs.UserID] = struct{}{}
	}

	// Compute the sweep under the lock, but issue deletes after releasing
	// it, ChannelDelete is a network call.
	t.mu.Lock()
	t.userChannel = make(map[string]string)
	t.occupants = make(map[string]map[string]struct{})
	t.owners = make(map[string]string)
	t.channelHub = make(map[string]int64)
	t.channelIndex = make(map[string]int)
	// pending is left alone: a create in flight across a reconnect still
	// holds its number and releases it itself. memberRank is kept and
	// overlaid from the payload: a rank learnt before the reconnect is
	// still a rank.

	// Ranks come from the payload's member list, and from any voice state
	// that carries a member, so a handover at the sweep needs no REST fetch.
	for _, m := range g.Members {
		if m != nil && m.User != nil {
			t.rememberMemberLocked(m.User.ID, m)
		}
	}
	for _, vs := range g.VoiceStates {
		if vs.ChannelID != "" {
			t.userChannel[vs.UserID] = vs.ChannelID
		}
		if vs.Member != nil {
			t.rememberMemberLocked(vs.UserID, vs.Member)
		}
	}

	var gone, empty []string
	var handovers []store.SpawnedChannel
	for _, row := range rows {
		if _, ok := present[row.ChannelID]; !ok {
			gone = append(gone, row.ChannelID)
			continue
		}
		occ := occupantsOf[row.ChannelID]
		if occ == nil {
			occ = make(map[string]struct{})
		}
		// Empty channels are tracked too, so deleteIfStillEmpty applies the
		// same rules as a live empty event: a failed delete keeps the channel
		// and its row for the next attempt.
		t.occupants[row.ChannelID] = occ
		t.channelHub[row.ChannelID] = row.HubID
		t.channelIndex[row.ChannelID] = row.Number
		if row.OwnerUserID != "" {
			t.owners[row.ChannelID] = row.OwnerUserID
		}
		if len(occ) == 0 {
			empty = append(empty, row.ChannelID)
			continue
		}
		// The row's owner keeps the channel when present. An absent owner
		// counts as having left, and the handover rule elects from the
		// occupants.
		if handover, changed := t.reconcileOwnerLocked(row.ChannelID); changed {
			handovers = append(handovers, handover)
		}
	}
	t.mu.Unlock()

	for _, id := range gone {
		t.deleteRow(id)
	}
	for _, id := range empty {
		t.deleteIfStillEmpty(id)
	}
	for _, row := range handovers {
		t.applyHandover(row)
	}

	utils.Info("Temp VC restart sweep complete",
		"rows", len(rows), "gone", len(gone), "empty", len(empty),
		"tracked", len(rows)-len(gone)-len(empty))
}

// handleVoiceStateUpdate diffs a member's voice move into a leave + join and
// runs the spawned-channel lifecycle on each side.
func (t *TempVC) handleVoiceStateUpdate(vs *discordgo.VoiceStateUpdate) {
	if vs.GuildID != t.guildID {
		return
	}

	t.mu.Lock()
	// Refresh the acting member's cached rank whenever the gateway gives us
	// their member object, so elections read current data.
	if vs.Member != nil {
		t.rememberMemberLocked(vs.UserID, vs.Member)
	}
	oldChannel := t.userChannel[vs.UserID]
	newChannel := vs.ChannelID
	if newChannel == "" {
		delete(t.userChannel, vs.UserID)
	} else {
		t.userChannel[vs.UserID] = newChannel
	}

	if oldChannel == newChannel {
		// Mute/deafen/stream toggles arrive as voice state updates too;
		// nothing moved, nothing to do.
		t.mu.Unlock()
		return
	}

	_, leftSpawned := t.occupants[oldChannel]
	emptied := t.applyLeaveLocked(vs.UserID, oldChannel)
	joinedSpawned := t.applyJoinLocked(vs.UserID, newChannel)
	// The handover rule runs on both sides of the move: the owner may have
	// left, or a rank holder may have joined a channel with no owner. Each
	// change is a row write and a notice, both network calls made off-lock.
	var handovers []store.SpawnedChannel
	if leftSpawned {
		if row, changed := t.reconcileOwnerLocked(oldChannel); changed {
			handovers = append(handovers, row)
		}
	}
	if joinedSpawned {
		if row, changed := t.reconcileOwnerLocked(newChannel); changed {
			handovers = append(handovers, row)
		}
	}
	// The hub lookup happens under the lock because ApplyHub and RemoveHub
	// change the map from other goroutines.
	hub, isHub := t.hubs[newChannel]
	t.mu.Unlock()

	for _, row := range handovers {
		t.applyHandover(row)
	}

	// The vacated channel emptied: delete it now. Off-lock, the delete is a
	// network call.
	if emptied {
		t.deleteIfStillEmpty(oldChannel)
	}

	// Creating happens only when the user joined a hub they are not already
	// tracked inside as a spawned channel.
	if joinedSpawned || !isHub {
		return
	}
	t.handleHubJoin(vs, hub)
}

// applyLeaveLocked removes the user from a spawned channel's occupancy and
// reports whether that emptied it. The caller deletes an emptied channel
// off-lock. Caller holds mu.
func (t *TempVC) applyLeaveLocked(userID, channelID string) bool {
	members, ok := t.occupants[channelID]
	if !ok {
		return false
	}
	delete(members, userID)
	return len(members) == 0
}

// applyJoinLocked adds the user to a spawned channel's occupancy. Returns
// whether the joined channel is spawned. Caller holds mu.
func (t *TempVC) applyJoinLocked(userID, channelID string) bool {
	members, ok := t.occupants[channelID]
	if !ok {
		return false
	}
	members[userID] = struct{}{}
	return true
}

// rememberMemberLocked caches a member's rank index, read from their roles,
// for later handovers. Caller holds mu.
func (t *TempVC) rememberMemberLocked(userID string, m *discordgo.Member) {
	t.memberRank[userID] = lowestRoleIndex(m, rankRoleIndex, noRankIndex)
}

// rankLocked returns a member's cached rank index, or noRankIndex when no
// gateway payload has carried their member object, so an occupant of unknown
// rank is never a candidate. Caller holds mu.
func (t *TempVC) rankLocked(userID string) int {
	if r, ok := t.memberRank[userID]; ok {
		return r
	}
	return noRankIndex
}

// reconcileOwnerLocked applies the handover rule to a spawned channel whose
// occupancy just changed. When the owner changed it returns the channel's
// row with the new owner, empty for none, for the caller to write off-lock
// with the notice. A present owner keeps the channel. Otherwise the
// highest-ranked occupant with a rank role takes over, ties to the lowest
// user ID, and with no such occupant the channel has no owner. An untracked
// or empty channel changes nothing: an emptied one is about to be deleted.
// Caller holds mu.
func (t *TempVC) reconcileOwnerLocked(channelID string) (row store.SpawnedChannel, changed bool) {
	occ, tracked := t.occupants[channelID]
	if !tracked || len(occ) == 0 {
		return row, false
	}
	current, has := t.owners[channelID]
	if _, present := occ[current]; has && present {
		return row, false
	}
	elected := t.electOwnerLocked(occ)
	if elected == "" {
		delete(t.owners, channelID)
	} else {
		t.owners[channelID] = elected
	}
	if elected == current {
		return row, false
	}
	return store.SpawnedChannel{
		ChannelID:   channelID,
		HubID:       t.channelHub[channelID],
		Number:      t.channelIndex[channelID],
		OwnerUserID: elected,
	}, true
}

// electOwnerLocked picks the occupant the handover rule names: the highest
// rank on the ladder, then the lowest user ID. An occupant holding no rank
// role, or whose roles no payload has carried, is not a candidate. Empty
// means there is none. Caller holds mu.
func (t *TempVC) electOwnerLocked(occ map[string]struct{}) string {
	best, bestRank := "", noRankIndex
	for uid := range occ {
		rank := t.rankLocked(uid)
		if rank >= noRankIndex {
			continue
		}
		if best == "" || rank < bestRank || (rank == bestRank && lowerUserID(uid, best)) {
			best, bestRank = uid, rank
		}
	}
	return best
}

// lowerUserID reports whether user ID a is the lower one, as a number. IDs
// are decimal strings with no leading zero, so a shorter one is smaller and
// equal lengths compare as strings. 17, 18 and 19 digit IDs all exist on the
// guild, so a plain string compare would rank a newer 18 digit account below
// an older 17 digit one.
func lowerUserID(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// recordOwnership writes a spawned channel's row and posts the ownership
// notice in its chat, off-lock, at create and at every handover. The upsert
// at a handover also heals a channel whose create write failed. A failed
// write captures under captureMsg and the channel stays tracked with its
// live owner; docs/temp-vc-decisions.md accepts that a restart may then not
// find it.
func (t *TempVC) recordOwnership(row store.SpawnedChannel, captureMsg string) {
	ctx, cancel := t.storeContext()
	defer cancel()
	if err := t.st.UpsertSpawnedChannel(ctx, row); err != nil {
		captureError(captureMsg, err, "channel_id", row.ChannelID, "hub_id", row.HubID)
	}
	t.postOwnershipNotice(row.ChannelID, row.OwnerUserID)
}

// applyHandover records an ownership change and logs it.
func (t *TempVC) applyHandover(row store.SpawnedChannel) {
	t.recordOwnership(row, "Temp VC handover row write failed")
	utils.Info("Temp VC handover", "channel_id", row.ChannelID, "hub_id", row.HubID, "owner_id", row.OwnerUserID)
}

// untrackLocked forgets a spawned channel. Caller holds mu.
func (t *TempVC) untrackLocked(channelID string) {
	delete(t.occupants, channelID)
	delete(t.owners, channelID)
	delete(t.channelHub, channelID)
	delete(t.channelIndex, channelID)
}

// deleteIfStillEmpty deletes a spawned channel that just emptied. It re-checks
// occupancy under the lock first: the leave that emptied the channel was
// applied under the lock, but a join can land in the gap before this runs.
func (t *TempVC) deleteIfStillEmpty(channelID string) {
	t.mu.Lock()
	members, tracked := t.occupants[channelID]
	if !tracked || len(members) > 0 {
		t.mu.Unlock()
		return
	}
	// Tracking is kept until Discord confirms the delete: a channel that fails
	// to delete is still live, and the next empty event or the restart sweep
	// retries it.
	t.deleting[channelID] = struct{}{}
	t.mu.Unlock()

	_, err := t.mgr.ChannelDelete(channelID, "empty")
	switch fault := classifySpawnedChannelError(err); {
	case err == nil:
		utils.Info("Temp VC deleted", "channel_id", channelID)
		t.mu.Lock()
		delete(t.deleteCaptured, t.channelHub[channelID])
		t.mu.Unlock()
	case fault.gone:
		// Already gone: someone deleted it by hand while the leave event was
		// in flight. Nothing failed, so nothing is captured.
		utils.Info("Temp VC channel already gone, untracked", "channel_id", channelID)
	default:
		// The channel is still live: it stays tracked with its row, and the
		// next empty event or the restart sweep retries. A 429 or any other
		// 4xx is this WARN line only. A 403, 5xx or transport failure also
		// captures, once per streak per hub.
		t.mu.Lock()
		delete(t.deleting, channelID)
		hubID := t.channelHub[channelID]
		t.mu.Unlock()
		utils.Warn("Temp VC delete failed, channel kept for the next attempt",
			"channel_id", channelID, "hub_id", hubID, "error", err)
		if fault.capturesOnDelete() {
			t.captureOncePerStreak(t.deleteCaptured, hubID, "Temp VC delete failed", err,
				"channel_id", channelID, "hub_id", hubID, "guild_id", t.guildID)
		}
		return
	}

	t.mu.Lock()
	t.untrackLocked(channelID)
	delete(t.deleting, channelID)
	t.mu.Unlock()
	t.deleteRow(channelID)
}

// deleteRow removes a spawned channel's row once the channel is gone from
// Discord. A store failure is captured; the row then lingers until the next
// restart sweep finds its channel absent and deletes it.
func (t *TempVC) deleteRow(channelID string) {
	ctx, cancel := t.storeContext()
	defer cancel()
	if err := t.st.DeleteSpawnedChannel(ctx, channelID); err != nil {
		captureError("Temp VC row delete failed", err, "channel_id", channelID)
	}
}

// handleHubJoin creates a spawned channel for a hub joiner and moves them into
// it. Runs without the lock held, creation and moves are network calls, and
// re-locks only to commit tracking state.
func (t *TempVC) handleHubJoin(vs *discordgo.VoiceStateUpdate, hub store.Hub) {
	// A disabled hub keeps its channel and its settings and ignores joins.
	if !hub.Enabled {
		return
	}

	// The category is never stored: it is the hub channel's parent as
	// discordgo's state cache has it now, so moving the hub channel in Discord
	// moves spawning with it.
	hubChannel, err := t.mgr.Channel(hub.HubChannelID)
	if err != nil {
		utils.Warn("Temp VC spawn refused, hub channel not in the state cache",
			"hub_channel_id", hub.HubChannelID, "user_id", vs.UserID, "error", err)
		return
	}
	if hubChannel.ParentID == "" {
		// The bot's own refusal: a WARN line and a note for the panel, never
		// a Sentry event and no message to the member.
		t.recordSpawnFailure(hub.ID, SpawnFailureNoCategory)
		utils.Warn("Temp VC spawn refused, hub channel has no category",
			"hub_channel_id", hub.HubChannelID, "user_id", vs.UserID)
		return
	}

	// Every channel a hub spawns is "<base string> - <n>", n being the
	// smallest number no live or in-flight channel of that hub holds (see
	// nextChannelIndexLocked), NOT the channel count, which collides after a
	// lower-numbered channel is deleted. The number is reserved here and
	// released on every exit below.
	t.mu.Lock()
	index := t.reserveChannelIndexLocked(hub.ID)
	t.mu.Unlock()
	name := nameWithIndex(hub.BaseString, index)

	// Permission source category: no PermissionOverwrites, so Discord copies
	// the category's and the channel is synced from birth. Permission source
	// hub_channel: the hub channel's own list, verbatim, for a stricter hub
	// inside a looser category. The bot adds no entry of its own either way.
	data := discordgo.GuildChannelCreateData{
		Name:      name,
		Type:      discordgo.ChannelTypeGuildVoice,
		ParentID:  hubChannel.ParentID,
		UserLimit: hub.UserLimit,
		Bitrate:   hub.Bitrate,
	}
	if hub.PermissionSource == store.PermissionHubChannel {
		data.PermissionOverwrites = hubChannel.PermissionOverwrites
	}
	reason := fmt.Sprintf("hub %s, creator %s", hub.HubChannelID, vs.UserID)
	channel, err := t.mgr.GuildChannelCreateComplex(t.guildID, data, reason)
	if err != nil {
		t.mu.Lock()
		t.releaseChannelIndexLocked(hub.ID, index)
		t.mu.Unlock()
		fault := classifySpawnedChannelError(err)
		t.recordSpawnFailure(hub.ID, fault.cause())
		utils.Warn("Temp VC create failed",
			"user_id", vs.UserID, "hub_channel_id", hub.HubChannelID, "cause", fault.cause(), "error", err)
		if fault.capturesOnCreate() {
			t.captureOncePerStreak(t.createCaptured, hub.ID, "Temp VC create failed", err,
				"user_id", vs.UserID, "hub_channel_id", hub.HubChannelID, "guild_id", t.guildID)
		}
		t.messageHubJoiner(hub, vs.UserID, fault.full)
		return
	}

	// The creator owns the channel only when they hold a rank role; a channel
	// a member with no rank role created starts with no owner, and the first
	// rank holder to join takes it. The creator counts as an occupant from
	// here: the bot is about to move them in, and their own voice state event
	// lands a moment after the move, so a rank holder who joins in that window
	// must not find the owner absent and take the channel for good.
	t.mu.Lock()
	owner := ""
	if t.rankLocked(vs.UserID) < noRankIndex {
		owner = vs.UserID
	}
	t.occupants[channel.ID] = map[string]struct{}{vs.UserID: {}}
	if owner != "" {
		t.owners[channel.ID] = owner
	}
	t.channelHub[channel.ID] = hub.ID
	t.channelIndex[channel.ID] = index
	t.releaseChannelIndexLocked(hub.ID, index)
	t.mu.Unlock()

	if err := t.mgr.GuildMemberMove(t.guildID, vs.UserID, &channel.ID); err != nil {
		// The member vanished (disconnected mid-create) or the move was
		// refused. A create that succeeds and a move-into that fails is one
		// spawn failure: the new channel is deleted at once through the
		// empty-channel path, and the member hears about it only when the
		// runtime's own occupancy map still has them in the hub. A member
		// who left needs no ping about a channel that never was.
		t.recordSpawnFailure(hub.ID, SpawnFailureMove)
		utils.Warn("Temp VC move-into failed, deleting channel",
			"user_id", vs.UserID, "channel_id", channel.ID, "hub_channel_id", hub.HubChannelID, "error", err)
		if classifySpawnedChannelError(err).capturesOnCreate() {
			t.captureOncePerStreak(t.createCaptured, hub.ID, "Temp VC move-into failed, deleting channel", err,
				"user_id", vs.UserID, "channel_id", channel.ID, "hub_channel_id", hub.HubChannelID)
		}
		t.mu.Lock()
		stillInHub := t.userChannel[vs.UserID] == hub.HubChannelID
		// The creator never arrived: they leave the occupancy so the
		// empty-channel path sees the channel for what it is.
		delete(t.occupants[channel.ID], vs.UserID)
		t.mu.Unlock()
		t.deleteIfStillEmpty(channel.ID)
		if stillInHub {
			t.messageHubJoiner(hub, vs.UserID, false)
		}
		return
	}

	// The spawn succeeded: the hub's last failure is cleared and its next
	// Discord-side failure will capture again.
	t.mu.Lock()
	delete(t.lastFailure, hub.ID)
	delete(t.createCaptured, hub.ID)
	t.mu.Unlock()

	t.recordOwnership(
		store.SpawnedChannel{ChannelID: channel.ID, HubID: hub.ID, Number: index, OwnerUserID: owner},
		"Temp VC row write failed")

	utils.Info("Temp VC created",
		"channel_id", channel.ID, "name", name, "number", index,
		"hub_channel_id", hub.HubChannelID, "hub_id", hub.ID, "owner_id", owner)
}

// postOwnershipNotice posts the ownership notice in a spawned channel's text
// chat: one message naming the owner by mention, or saying nobody owns it.
// The allowed mentions parse nothing, so the mention renders and pings
// nobody. The voice channel status line is not used. A failed send is a WARN
// line: the ownership change itself has already happened.
func (t *TempVC) postOwnershipNotice(channelID, owner string) {
	content := "Nobody owns this channel."
	if owner != "" {
		content = fmt.Sprintf("<@%s> owns this channel.", owner)
	}
	_, err := t.mgr.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	})
	if err != nil {
		utils.Warn("Temp VC ownership notice not sent",
			"channel_id", channelID, "owner_id", owner, "error", err)
	}
}

// messageHubJoiner tells a member whose channel could not be created, in the
// hub channel's text chat. The content mentions them and the allowed mentions
// name them alone, so nobody else is pinged. No DM, no disconnect, and the
// bot never deletes the message. Two texts only: the category is full, or
// the error has been reported to S6. A failed send is a WARN line: the spawn failure
// itself has already been handled.
func (t *TempVC) messageHubJoiner(hub store.Hub, userID string, full bool) {
	content := fmt.Sprintf("<@%s> Cavbot could not create your channel. The error has been reported to S6.", userID)
	if full {
		content = fmt.Sprintf("<@%s> this category is full. Wait for a channel to empty, then join again.", userID)
	}
	_, err := t.mgr.ChannelMessageSendComplex(hub.HubChannelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{Users: []string{userID}},
	})
	if err != nil {
		utils.Warn("Temp VC spawn failure message not sent",
			"hub_channel_id", hub.HubChannelID, "user_id", userID, "error", err)
	}
}

// nameWithIndex renders "<base> - <n>", first truncating the base so the whole
// result still fits Discord's channel-name limit, counted in characters. The
// number is always preserved (the base is what gets cut). The panel bounds a
// base string at 90 characters, so this is a guard for rows written by hand.
func nameWithIndex(base string, n int) string {
	suffix := fmt.Sprintf(" - %d", n)
	if runes := []rune(base); len(runes)+len(suffix) > discordChannelNameLimit {
		base = string(runes[:discordChannelNameLimit-len(suffix)])
	}
	return base + suffix
}

// nextChannelIndexLocked returns the number for a hub's new channel: the
// smallest positive integer no live or in-flight channel of that hub holds.
// Deriving it from the set actually in use, rather than the channel count, is
// what prevents a freed lower number from colliding with a surviving higher
// one: deleting "- 1" then creating another yields "- 1" again, never a second
// "- 4". Caller holds mu.
func (t *TempVC) nextChannelIndexLocked(hubID int64) int {
	used := make(map[int]bool)
	for id, n := range t.channelIndex {
		if t.channelHub[id] == hubID {
			used[n] = true
		}
	}
	for n := range t.pending[hubID] {
		used[n] = true
	}
	for n := 1; ; n++ {
		if !used[n] {
			return n
		}
	}
}

// reserveChannelIndexLocked takes the next number for a hub and marks it in
// flight until releaseChannelIndexLocked. Caller holds mu.
func (t *TempVC) reserveChannelIndexLocked(hubID int64) int {
	n := t.nextChannelIndexLocked(hubID)
	if t.pending[hubID] == nil {
		t.pending[hubID] = make(map[int]struct{})
	}
	t.pending[hubID][n] = struct{}{}
	return n
}

// releaseChannelIndexLocked ends a reservation: the number is now either held
// by a tracked channel or free again. Caller holds mu.
func (t *TempVC) releaseChannelIndexLocked(hubID int64, n int) {
	delete(t.pending[hubID], n)
	if len(t.pending[hubID]) == 0 {
		delete(t.pending, hubID)
	}
}
