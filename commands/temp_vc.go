package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
// service layer applies each save in-process through ApplyHub, RemoveHub and
// ApplyGuildModeratorRoles.
// The spawned channel is named "<base string> - n", numbered per hub from 1
// with freed numbers reused, and carries the hub's user limit and bitrate.
// Permission source "category" sends no overwrites, so Discord copies the
// category's; "hub_channel" copies the hub channel's own overwrite list.
// permissionSourceOverwrites is the one reader of either list. The bot
// writes overwrites of its own only to lock a channel, and an unlock puts
// the source back (temp_vc_lock.go). Ownership is a bot-internal marker that
// grants no Discord permission.
//
// One row per spawned channel lives in the store: channel ID, hub, number,
// owner and lock. It is written at create and at every handover, its lock
// at every lock and unlock, and it is deleted with the channel. Each create
// and each handover also posts the ownership notice in the spawned channel's
// text chat, naming the owner or saying there is none, and pinging nobody.
//
// A spawned channel is deleted the moment its last occupant leaves, which
// ends any lock on it. The restart sweep on GUILD_CREATE reads the rows,
// restores each channel's owner from its row or elects one by the handover
// rule, restores its lock, and touches no channel it holds no row for.
//
// The sweep and the voice handlers run on their own goroutines, and the
// sweep's list call is made off-lock, so a voice event can be handled
// inside the sweep's window. The sweep rebuilds who is where from one
// copied cache snapshot taken under the runtime mutex, not from the payload,
// so an event handled in the window is counted. A hub join marks its fresh
// channel as a spawn in flight (CONTEXT.md) at the tracking commit, until
// its row write or compensating delete finishes; a sweep that overlaps
// leaves that channel alone and skips its row, so a spawn is never lost to
// the rebuild. Sweeps serialize behind one mutex, and the sweep reads the
// payload's slices only for its rank copy, under the state's lock, because
// discordgo shares them with its cache.
//
// A spawn that fails is one create per join: no retry, no backoff, no
// auto-disable. The member gets one message in the hub channel's text chat
// that mentions them alone, no DM and no disconnect. Three texts: the
// category is full; the error has been reported to S6, for a failure Discord
// returned; or only that the channel could not be created, for a refusal the
// bot made itself. The runtime keeps each hub's last spawn failure in memory
// for the panel, cleared by the next successful spawn from that hub. A
// failure Discord returned reaches Sentry once per streak per hub; a refusal
// the bot makes itself, a hub with no category, never does.
// temp_vc_errors.go classifies the Discord errors on these paths.
//
// discordgo applies every gateway event to its state cache in gateway order
// before it starts any handler goroutine, and the handlers run unordered, so
// the runtime's record of who is where can lag Discord: a stale voice state
// (CONTEXT.md). Each decision is therefore checked against one fresh copied
// snapshot of the cache (VoiceSnapshot): a handler drops an event the cache
// already shows superseded, the create and the move-into go out only while
// the member is still in the hub, a delete only while the cache shows nobody
// inside, the hub refusal message only while the member is still there, and
// a handover elects only from recorded occupants the cache confirms. A fresh
// channel whose creator never entered goes through the compensating delete;
// when that is refused the channel stays tracked as an ordinary spawned
// channel. The guarantee is that no handler acts on a decision the cache
// already shows superseded at that check, not full ordering: a newer event
// can still land between a check and the API call. A guild missing from the
// cache counts as current at entry and as "cannot confirm" before an action.
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
// The bits the bot writes of its own accord are lockBits (temp_vc_lock.go),
// which a lock, a guest add and a let-in set or clear, and the build fails
// if lockBits ever holds a bit outside this ceiling. The bits an overwrite
// already carried, and the permission source an unlock copies back, are
// Discord's and pass as they are. Raising it is a decision of its own
// (spec #347, out of scope).
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
	// parent, so there is nowhere to spawn under. No create call goes out;
	// the member gets the refusal text.
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
	// The new channel went through the compensating delete.
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

// GuildMemberRanks reads a GUILD_CREATE guild's rank data into a user ID to
// rank index map, the guild's member list first and the members its voice
// states carry second, by the same rank rule the voice handlers apply. It
// keeps no reference to the guild's members or roles. The production
// adapter calls it under State.RLock, because discordgo shares the
// payload's slices with its state cache; the test fakes call it as it is.
// One helper for all three, so the rank rule cannot diverge.
func GuildMemberRanks(g *discordgo.Guild) map[string]int {
	ranks := make(map[string]int, len(g.Members))
	for _, m := range g.Members {
		if m != nil && m.User != nil {
			ranks[m.User.ID] = lowestRoleIndex(m, rankRoleIndex, noRankIndex)
		}
	}
	for _, vs := range g.VoiceStates {
		if vs != nil && vs.Member != nil {
			ranks[vs.UserID] = lowestRoleIndex(vs.Member, rankRoleIndex, noRankIndex)
		}
	}
	return ranks
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
	// ChannelEdit edits a channel with the given audit-log reason and no retry
	// on rate limit. /voice-rename sends the name alone. An empty overwrite
	// list is dropped from the request (omitempty), so overwrites go through
	// ChannelOverwritesReplace.
	ChannelEdit(channelID string, data *discordgo.ChannelEdit, auditReason string) (*discordgo.Channel, error)
	// ChannelOverwritesReplace replaces a channel's whole overwrite list in
	// one edit, with the given audit-log reason and no retry on rate limit.
	// An empty list reaches Discord as an empty list and clears every
	// overwrite. Once Discord accepts the edit, Channel reads the new list at
	// once, without waiting for Discord's CHANNEL_UPDATE. A lock and an
	// unlock send their edits through it.
	ChannelOverwritesReplace(channelID string, overwrites []*discordgo.PermissionOverwrite, auditReason string) error
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
	// GuildChannels fetches the guild's channel list from the API. The panel
	// reads hub channel names, category names and the register picker from
	// it at each page load.
	GuildChannels(guildID string) ([]*discordgo.Channel, error)
	// VoiceStates reads one copied snapshot of the guild's voice states and
	// channel IDs from discordgo's state cache, never the API. The runtime
	// checks each decision against it (#319) and the restart sweep rebuilds
	// from it (#325).
	VoiceStates(guildID string) VoiceSnapshot
	// MemberRanks reads a GUILD_CREATE guild's rank data into a user ID to
	// rank index map, through GuildMemberRanks. The production adapter
	// reads under the state's lock, because discordgo shares the payload's
	// slices with its cache (#335).
	MemberRanks(g *discordgo.Guild) map[string]int
	// ChannelPermissionSet sets one permission overwrite on a channel, the
	// target's whole overwrite, with the given audit-log reason. It is the
	// one call that retries on a 429, after the wait Discord gives: a guest
	// add has no other retry, and a let-in of 25 members would otherwise
	// drop the rest on a bucket that resets in seconds. Discord replaces the
	// target's existing overwrite, so the caller sends every bit it means to
	// keep. A guest add sends one member overwrite.
	ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64, auditReason string) error
	// ChannelMessageEditComplex edits a message the bot posted, with no
	// retry on rate limit. The edit names its channel and message, and
	// carries the new content and components: a pointer to an empty list
	// removes every component, and a nil one leaves them. The unlock edits
	// the lock notice through it.
	ChannelMessageEditComplex(edit *discordgo.MessageEdit) (*discordgo.Message, error)
	// CanSeeChannel reports whether a member holding the given roles sees a
	// channel: View Channel, as discordgo's state permission calculation
	// gives it over the cached guild roles and channel overwrites. The roles
	// come from the caller, since the cache holds no offline member of a
	// large guild; a user select carries each picked member's. A channel or
	// guild missing from the cache is an error. Let someone in checks it
	// before a guest add.
	CanSeeChannel(channelID, userID string, roles []string) (bool, error)
}

// VoiceSnapshot is one copy of a guild's voice states from discordgo's state
// cache, taken at one instant. discordgo applies every gateway event to the
// cache in gateway order before it starts any handler goroutine, so the
// snapshot is the ordered truth the runtime's own record can lag behind
// (CONTEXT.md, "Stale voice state"). It holds copied IDs only and is never
// carried across an API call: each check takes a fresh one.
type VoiceSnapshot struct {
	// Present is false when the cache holds no guild by that ID, or holds it
	// marked unavailable. A missing guild answers nothing about any member.
	Present bool
	// ChannelByUser maps each connected member's user ID to the channel they
	// are in. A member with no entry is not connected.
	ChannelByUser map[string]string
	// Channels is the guild's channel ID set as the cache holds it, copied
	// under the same lock as the voice states. The restart sweep's gone
	// test reads it: a row whose channel is absent loses its row.
	Channels map[string]struct{}
}

// hasChannel reports whether the snapshot holds the channel. A missing
// guild holds none; callers check Present first.
func (s VoiceSnapshot) hasChannel(channelID string) bool {
	_, ok := s.Channels[channelID]
	return ok
}

// channelOf reports the channel a member is in, or empty when they are not
// connected or the guild is missing.
func (s VoiceSnapshot) channelOf(userID string) string {
	return s.ChannelByUser[userID]
}

// occupied reports whether the snapshot shows anyone in the channel. A
// missing guild reads as not occupied; callers check Present first.
func (s VoiceSnapshot) occupied(channelID string) bool {
	for _, ch := range s.ChannelByUser {
		if ch == channelID {
			return true
		}
	}
	return false
}

// sessionTempVCManager adapts *discordgo.Session to TempVCManager. Each
// method is a one-line pass-through, which keeps the hard-to-unit-test
// wrapper's uncovered code small. ChannelOverwritesReplace, VoiceStates and
// CanSeeChannel do more, and their tests drive a real session. Every REST
// call but ChannelPermissionSet passes WithRetryOnRatelimit(false): a 429
// is a failure the caller handles, never a sleeping gateway handler. A
// guest add's overwrite set waits out a 429 and retries instead, and
// TempVCManager says why.
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

func (m *sessionTempVCManager) ChannelEdit(channelID string, data *discordgo.ChannelEdit, auditReason string) (*discordgo.Channel, error) {
	return m.s.ChannelEdit(channelID, data,
		discordgo.WithAuditLogReason(auditReason), discordgo.WithRetryOnRatelimit(false))
}

// ChannelOverwritesReplace sends its own PATCH, since ChannelEdit's
// PermissionOverwrites is omitempty and would drop an empty list, the one
// an unlock to a source with no overwrites sends. The body always carries
// the list. The channel Discord returns then goes into the state cache,
// which would otherwise hold the old list until the CHANNEL_UPDATE lands: a
// lock sent in that moment after an unlock would start from the locked
// list. A later CHANNEL_UPDATE overwrites the cache as usual.
func (m *sessionTempVCManager) ChannelOverwritesReplace(channelID string, overwrites []*discordgo.PermissionOverwrite, auditReason string) error {
	if overwrites == nil {
		overwrites = []*discordgo.PermissionOverwrite{}
	}
	body := struct {
		PermissionOverwrites []*discordgo.PermissionOverwrite `json:"permission_overwrites"`
	}{overwrites}
	endpoint := discordgo.EndpointChannel(channelID)
	resp, err := m.s.RequestWithBucketID(http.MethodPatch, endpoint, body, endpoint,
		discordgo.WithAuditLogReason(auditReason), discordgo.WithRetryOnRatelimit(false))
	if err != nil {
		return err
	}
	// The edit has happened. A reply the cache cannot take leaves the cache
	// to the CHANNEL_UPDATE, as before, and is no failure of the edit.
	var edited discordgo.Channel
	if err := json.Unmarshal(resp, &edited); err != nil {
		utils.Warn("Temp VC edited channel not cached, reply unreadable", "channel_id", channelID, "error", err)
		return nil
	}
	// ChannelAdd keeps the cached list when the new one is nil, so an
	// answer of no overwrites is cached as an empty list.
	if edited.PermissionOverwrites == nil {
		edited.PermissionOverwrites = []*discordgo.PermissionOverwrite{}
	}
	if err := m.s.State.ChannelAdd(&edited); err != nil {
		utils.Warn("Temp VC edited channel not cached", "channel_id", channelID, "error", err)
	}
	return nil
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

func (m *sessionTempVCManager) GuildChannels(guildID string) ([]*discordgo.Channel, error) {
	return m.s.GuildChannels(guildID, discordgo.WithRetryOnRatelimit(false))
}

// VoiceStates takes State.RLock once and scans State.Guilds itself. It calls
// no other State method while it holds the lock: State.Guild, State.Channel
// and State.VoiceState each take the same read lock, which Go forbids
// recursively, and State.VoiceState also iterates the slice after State.Guild
// has released it. A guild marked unavailable is the stub Discord sends on
// Ready before the GUILD_CREATE, and reads as missing. Lock order is the
// runtime mutex, then State.RLock; discordgo never takes the runtime mutex.
func (m *sessionTempVCManager) VoiceStates(guildID string) VoiceSnapshot {
	st := m.s.State
	// With voice tracking off the cache holds the GUILD_CREATE-time states
	// and never moves, so every guild reads as missing and every guarded
	// action stops, rather than every join reading as superseded.
	if !m.s.StateEnabled || !st.TrackVoice {
		return VoiceSnapshot{}
	}
	st.RLock()
	defer st.RUnlock()
	for _, g := range st.Guilds {
		if g.ID != guildID {
			continue
		}
		if g.Unavailable {
			return VoiceSnapshot{}
		}
		snap := VoiceSnapshot{
			Present:       true,
			ChannelByUser: make(map[string]string, len(g.VoiceStates)),
			Channels:      make(map[string]struct{}, len(g.Channels)),
		}
		for _, vs := range g.VoiceStates {
			if vs.ChannelID != "" {
				snap.ChannelByUser[vs.UserID] = vs.ChannelID
			}
		}
		for _, ch := range g.Channels {
			snap.Channels[ch.ID] = struct{}{}
		}
		return snap
	}
	return VoiceSnapshot{}
}

// MemberRanks reads the payload under State.RLock. Before any handler runs,
// discordgo stores the GUILD_CREATE guild's pointer, or copies the struct
// over the cached one. From then on the payload's member and voice state
// slices are the cache's, written under State.Lock: a member update writes
// over the shared member, and a voice event replaces or removes an element.
// The lock covers the whole read. The map holds indexes only, so nothing
// shared is kept.
func (m *sessionTempVCManager) MemberRanks(g *discordgo.Guild) map[string]int {
	m.s.State.RLock()
	defer m.s.State.RUnlock()
	return GuildMemberRanks(g)
}

func (m *sessionTempVCManager) ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64, auditReason string) error {
	return m.s.ChannelPermissionSet(channelID, targetID, targetType, allow, deny,
		discordgo.WithAuditLogReason(auditReason), discordgo.WithRetryOnRatelimit(true))
}

func (m *sessionTempVCManager) ChannelMessageEditComplex(edit *discordgo.MessageEdit) (*discordgo.Message, error) {
	return m.s.ChannelMessageEditComplex(edit, discordgo.WithRetryOnRatelimit(false))
}

// CanSeeChannel goes through State.MessagePermissions, discordgo's one
// state calculation that takes the member's roles from its argument rather
// than from the cache: State.UserChannelPermissions fails for a member the
// cache never saw. It reads the channel and the guild under the state's
// lock, never the API.
func (m *sessionTempVCManager) CanSeeChannel(channelID, userID string, roles []string) (bool, error) {
	perms, err := m.s.State.MessagePermissions(&discordgo.Message{
		ChannelID: channelID,
		Author:    &discordgo.User{ID: userID},
		Member:    &discordgo.Member{Roles: roles},
	})
	if err != nil {
		return false, err
	}
	return perms&discordgo.PermissionViewChannel != 0, nil
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
	// guildModeratorRoles are the roles that moderate every hub. Loaded from
	// the store at startup; a hub's effective moderator set is the union of
	// these and its own.
	guildModeratorRoles []string
	// userChannel tracks every member's current voice channel (any channel,
	// not just spawned ones) so a VOICE_STATE_UPDATE can be diffed into a
	// leave + join. It is the diff base, not the truth: discordgo's state
	// cache is checked before each action. Seeded by the restart sweep from
	// the cache, hub occupants left out, then maintained from events.
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
	// renames holds, per spawned channel, the times of its renames inside the
	// last ten minutes, oldest first. Discord allows two per channel per ten
	// minutes; the runtime refuses the third itself so a 429 never sleeps a
	// handler. Pruned at each rename and cleared with the channel.
	renames map[string][]time.Time
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
	// renameCaptured is the same rule for rename failures, opened by a
	// successful rename of one of the hub's channels.
	renameCaptured map[int64]struct{}
	// locks holds each locked spawned channel's lock, the copy of its row's
	// that the runtime decides on. Absent when the channel is unlocked. Set
	// by a lock, removed by an unlock, cleared with the channel, and
	// restored from the rows by the restart sweep.
	locks map[string]lockRecord
	// accessChanges holds the spawned channels with an access change in
	// flight (temp_vc_lock.go): a lock or unlock from its checks until its
	// row write, a let-in from its checks until its guest adds are
	// answered. Another access change of the same channel waits for it.
	accessChanges map[string]*accessChange
	// lockCaptured is the capture rule of deleteCaptured for lock and
	// unlock edit failures, opened by a successful lock or unlock of one of
	// the hub's channels.
	lockCaptured map[int64]struct{}
	// guestCaptured is the same rule for guest add failures, opened by a
	// successful guest add in one of the hub's channels.
	guestCaptured map[int64]struct{}
	// sweepMu serializes restart sweeps. One sweep holds it for its whole
	// run, list included, and a queued one takes its own snapshot once the
	// first has finished, so the later payload's ranks land last. Voice
	// handlers never take it. Taken before mu and released after it.
	sweepMu sync.Mutex
	// sweepActive is set for the length of a restart sweep, list included.
	// A spawn that settles while it is set keeps its marker until the sweep
	// finishes.
	sweepActive bool
	// inFlight holds each spawn in flight (CONTEXT.md) by channel ID, from
	// the tracking commit until settle. The restart sweep skips every
	// channel in it.
	inFlight map[string]struct{}
	// settled is the subset of inFlight whose spawn settled while a sweep
	// was active. That sweep removes them from both sets when it finishes.
	settled map[string]struct{}
}

// NewTempVC builds the runtime state around a manager and a store and loads
// the guild's hubs and guild-wide moderator roles from the store. A store
// that cannot read them is an error: the bot must not run with no hubs when
// hubs exist. StartTempVC is the production caller; the panel's tests build a
// runtime over their own fakes through it, so a register through the panel
// can be followed by a join.
func NewTempVC(mgr TempVCManager, st store.Store, guildID string) (*TempVC, error) {
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
		renames:        make(map[string][]time.Time),
		memberRank:     make(map[string]int),
		lastFailure:    make(map[int64]SpawnFailure),
		createCaptured: make(map[int64]struct{}),
		deleteCaptured: make(map[int64]struct{}),
		renameCaptured: make(map[int64]struct{}),
		locks:          make(map[string]lockRecord),
		accessChanges:  make(map[string]*accessChange),
		lockCaptured:   make(map[int64]struct{}),
		guestCaptured:  make(map[int64]struct{}),
		inFlight:       make(map[string]struct{}),
		settled:        make(map[string]struct{}),
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
	roles, err := st.GetGuildModeratorRoles(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("load guild moderator roles: %w", err)
	}
	t.guildModeratorRoles = roles
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

// ApplyGuildModeratorRoles replaces the guild-wide moderator roles, the set
// every hub's effective moderators include. The panel's service layer calls
// it after its store write succeeds, the same arrangement as ApplyHub, so a
// holder of a role saved on the panel renames at once and a holder of a
// dropped one is refused at once.
func (t *TempVC) ApplyGuildModeratorRoles(roleIDs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.guildModeratorRoles = slices.Clone(roleIDs)
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

// SpawnedCount reports how many live spawned channels a hub has right now,
// from the runtime's own tracking. The panel's hub list shows it per hub row
// ID. A channel whose delete is in flight still counts: it is live until
// Discord confirms.
func (t *TempVC) SpawnedCount(hubID int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, id := range t.channelHub {
		if id == hubID {
			n++
		}
	}
	return n
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
// GUILD_CREATE handler that runs the restart sweep (fires on initial connect
// and again on any reconnect), and a VOICE_STATE_UPDATE
// handler that drives the create, cleanup and ownership lifecycle. Call
// before dg.Open(). The returned runtime is what the panel's service layer
// applies hub saves to.
func StartTempVC(dg *discordgo.Session, guildID string, st store.Store) (*TempVC, error) {
	t, err := NewTempVC(NewSessionTempVCManager(dg), st, guildID)
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
		t.HandleVoiceStateUpdate(vs)
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

// handleGuildCreate runs the restart sweep: it lists the stored rows, then
// rebuilds the record from them and from one copied snapshot of the cache
// taken under the lock. A row whose channel is absent from the snapshot
// loses its row. A present, empty channel is deleted with its row. A
// present, occupied channel is tracked again with its number from the row
// and its owner restored from the row; an owner absent from the channel
// counts as having left, and the handover rule elects from the occupants,
// whose ranks come from the payload's member list. Its lock comes from the
// row too, unless the record tracked the channel before this sweep, which
// only a reconnect finds: the record's lock is then the newer and is kept
// (restoreLockLocked). Everyone inside a locked channel goes on its guest
// list, since they may have got in while the bot was away. No channel
// without a row is touched, so a channel a human made is never deleted.
// GUILD_CREATE re-fires on gateway reconnects, so this also rebuilds
// tracking after any missed events; a restored owner, lock or guest posts
// no notice, so a reconnect is silent.
//
// A spawn in flight, a hub join whose row write or compensating delete has
// not finished, is left as the record has it and its row is skipped: its
// tracking either committed before the rebuild and is kept, or lands on the
// rebuilt record afterwards. Hub occupants are left out of the diff base so
// their next voice event in the hub reads as a join.
//
// A store that cannot list the rows, or a guild missing from the cache,
// leaves the in-memory state as it was: on first connect that is empty, and
// on a reconnect it is the tracking from before the disconnect. The next
// GUILD_CREATE retries.
func (t *TempVC) handleGuildCreate(g *discordgo.GuildCreate) {
	if g.ID != t.guildID {
		return
	}

	// The payload's rank data is copied before anything waits: the read is
	// the sweep's only look at the payload's slices, under the state's
	// lock, and the copy holds indexes alone.
	ranks := t.mgr.MemberRanks(g.Guild)

	// One sweep at a time. A GUILD_CREATE that lands during a sweep waits
	// for it and then runs in full over its own snapshot: its payload may
	// carry a fresher member list than the one in progress.
	t.sweepMu.Lock()
	defer t.sweepMu.Unlock()
	// The sweep is active from before the list until it finishes, so a
	// spawn that settles in between keeps its marker for the rebuild. The
	// deferred finish runs before the sweep mutex is released.
	t.mu.Lock()
	t.sweepActive = true
	t.mu.Unlock()
	defer t.finishSweep()

	ctx, cancel := t.storeContext()
	rows, err := t.st.ListSpawnedChannels(ctx)
	cancel()
	if err != nil {
		captureError("Temp VC restart sweep could not list spawned channels", err, "guild_id", g.ID)
		return
	}

	// Compute the sweep under the lock, but issue deletes after releasing
	// it, ChannelDelete is a network call.
	t.mu.Lock()
	// Who is where, and which channels exist, come from one snapshot of the
	// cache taken here, under the lock, not from the payload: a voice or
	// channel event handled during the list call has already reached the
	// cache, and the payload predates it.
	snap := t.mgr.VoiceStates(t.guildID)
	// A missing guild confirms nothing, so no row can be judged gone or
	// empty. The record stays as it was, as after a failed list, and the
	// next GUILD_CREATE retries.
	if !snap.Present {
		t.mu.Unlock()
		utils.Warn("Temp VC restart sweep skipped, guild missing from the cache", "guild_id", g.ID, "rows", len(rows))
		return
	}
	t.userChannel = make(map[string]string, len(snap.ChannelByUser))
	occupantsOf := make(map[string]map[string]struct{})
	for userID, channelID := range snap.ChannelByUser {
		// A hub's occupants are left out, enabled, disabled or broken hub
		// alike, so their next voice event in it reads as a join once: an
		// enabled hub spawns for them, the others take their usual path.
		if _, isHub := t.hubs[channelID]; !isHub {
			t.userChannel[userID] = channelID
		}
		if occupantsOf[channelID] == nil {
			occupantsOf[channelID] = make(map[string]struct{})
		}
		occupantsOf[channelID][userID] = struct{}{}
	}
	// The record as it stood before this rebuild, for the lock rule in the
	// row loop.
	wasTracked, heldLocks := t.occupants, t.locks
	// A spawn in flight is left as the record has it: its tracking committed
	// before this rebuild, and its row may not have been listed. Everything
	// else is rebuilt from the rows below.
	protected := t.keepSpawnsInFlightLocked()
	// pending is left alone: a create in flight across a reconnect still
	// holds its number and releases it itself. memberRank is kept and
	// overlaid from the payload's copy: a rank learnt before the reconnect
	// is still a rank, and a handover at the sweep needs no REST fetch.
	for userID, rank := range ranks {
		t.memberRank[userID] = rank
	}

	var gone, empty []string
	var handovers []store.SpawnedChannel
	// guests holds, per locked channel, the occupants the sweep adds to its
	// guest list off-lock after the rebuild: anyone who got in while the
	// bot was down or disconnected is a guest.
	guests := make(map[string][]string)
	for _, row := range rows {
		// A spawn in flight's row is skipped, not judged: its channel may
		// not have reached the cache yet, and its tracking is kept above.
		if _, ok := t.inFlight[row.ChannelID]; ok {
			continue
		}
		if !snap.hasChannel(row.ChannelID) {
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
		t.restoreLockLocked(row, wasTracked, heldLocks)
		if in := t.joinGuestsLocked(row.ChannelID, sortedIDs(occ)...); len(in) > 0 {
			guests[row.ChannelID] = in
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
		t.deleteIfStillEmpty(id, "")
	}
	for _, row := range handovers {
		t.applyHandover(row)
	}
	for channelID, in := range guests {
		t.addGuests(channelID, in, guestReasonSwept)
	}

	// tracked is read from the record once the deletes have run: the rows'
	// occupied channels, the spawns in flight the sweep kept, and any empty
	// channel whose delete was refused.
	t.mu.Lock()
	tracked := len(t.occupants)
	t.mu.Unlock()
	utils.Info("Temp VC restart sweep complete",
		"rows", len(rows), "gone", len(gone), "empty", len(empty),
		"tracked", tracked, "protected", protected)
}

// keepSpawnsInFlightLocked replaces the record's spawned channel maps with
// ones holding only the spawns in flight, and reports how many it kept. A
// spawn in flight whose channel is no longer tracked, because its
// compensating delete went through or a hand delete untracked it, keeps
// nothing. Caller holds mu.
func (t *TempVC) keepSpawnsInFlightLocked() int {
	occupants := make(map[string]map[string]struct{})
	owners := make(map[string]string)
	channelHub := make(map[string]int64)
	channelIndex := make(map[string]int)
	locks := make(map[string]lockRecord)
	kept := 0
	for id := range t.inFlight {
		occ, tracked := t.occupants[id]
		if !tracked {
			continue
		}
		kept++
		occupants[id] = occ
		if owner, ok := t.owners[id]; ok {
			owners[id] = owner
		}
		channelHub[id] = t.channelHub[id]
		channelIndex[id] = t.channelIndex[id]
		if lock, ok := t.locks[id]; ok {
			locks[id] = lock
		}
	}
	t.occupants, t.owners, t.channelHub, t.channelIndex = occupants, owners, channelHub, channelIndex
	t.locks = locks
	return kept
}

// finishSweep ends a restart sweep on every exit, a failed list included:
// the active flag drops and every spawn that settled during the sweep
// leaves inFlight. A spawn still in flight stays, because its row write or
// compensating delete is still running and the next sweep must skip it too.
func (t *TempVC) finishSweep() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepActive = false
	for id := range t.settled {
		delete(t.inFlight, id)
	}
	clear(t.settled)
}

// settleSpawn ends a spawn in flight: its row write or its compensating
// delete has finished, whatever the outcome. The channel leaves inFlight
// at once unless a sweep is active, in which case it stays until that
// sweep finishes: the sweep may have listed the rows before this spawn's
// row landed, and must not judge it gone or wipe its tracking.
func (t *TempVC) settleSpawn(channelID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.inFlight[channelID]; !ok {
		return
	}
	if t.sweepActive {
		t.settled[channelID] = struct{}{}
		return
	}
	delete(t.inFlight, channelID)
}

// HandleVoiceStateUpdate diffs a member's voice move into a leave + join and
// runs the spawned-channel lifecycle on each side. StartTempVC is its one
// production caller and wraps it in RecoverPanic; nothing else in production
// should call it. It is exported so the panel's tests can feed a join to a
// runtime they built with NewTempVC.
func (t *TempVC) HandleVoiceStateUpdate(vs *discordgo.VoiceStateUpdate) {
	if vs.GuildID != t.guildID {
		return
	}

	t.mu.Lock()
	// An event the cache already shows superseded is dropped whole:
	// discordgo applied a later event for this member before this handler
	// started. The record is untouched; the member's latest event brings it
	// current. A missing guild answers nothing, so the event counts as
	// current.
	if snap := t.mgr.VoiceStates(t.guildID); snap.Present && snap.channelOf(vs.UserID) != vs.ChannelID {
		t.mu.Unlock()
		utils.Debug("Temp VC voice state event dropped as stale",
			"user_id", vs.UserID, "channel_id", vs.ChannelID, "cache_channel_id", snap.channelOf(vs.UserID))
		return
	}
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
	// A join into a locked channel puts the member on its guest list.
	guests := t.joinGuestsLocked(newChannel, vs.UserID)
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
		t.deleteIfStillEmpty(oldChannel, vs.UserID)
	}

	// The guest add comes after the handovers and the delete, since it may
	// wait out a rate limit (ChannelPermissionSet retries a 429). A member
	// who joined a spawned channel never reaches the hub join below.
	t.addGuests(newChannel, guests, guestReasonJoined)

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
// user ID, and with no such occupant the channel has no owner. The election
// runs over the recorded occupants a fresh snapshot of the cache confirms
// are inside: a successor whose own leave handler has not run yet is not
// handed a channel they already left. An untracked or empty channel changes
// nothing: an emptied one is about to be deleted. Caller holds mu.
func (t *TempVC) reconcileOwnerLocked(channelID string) (row store.SpawnedChannel, changed bool) {
	occ, tracked := t.occupants[channelID]
	if !tracked || len(occ) == 0 {
		return row, false
	}
	current, has := t.owners[channelID]
	if _, present := occ[current]; has && present {
		return row, false
	}
	// A missing guild confirms nobody, so no handover is committed: the
	// owner stands until the next occupancy change or the restart sweep.
	snap := t.mgr.VoiceStates(t.guildID)
	if !snap.Present {
		return row, false
	}
	elected := t.electPresentOwnerLocked(channelID, snap)
	t.setOwnerLocked(channelID, elected)
	if elected == current {
		return row, false
	}
	return t.rowLocked(channelID, elected), true
}

// setOwnerLocked records a spawned channel's owner, or none for empty.
// Caller holds mu.
func (t *TempVC) setOwnerLocked(channelID, owner string) {
	if owner == "" {
		delete(t.owners, channelID)
		return
	}
	t.owners[channelID] = owner
}

// rowLocked builds a tracked spawned channel's row with the given owner,
// from the hub and number the runtime holds for it. Caller holds mu.
func (t *TempVC) rowLocked(channelID, owner string) store.SpawnedChannel {
	return store.SpawnedChannel{
		ChannelID:   channelID,
		HubID:       t.channelHub[channelID],
		Number:      t.channelIndex[channelID],
		OwnerUserID: owner,
	}
}

// electPresentOwnerLocked runs the election over the record's occupants of
// a channel that the snapshot confirms are inside it. The record is the only
// candidate source, because the cache carries no rank; a member whose join
// handler has not run yet is elected by that handler when it runs, since an
// ownerless channel takes the first rank holder to join. A missing guild
// confirms nobody. Caller holds mu.
func (t *TempVC) electPresentOwnerLocked(channelID string, snap VoiceSnapshot) string {
	if !snap.Present {
		return ""
	}
	present := make(map[string]struct{})
	for uid := range t.occupants[channelID] {
		if snap.channelOf(uid) == channelID {
			present[uid] = struct{}{}
		}
	}
	return t.electOwnerLocked(present)
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

// noticeEvent is the event an ownership notice reports: the channel's
// create, or a handover. Each has its own line for an owner and for none,
// and its own capture message for a failed row write.
type noticeEvent int

const (
	noticeCreate noticeEvent = iota
	noticeHandover
)

// captureMsg is the Sentry message for a row write that failed at this event.
func (e noticeEvent) captureMsg() string {
	if e == noticeCreate {
		return "Temp VC row write failed"
	}
	return "Temp VC handover row write failed"
}

// recordOwnership writes a spawned channel's row and posts the ownership
// notice in its chat, off-lock, at every handover. The create path does the
// same two steps itself, with the settle of its spawn in between. The notice
// goes out whether or not the write succeeded: the ownership change itself
// has happened.
func (t *TempVC) recordOwnership(row store.SpawnedChannel, event noticeEvent) {
	t.writeRow(row, event)
	t.postOwnershipNotice(row.ChannelID, event, row.OwnerUserID)
}

// writeRow upserts a spawned channel's row and reports whether it went
// through. The upsert at a handover also heals a channel whose create write
// failed. A failed write captures under the event's message and the channel
// stays tracked with its live owner; docs/temp-vc-decisions.md accepts that
// a restart may then not find it.
func (t *TempVC) writeRow(row store.SpawnedChannel, event noticeEvent) bool {
	ctx, cancel := t.storeContext()
	defer cancel()
	if err := t.st.UpsertSpawnedChannel(ctx, row); err != nil {
		captureError(event.captureMsg(), err, "channel_id", row.ChannelID, "hub_id", row.HubID)
		return false
	}
	return true
}

// applyHandover records an ownership change and logs it.
func (t *TempVC) applyHandover(row store.SpawnedChannel) {
	t.recordOwnership(row, noticeHandover)
	utils.Info("Temp VC handover", "channel_id", row.ChannelID, "hub_id", row.HubID, "owner_id", row.OwnerUserID)
}

// untrackLocked forgets a spawned channel. Caller holds mu.
func (t *TempVC) untrackLocked(channelID string) {
	delete(t.occupants, channelID)
	delete(t.owners, channelID)
	delete(t.channelHub, channelID)
	delete(t.channelIndex, channelID)
	delete(t.renames, channelID)
	delete(t.locks, channelID)
}

// deleteOutcome is what deleteIfStillEmpty did with a channel: the delete
// went through, or was refused and the channel stays tracked with its row.
type deleteOutcome int

const (
	// deleteDone covers a channel that is deleted, already gone, or no
	// longer tracked: nothing is left to keep.
	deleteDone deleteOutcome = iota
	// deleteRefusedOccupied: the record or the cache shows someone inside.
	deleteRefusedOccupied
	// deleteRefusedGuildMissing: the cache holds no guild, so nothing can
	// confirm the channel is empty. It never reads as empty.
	deleteRefusedGuildMissing
	// deleteRefusedFailed: the delete call failed and the channel is live.
	deleteRefusedFailed
)

// reason is the value the compensating delete logs under "reason".
func (o deleteOutcome) reason() string {
	switch o {
	case deleteRefusedOccupied:
		return "channel occupied"
	case deleteRefusedGuildMissing:
		return "guild unavailable"
	case deleteRefusedFailed:
		return "delete failed"
	default:
		return ""
	}
}

// deleteIfStillEmpty deletes a spawned channel that just emptied. It re-checks
// occupancy under the lock first: the leave that emptied the channel was
// applied under the lock, but a join can land in the gap before this runs.
// userID is the member whose leave emptied it, for the log lines; the
// restart sweep passes none.
func (t *TempVC) deleteIfStillEmpty(channelID, userID string) deleteOutcome {
	t.mu.Lock()
	members, tracked := t.occupants[channelID]
	if !tracked {
		t.mu.Unlock()
		return deleteDone
	}
	if len(members) > 0 {
		t.mu.Unlock()
		return deleteRefusedOccupied
	}
	// The record says empty; the cache has the last word. A member whose
	// join handler has not run yet is still inside, and their handler will
	// apply the join when it runs. A missing guild confirms nothing. Either
	// way the channel stays tracked with its row for the next empty event
	// or the restart sweep.
	snap := t.mgr.VoiceStates(t.guildID)
	if !snap.Present {
		t.mu.Unlock()
		utils.Info("Temp VC delete skipped, guild missing from the cache", "channel_id", channelID, "user_id", userID)
		return deleteRefusedGuildMissing
	}
	if snap.occupied(channelID) {
		t.mu.Unlock()
		utils.Info("Temp VC delete skipped, cache shows the channel occupied", "channel_id", channelID, "user_id", userID)
		return deleteRefusedOccupied
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
		if fault.capturesOnChannelChange() {
			t.captureOncePerStreak(t.deleteCaptured, hubID, "Temp VC delete failed", err,
				"channel_id", channelID, "hub_id", hubID, "guild_id", t.guildID)
		}
		return deleteRefusedFailed
	}

	t.mu.Lock()
	t.untrackLocked(channelID)
	delete(t.deleting, channelID)
	t.mu.Unlock()
	t.deleteRow(channelID)
	return deleteDone
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

// permissionSourceOverwrites reads a hub's permission source from
// discordgo's state cache, as it is now: the overwrite list of the hub
// channel's category for source category, the hub channel's own list for
// source hub_channel. The spawn path builds a hub_channel create payload
// from it; a category create sends nothing and lets Discord copy the same
// list. The slice and its entries are the cache's, so a caller sends them
// and never changes them.
//
// A source that cannot be read is an error, never a fallback to another
// list: the hub channel missing from the cache, a hub channel with no
// category, or a category missing from the cache. A hub channel with no
// category is a broken hub, so it is an error for either source kind.
func (t *TempVC) permissionSourceOverwrites(hub store.Hub) ([]*discordgo.PermissionOverwrite, error) {
	hubChannel, err := t.mgr.Channel(hub.HubChannelID)
	if err != nil {
		return nil, fmt.Errorf("hub channel %s not in the state cache: %w", hub.HubChannelID, err)
	}
	if hubChannel.ParentID == "" {
		return nil, fmt.Errorf("hub channel %s has no category", hub.HubChannelID)
	}
	if hub.PermissionSource == store.PermissionHubChannel {
		return hubChannel.PermissionOverwrites, nil
	}
	category, err := t.mgr.Channel(hubChannel.ParentID)
	if err != nil {
		return nil, fmt.Errorf("category %s of hub channel %s not in the state cache: %w",
			hubChannel.ParentID, hub.HubChannelID, err)
	}
	return category.PermissionOverwrites, nil
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
		// The bot's own refusal: a WARN line, a note for the panel and the
		// refusal text in the hub chat. No create call and never a Sentry
		// event.
		t.recordSpawnFailure(hub.ID, SpawnFailureNoCategory)
		utils.Warn("Temp VC spawn refused, hub channel has no category",
			"hub_channel_id", hub.HubChannelID, "user_id", vs.UserID)
		t.messageHubJoiner(hub, vs.UserID, SpawnFailureNoCategory)
		return
	}

	// Permission source category: no PermissionOverwrites, so Discord copies
	// the category's and the channel is synced from birth. Permission source
	// hub_channel: the hub channel's own list, verbatim, for a stricter hub
	// inside a looser category. The bot adds no entry of its own either way.
	var overwrites []*discordgo.PermissionOverwrite
	if hub.PermissionSource == store.PermissionHubChannel {
		overwrites, err = t.permissionSourceOverwrites(hub)
		if err != nil {
			// The hub channel passed both checks above a moment ago, so only
			// a channel event landing in between gets here. It ends as the
			// cache miss above does: nothing spawns and nothing is recorded.
			utils.Warn("Temp VC spawn refused, permission source unreadable",
				"hub_channel_id", hub.HubChannelID, "user_id", vs.UserID, "error", err)
			return
		}
	}

	// The create goes out only for a member the cache still shows in the
	// hub. One who left in the meantime gets no channel and no message:
	// nothing failed.
	if !t.stillInHub(vs.UserID, hub.HubChannelID) {
		utils.Info("Temp VC create skipped, member no longer in the hub",
			"user_id", vs.UserID, "hub_channel_id", hub.HubChannelID)
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

	data := discordgo.GuildChannelCreateData{
		Name:                 name,
		Type:                 discordgo.ChannelTypeGuildVoice,
		ParentID:             hubChannel.ParentID,
		UserLimit:            hub.UserLimit,
		Bitrate:              hub.Bitrate,
		PermissionOverwrites: overwrites,
	}
	reason := fmt.Sprintf("hub %s, creator %s", hub.HubChannelID, vs.UserID)
	channel, err := t.mgr.GuildChannelCreateComplex(t.guildID, data, reason)
	if err != nil {
		t.mu.Lock()
		t.releaseChannelIndexLocked(hub.ID, index)
		t.mu.Unlock()
		fault := classifySpawnedChannelError(err)
		cause := fault.cause()
		t.recordSpawnFailure(hub.ID, cause)
		utils.Warn("Temp VC create failed",
			"user_id", vs.UserID, "hub_channel_id", hub.HubChannelID, "cause", cause, "error", err)
		if fault.capturesOnCreate() {
			t.captureOncePerStreak(t.createCaptured, hub.ID, "Temp VC create failed", err,
				"user_id", vs.UserID, "hub_channel_id", hub.HubChannelID, "guild_id", t.guildID)
		}
		t.messageHubJoiner(hub, vs.UserID, cause)
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
	// From here until settle the channel is a spawn in flight: a restart
	// sweep that overlaps leaves it alone.
	t.inFlight[channel.ID] = struct{}{}
	t.mu.Unlock()

	// The move goes out only for a member the cache still shows in the hub.
	// One who left while the create was in flight gets no move and no
	// message: nothing failed, the channel is just not needed.
	if !t.stillInHub(vs.UserID, hub.HubChannelID) {
		utils.Info("Temp VC move-into abandoned, member no longer in the hub",
			"user_id", vs.UserID, "channel_id", channel.ID, "hub_channel_id", hub.HubChannelID)
		t.abandonSpawn(channel.ID, hub, vs.UserID)
		return
	}
	if err := t.mgr.GuildMemberMove(t.guildID, vs.UserID, &channel.ID); err != nil {
		// The member vanished (disconnected mid-create) or the move was
		// refused. A create that succeeds and a move-into that fails is one
		// spawn failure: the new channel goes through the compensating
		// delete, and the member hears about it only when the cache still
		// shows them in the hub. A member who left needs no ping about a
		// channel that never was. A member still waiting hears about it
		// whatever the delete did: the channel is not theirs either way.
		t.recordSpawnFailure(hub.ID, SpawnFailureMove)
		utils.Warn("Temp VC move-into failed, deleting channel",
			"user_id", vs.UserID, "channel_id", channel.ID, "hub_channel_id", hub.HubChannelID, "error", err)
		if classifySpawnedChannelError(err).capturesOnCreate() {
			t.captureOncePerStreak(t.createCaptured, hub.ID, "Temp VC move-into failed, deleting channel", err,
				"user_id", vs.UserID, "channel_id", channel.ID, "hub_channel_id", hub.HubChannelID)
		}
		t.abandonSpawn(channel.ID, hub, vs.UserID)
		t.messageHubJoiner(hub, vs.UserID, SpawnFailureMove)
		return
	}

	// The spawn succeeded: the hub's last failure is cleared and its next
	// Discord-side failure will capture again.
	t.mu.Lock()
	delete(t.lastFailure, hub.ID)
	delete(t.createCaptured, hub.ID)
	t.mu.Unlock()

	// The row write settles the spawn; the create notice follows, as at a
	// handover.
	t.writeRow(store.SpawnedChannel{ChannelID: channel.ID, HubID: hub.ID, Number: index, OwnerUserID: owner}, noticeCreate)
	t.settleSpawn(channel.ID)
	t.postOwnershipNotice(channel.ID, noticeCreate, owner)

	utils.Info("Temp VC created",
		"channel_id", channel.ID, "name", name, "number", index,
		"hub_channel_id", hub.HubChannelID, "hub_id", hub.ID, "owner_id", owner)
}

// abandonSpawn is the compensating delete for a fresh channel its creator
// never entered, whether the pre-move check skipped the move or Discord
// refused it. The creator leaves the occupancy first: they are never a
// candidate for the channel's ownership. Then the guarded delete runs. When
// it goes through, the channel never had a row. When it is refused, because
// someone is inside, the guild cannot confirm, or the delete call failed,
// the channel is live and stays tracked as an ordinary spawned channel: its
// owner is elected from the occupants a fresh snapshot confirms, its row is
// written, and the create notice follows the row and never names the
// creator.
func (t *TempVC) abandonSpawn(channelID string, hub store.Hub, creator string) {
	t.mu.Lock()
	delete(t.occupants[channelID], creator)
	t.mu.Unlock()
	outcome := t.deleteIfStillEmpty(channelID, creator)
	if outcome == deleteDone {
		t.settleSpawn(channelID)
		return
	}
	utils.Info("Temp VC compensating delete refused, channel kept",
		"channel_id", channelID, "user_id", creator, "hub_channel_id", hub.HubChannelID, "reason", outcome.reason())
	t.mu.Lock()
	elected := t.electPresentOwnerLocked(channelID, t.mgr.VoiceStates(t.guildID))
	t.setOwnerLocked(channelID, elected)
	row := t.rowLocked(channelID, elected)
	t.mu.Unlock()
	// The row write settles the spawn either way; the notice follows a
	// write that went through.
	written := t.writeRow(row, noticeCreate)
	t.settleSpawn(channelID)
	if written {
		t.postOwnershipNotice(channelID, noticeCreate, elected)
	}
}

// stillInHub reports whether a fresh snapshot of the cache shows the member
// in the hub channel. A missing guild cannot confirm it, so the answer is
// no: the guarded action is skipped rather than taken on a guess.
func (t *TempVC) stillInHub(userID, hubChannelID string) bool {
	snap := t.mgr.VoiceStates(t.guildID)
	return snap.Present && snap.channelOf(userID) == hubChannelID
}

// postOwnershipNotice posts the ownership notice in a spawned channel's text
// chat, one of the four lines ownershipNoticeLine picks. The owner is named
// by mention, and the allowed mentions parse nothing, so the mention renders
// and pings nobody. The voice channel status line is not used. A failed send
// is a WARN line: the ownership change itself has already happened.
func (t *TempVC) postOwnershipNotice(channelID string, event noticeEvent, owner string) {
	_, err := t.mgr.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         ownershipNoticeLine(event, owner),
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	})
	if err != nil {
		utils.Warn("Temp VC ownership notice not sent",
			"channel_id", channelID, "owner_id", owner, "error", err)
	}
}

// ownershipNoticeLine picks the ownership notice's line by the event and by
// whether there is an owner. The copy is #264's, with the handover line
// amended by #316 to name the rename command, so a member who takes an
// ownerless channel and never created one still learns it. The command is
// plain text, not a command mention.
func ownershipNoticeLine(event noticeEvent, owner string) string {
	switch {
	case event == noticeCreate && owner != "":
		return fmt.Sprintf("<@%s> owns this channel and can rename it with /%s.", owner, voiceRenameCommandName)
	case event == noticeCreate:
		return "This channel has no owner. The first Cav member to join it becomes the owner."
	case owner != "":
		return fmt.Sprintf("<@%s> now owns this channel and can rename it with /%s.", owner, voiceRenameCommandName)
	default:
		return "This channel has no owner now."
	}
}

// messageHubJoiner tells a member whose channel could not be created, in the
// hub channel's text chat. The content mentions them and the allowed mentions
// name them alone, so nobody else is pinged. No DM, no disconnect, and the
// bot never deletes the message. Three texts, picked by the cause: the
// category is full; the error has been reported to S6, for a failure Discord
// returned; or only that the channel could not be created, for the bot's own
// refusal, which never reaches Sentry and so has nothing to report. A failed
// send is a WARN line: the spawn failure itself has already been handled.
func (t *TempVC) messageHubJoiner(hub store.Hub, userID string, cause SpawnFailureCause) {
	// A member the cache no longer shows in the hub has left, and hears
	// nothing about a channel they no longer want.
	if !t.stillInHub(userID, hub.HubChannelID) {
		utils.Debug("Temp VC spawn failure message skipped, member no longer in the hub",
			"hub_channel_id", hub.HubChannelID, "user_id", userID, "cause", cause)
		return
	}
	var content string
	switch cause {
	case SpawnFailureFull:
		content = fmt.Sprintf("<@%s> this category is full. Wait for a channel to empty, then join again.", userID)
	case SpawnFailureNoCategory:
		content = fmt.Sprintf("<@%s> Cavbot could not create your channel.", userID)
	default:
		content = fmt.Sprintf("<@%s> Cavbot could not create your channel. The error has been reported to S6.", userID)
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
