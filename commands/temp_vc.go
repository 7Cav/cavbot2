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
// owner. It is written after the create and deleted with the channel.
//
// A spawned channel is deleted the moment its last occupant leaves. The
// restart sweep on GUILD_CREATE reads the rows and touches no channel it
// holds no row for.
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

// tempVCStoreTimeout bounds each store call a gateway handler makes, so a
// stalled database never hangs a handler goroutine.
const tempVCStoreTimeout = 5 * time.Second

// Interim-ownership election is decided purely from a member's Discord roles,
// with no nickname parsing. When a channel's creator steps away, the present
// occupants are ranked by, in order:
//
//  1. rank role: their rank (tempVCRankRoles), and
//  2. lowest user ID: a deterministic final tiebreak.
//
// The ladder is ordered most senior first (smaller index = higher priority),
// and a member holding none of its roles sorts below every entry in it.

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
	// controller maps a spawned channel ID -> the member currently holding
	// INTERIM ownership because the creator has stepped out of the channel.
	// Absent when the creator is present or when no eligible member is
	// available.
	controller map[string]string
	// memberMeta caches each seen member's rank seniority, captured from
	// gateway events (which carry the acting member), so an election reads ranks
	// without a REST call per occupant. Keyed by user ID.
	memberMeta map[string]memberRankMeta
}

// memberRankMeta is the cached election input for a member, read from their
// roles: their rank index (smaller = higher; noRankIndex if none).
type memberRankMeta struct {
	rankIdx int
}

// worstMemberMeta is the election input for a member we have never cached (no
// rank role), so an unseen occupant never outranks a known one.
var worstMemberMeta = memberRankMeta{rankIdx: noRankIndex}

// newTempVC builds the runtime state around a manager and a store and loads
// the guild's hubs from the store. A store that cannot list them is an error:
// the bot must not run with no hubs when hubs exist.
func newTempVC(mgr TempVCManager, st store.Store, guildID string) (*TempVC, error) {
	t := &TempVC{
		mgr:          mgr,
		st:           st,
		guildID:      guildID,
		hubs:         make(map[string]store.Hub),
		userChannel:  make(map[string]string),
		occupants:    make(map[string]map[string]struct{}),
		owners:       make(map[string]string),
		channelHub:   make(map[string]int64),
		channelIndex: make(map[string]int),
		controller:   make(map[string]string),
		memberMeta:   make(map[string]memberRankMeta),
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

// storeContext bounds one store call made from a gateway handler.
func (t *TempVC) storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), tempVCStoreTimeout)
}

// StartTempVC wires the temp voice channel feature onto a Discord session: a
// GUILD_CREATE handler that seeds occupancy and runs the restart sweep (fires
// on initial connect and again on any reconnect), and a VOICE_STATE_UPDATE
// handler that drives the create/cleanup/interim-ownership lifecycle. Call
// before dg.Open(). The returned runtime is what the panel's service layer
// applies hub saves to.
func StartTempVC(dg *discordgo.Session, guildID string, st store.Store) (*TempVC, error) {
	t, err := newTempVC(NewSessionTempVCManager(dg), st, guildID)
	if err != nil {
		return nil, err
	}

	utils.Info("Starting temp voice channels", "hubs", len(t.hubs))

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
	if tracked {
		t.untrackLocked(c.ID)
	}
	t.mu.Unlock()
	if !tracked {
		return
	}
	t.deleteRow(c.ID)
	utils.Info("Temp VC channel deleted outside the bot", "channel_id", c.ID)
}

// handleGuildCreate seeds voice-state tracking from the GUILD_CREATE payload
// and runs the restart sweep over the stored rows. A row whose channel is
// absent from the guild loses its row. A present, empty channel is deleted
// with its row. A present, occupied channel is tracked again with its number
// and owner from the row. No channel without a row is touched, so a channel
// a human made is never deleted. GUILD_CREATE re-fires on gateway reconnects,
// so this also resynchronizes tracking after any missed events.
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
	t.controller = make(map[string]string)
	t.memberMeta = make(map[string]memberRankMeta)

	for _, vs := range g.VoiceStates {
		if vs.ChannelID != "" {
			t.userChannel[vs.UserID] = vs.ChannelID
		}
		// Seed the rank cache from any voice state that carries a member, so an
		// election right after reconnect has ranks without a REST fetch.
		if vs.Member != nil {
			t.rememberMemberLocked(vs.UserID, vs.Member)
		}
	}

	var gone, empty []string
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
		t.reconcileControllerLocked(row.ChannelID)
	}
	t.mu.Unlock()

	for _, id := range gone {
		t.deleteRow(id)
	}
	for _, id := range empty {
		t.deleteIfStillEmpty(id)
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
	// Re-elect interim ownership on both sides of the move: leaving may have made
	// a creator absent (hand off) or removed the interim holder (re-elect);
	// joining may have brought the creator back (hand back). Bookkeeping only;
	// ownership changes nothing in Discord.
	if leftSpawned {
		t.reconcileControllerLocked(oldChannel)
	}
	if joinedSpawned {
		t.reconcileControllerLocked(newChannel)
	}
	// The hub lookup happens under the lock because ApplyHub and RemoveHub
	// change the map from other goroutines.
	hub, isHub := t.hubs[newChannel]
	t.mu.Unlock()

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
// for future elections. Caller holds mu.
func (t *TempVC) rememberMemberLocked(userID string, m *discordgo.Member) {
	t.memberMeta[userID] = memberRankMeta{
		rankIdx: lowestRoleIndex(m, rankRoleIndex, noRankIndex),
	}
}

// electInterimControllerLocked picks the member who should hold interim
// ownership of a spawned channel whose creator is currently absent. Every
// occupant is eligible; they are ordered by rank role (the present occupant
// with the highest of tempVCRankRoles wins), then by lowest user ID for
// determinism. Returns "" only when the channel is empty. Caller holds mu.
func (t *TempVC) electInterimControllerLocked(channelID string) string {
	best := ""
	var bestMeta memberRankMeta
	for uid := range t.occupants[channelID] {
		meta := t.metaForLocked(uid)
		if best == "" || outranksForInterim(meta, uid, bestMeta, best) {
			best, bestMeta = uid, meta
		}
	}
	return best
}

// metaForLocked returns a member's cached election input, or worstMemberMeta
// when we have never seen their member object, so an uncached occupant never
// displaces a known candidate. Caller holds mu.
func (t *TempVC) metaForLocked(userID string) memberRankMeta {
	if m, ok := t.memberMeta[userID]; ok {
		return m
	}
	return worstMemberMeta
}

// outranksForInterim reports whether candidate (m, uid) should beat the current
// best (bm, buid) for interim ownership: higher rank, then lower user ID.
func outranksForInterim(m memberRankMeta, uid string, bm memberRankMeta, buid string) bool {
	if m.rankIdx != bm.rankIdx {
		return m.rankIdx < bm.rankIdx
	}
	return uid < buid
}

// reconcileControllerLocked recomputes who should hold interim ownership of a
// spawned channel and records it in controller. The creator keeps their claim;
// interim control applies only while the creator is out of the channel. A
// channel with no recorded owner never gets an interim owner. An emptied
// channel is left alone, it is about to be deleted. Caller holds mu.
func (t *TempVC) reconcileControllerLocked(channelID string) {
	occ, tracked := t.occupants[channelID]
	if !tracked || len(occ) == 0 {
		return
	}

	desired := ""
	creator, hasCreator := t.owners[channelID]
	if _, creatorPresent := occ[creator]; hasCreator && !creatorPresent {
		// Creator is away: elect a stand-in from the (non-empty) occupants.
		desired = t.electInterimControllerLocked(channelID)
	}

	if desired == "" {
		delete(t.controller, channelID)
		return
	}
	t.controller[channelID] = desired
}

// untrackLocked forgets a spawned channel. Caller holds mu.
func (t *TempVC) untrackLocked(channelID string) {
	delete(t.occupants, channelID)
	delete(t.owners, channelID)
	delete(t.channelHub, channelID)
	delete(t.channelIndex, channelID)
	delete(t.controller, channelID)
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
	t.mu.Unlock()

	if _, err := t.mgr.ChannelDelete(channelID, "empty"); err != nil {
		captureError("Temp VC delete failed", err,
			"channel_id", channelID, "guild_id", t.guildID)
		return
	}

	t.mu.Lock()
	t.untrackLocked(channelID)
	t.mu.Unlock()
	t.deleteRow(channelID)
	utils.Info("Temp VC deleted", "channel_id", channelID)
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
	if err != nil || hubChannel.ParentID == "" {
		utils.Warn("Temp VC spawn refused, hub channel has no category",
			"hub_channel_id", hub.HubChannelID, "user_id", vs.UserID, "error", err)
		return
	}

	// Every channel a hub spawns is "<base string> - <n>", n being the
	// smallest number no live channel of that hub holds (see
	// nextChannelIndexLocked), NOT the channel count, which collides after a
	// lower-numbered channel is deleted.
	t.mu.Lock()
	index := t.nextChannelIndexLocked(hub.ID)
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
		captureError("Temp VC create failed", err,
			"user_id", vs.UserID, "hub_channel_id", hub.HubChannelID, "guild_id", t.guildID)
		return
	}

	t.mu.Lock()
	t.occupants[channel.ID] = make(map[string]struct{})
	t.owners[channel.ID] = vs.UserID
	t.channelHub[channel.ID] = hub.ID
	t.channelIndex[channel.ID] = index
	t.mu.Unlock()

	if err := t.mgr.GuildMemberMove(t.guildID, vs.UserID, &channel.ID); err != nil {
		// The user vanished (disconnected mid-create) or the move was
		// refused; without them the new channel would sit empty, so delete
		// it now.
		captureError("Temp VC move-into failed, deleting channel", err,
			"user_id", vs.UserID, "channel_id", channel.ID)
		t.mu.Lock()
		t.untrackLocked(channel.ID)
		t.mu.Unlock()
		if _, delErr := t.mgr.ChannelDelete(channel.ID, "empty"); delErr != nil {
			captureError("Temp VC post-move-failure delete failed", delErr,
				"channel_id", channel.ID)
		}
		return
	}

	ctx, cancel := t.storeContext()
	defer cancel()
	row := store.SpawnedChannel{ChannelID: channel.ID, HubID: hub.ID, Number: index, OwnerUserID: vs.UserID}
	if err := t.st.UpsertSpawnedChannel(ctx, row); err != nil {
		// The channel is live and stays tracked in memory. Without its row a
		// restart will not find it; docs/temp-vc-decisions.md accepts that.
		captureError("Temp VC row write failed", err,
			"channel_id", channel.ID, "hub_channel_id", hub.HubChannelID)
	}

	utils.Info("Temp VC created",
		"channel_id", channel.ID, "name", name, "number", index,
		"hub_channel_id", hub.HubChannelID, "hub_id", hub.ID, "owner_id", vs.UserID)
}

// nameWithIndex renders "<base> - <n>", first truncating the base so the whole
// result still fits Discord's channel-name limit. The number is always
// preserved (the base is what gets cut).
func nameWithIndex(base string, n int) string {
	suffix := fmt.Sprintf(" - %d", n)
	if len(base)+len(suffix) > discordChannelNameLimit {
		base = base[:discordChannelNameLimit-len(suffix)]
	}
	return base + suffix
}

// nextChannelIndexLocked returns the number for a hub's new channel: the
// smallest positive integer no live channel of that hub holds. Deriving it
// from the set actually in use, rather than the channel count, is what
// prevents a freed lower number from colliding with a surviving higher one:
// deleting "- 1" then creating another yields "- 1" again, never a second
// "- 4". Caller holds mu.
func (t *TempVC) nextChannelIndexLocked(hubID int64) int {
	used := make(map[int]bool)
	for id, n := range t.channelIndex {
		if t.channelHub[id] == hubID {
			used[n] = true
		}
	}
	for n := 1; ; n++ {
		if !used[n] {
			return n
		}
	}
}
