package commands

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Temporary voice channels (issue #100, first slice of the MEE6 migration).
//
// A member joining any configured hub voice channel gets a personal voice
// channel spawned under that hub's category and is moved into it. Each hub
// carries its own defaults (user limit, bitrate, empty-channel grace) that are
// stamped on the channels it spawns, so a "Squad" hub and a "Briefing" hub can
// behave differently. The creator receives a per-channel permission overwrite
// (ManageChannels + MoveMembers) so they can rename the channel, set a user
// limit, and move or disconnect occupants, matching MEE6's owner model. The
// overwrite itself is the only durable ownership marker; nothing is persisted.
//
// Cleanup is event-driven: when a temp channel empties, a grace timer starts
// (its hub's grace, default 15s, covering quick disconnect/reconnects); if
// nobody returns before it fires, the channel is deleted. Orphans left by a
// bot restart are reaped by the GUILD_CREATE sweep. A hub's category is the
// marker for "temp", so any empty channel under any hub category (except the
// hubs themselves) is deleted on every connect/resume, and non-empty survivors
// are adopted with no recorded owner (their overwrite keeps working; the bot
// just no longer knows who made them).
//
// This is the codebase's first gateway-event feature. GuildVoiceStates is an
// unprivileged intent already covered by IntentsAllWithoutPrivileged in
// main.go, so no identify or Developer Portal change is needed.

const (
	// maxTempChannelsPerUser caps concurrently owned temp channels. Four
	// supports the "operation host spinning up team channels" case; a fifth
	// hub join disconnects the user instead of creating another.
	maxTempChannelsPerUser = 4

	// tempVCNameSuffix follows the MEE6 default naming convention.
	tempVCNameSuffix = "'s Channel"

	// discordChannelNameLimit is Discord's hard cap on channel name length.
	discordChannelNameLimit = 100
)

// Interim-ownership election is decided purely from a member's Discord roles,
// with no nickname parsing. When a channel's creator steps away, the present
// occupant is ranked by, in order:
//
//  1. status tier: their position or membership status (tempVCStatusRoles),
//  2. rank role: their rank within that status (tempVCRankRoles), and
//  3. lowest user ID: a deterministic final tiebreak.
//
// Both ladders are ordered most senior first (smaller index = higher priority),
// and a member holding none of a ladder's roles sorts below every entry in it.
// So a higher status always wins regardless of rank, and within a status the
// higher rank wins.

// tempVCStatusRoles are the 7Cav status / position roles, most senior first.
// Hardcoded for the same reason as the hub IDs: fixed 7Cav infrastructure.
var tempVCStatusRoles = []string{
	"109873149507555328",  // general staff
	"1105529062832742522", // battalion staff
	"1104551737219620894", // company staff
	"1105528611630493818", // platoon staff
	"340945371503001603",  // mp department
	"1127002194940526624", // SL / ASL
	"437748324960043009",  // active member
	"937082349848526848",  // ELOA member
	"690899750425329666",  // reservist member
	"437748982400417792",  // retired member
	"437749895785480193",  // discharged member
}

// rankRole pairs a rank abbreviation with its Discord role ID. The abbreviation
// is documentation only; the roleID is what the election matches against a
// member's roles.
type rankRole struct {
	abbrev string
	roleID string
}

// tempVCRankRoles are the rank roles most senior first (GOA highest, RCT lowest),
// the secondary election key within a status tier. AR (active reservist) is the
// reservist rank granted in lieu of retirement and sits just below PVT. A blank
// role ID would never be matched (that rank would not distinguish members);
// changing an ID later needs no other code change.
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

// noStatusTier / noRankIndex sort after every real entry, so a member holding
// none of a ladder's roles is the lowest priority on that key. (A slice length
// is not a constant expression, so these are vars.)
var (
	noStatusTier = len(tempVCStatusRoles)
	noRankIndex  = len(tempVCRankRoles)
)

// statusRoleIndex / rankRoleIndex map a role ID to its seniority index, derived
// from the ordered ladders so those slices are the single source of truth. Blank
// rank IDs (not yet configured) are skipped.
var (
	statusRoleIndex = func() map[string]int {
		idx := make(map[string]int, len(tempVCStatusRoles))
		for i, id := range tempVCStatusRoles {
			idx[id] = i
		}
		return idx
	}()
	rankRoleIndex = func() map[string]int {
		idx := make(map[string]int, len(tempVCRankRoles))
		for i, rr := range tempVCRankRoles {
			if rr.roleID != "" {
				idx[rr.roleID] = i
			}
		}
		return idx
	}()
)

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

// tempVCOwnerPerms is the permission set a temp channel's owner holds: rename /
// user-limit / bitrate via ManageChannels, kick/move via VoiceMoveMembers, and
// lock/hide/block via ManageRoles (editing the channel's permission overwrites).
// It is granted to the creator at spawn time and to an interim owner while the
// creator is away.
const tempVCOwnerPerms = discordgo.PermissionManageChannels | discordgo.PermissionVoiceMoveMembers | discordgo.PermissionManageRoles

// defaultTempVCGrace is how long an empty temp channel survives before
// deletion, long enough to cover a quick disconnect/reconnect. It is the
// per-hub Grace used by hubs that do not override it, and the fallback for a
// tracked channel whose grace is unknown (an adopted survivor whose hub can no
// longer be resolved).
const defaultTempVCGrace = 15 * time.Second

// tempVCLogChannelID is where the audit trail (create / join / leave / delete /
// rename / cap) is posted, so who did what is on record. It is shared across
// all hubs, one audit log for the whole feature. Hardcoded for the same reason
// as the hub IDs below: it is fixed 7Cav infrastructure.
const tempVCLogChannelID = "1530898079316705430"

// tempVCHub is one "join to create" hub and the defaults it stamps on the
// channels it spawns. A member joining HubChannelID gets a channel created
// under CategoryID carrying UserLimit / Bitrate, reaped after Grace of
// emptiness.
type tempVCHub struct {
	// HubChannelID is the "join to create" hub voice channel.
	HubChannelID string
	// CategoryID is the category this hub's temp channels are spawned under. It
	// is also the durable marker the restart sweep keys on: every empty voice
	// channel under it (except a hub) is reaped on connect, which is how orphans
	// from a bot restart are cleaned up without persisted state, so it must
	// contain only this hub's temp channels.
	CategoryID string
	// UserLimit is the default max occupants stamped on spawned channels
	// (0 = unlimited, Discord's default). The owner can change it afterward.
	UserLimit int
	// Bitrate is the default bitrate in bits/sec for spawned channels
	// (0 = Discord's default, currently 64000).
	Bitrate int
	// Grace is how long one of this hub's channels survives empty before it is
	// deleted.
	Grace time.Duration
}

// tempVCHubs is the hardcoded hub table for the 7Cav guild. Hardcoded rather
// than env-configured because the channel IDs identify fixed 7Cav
// infrastructure, matching the star_citizen_joiners.go / /warden role-ID
// precedent that tenant identifiers live in code, not the environment. Add a
// hub by adding an entry (its own distinct hub channel and category); the
// runtime supports any number. mustTempVCHubs validates the table at init.
var tempVCHubs = mustTempVCHubs([]tempVCHub{
	{
		HubChannelID: "1391707962929709091",
		CategoryID:   "1391707962929709089",
		UserLimit:    0,
		Bitrate:      0,
		Grace:        defaultTempVCGrace,
	},
	{
		HubChannelID: "1530946025114828870",
		CategoryID:   "1530945960870543470",
		UserLimit:    0,
		Bitrate:      0,
		Grace:        defaultTempVCGrace,
	},
})

// mustTempVCHubs validates the hardcoded hub table at package init, panicking
// on a misconfiguration, an empty or reused hub/category ID, a hub that is its
// own category, or a non-positive grace, so a bad edit fails at startup rather
// than silently half-working. Same fail-at-init stance as mustWeeklyFireTime in
// star_citizen_joiners.go.
func mustTempVCHubs(hubs []tempVCHub) []tempVCHub {
	if len(hubs) == 0 {
		panic("tempVCHubs: at least one hub is required")
	}
	seenHub := make(map[string]bool, len(hubs))
	seenCat := make(map[string]bool, len(hubs))
	for i, h := range hubs {
		switch {
		case h.HubChannelID == "" || h.CategoryID == "":
			panic(fmt.Sprintf("tempVCHubs[%d]: hub and category IDs must both be set", i))
		case h.HubChannelID == h.CategoryID:
			panic(fmt.Sprintf("tempVCHubs[%d]: hub and category IDs must differ", i))
		case seenHub[h.HubChannelID]:
			panic(fmt.Sprintf("tempVCHubs[%d]: duplicate hub channel ID %q", i, h.HubChannelID))
		case seenCat[h.CategoryID]:
			panic(fmt.Sprintf("tempVCHubs[%d]: duplicate category ID %q", i, h.CategoryID))
		case h.Grace <= 0:
			panic(fmt.Sprintf("tempVCHubs[%d]: grace must be positive, got %v", i, h.Grace))
		case h.UserLimit < 0 || h.Bitrate < 0:
			panic(fmt.Sprintf("tempVCHubs[%d]: user limit and bitrate must be non-negative", i))
		}
		seenHub[h.HubChannelID] = true
		seenCat[h.CategoryID] = true
	}
	return hubs
}

// TempVCManager is the subset of *discordgo.Session the temp-VC lifecycle
// uses. Command code depends on this interface so tests can substitute a fake
// that records calls and injects per-call errors without touching the live
// Discord gateway, the same seam pattern as GuildManager (/warden) and
// InteractionResponder (utils/discord_responder.go). The variadic
// discordgo.RequestOption arguments are dropped because no call site uses
// them.
type TempVCManager interface {
	GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData) (*discordgo.Channel, error)
	ChannelDelete(channelID string) (*discordgo.Channel, error)
	// GuildMemberMove moves a member between voice channels; a nil channelID
	// disconnects them from voice entirely (the over-cap response).
	GuildMemberMove(guildID, userID string, channelID *string) error
	GuildMember(guildID, userID string) (*discordgo.Member, error)
	// Channel reads a channel's current state. Used to read the live name of a
	// user's existing temp channel before a retro-rename, so an owner who has
	// renamed their channel is not overwritten (see renameForSecondChannel).
	Channel(channelID string) (*discordgo.Channel, error)
	// ChannelEdit applies a partial channel edit; only Name is set at the call
	// sites here (the numeric-suffix disambiguation rename).
	ChannelEdit(channelID string, data *discordgo.ChannelEdit) (*discordgo.Channel, error)
	// ChannelMessageSend posts a plain message to a channel. Used to notify an
	// over-cap hub joiner in the hub's chat (the message return value is dropped
	// as the other write sites here do, matching /warden's GuildManager seam).
	ChannelMessageSend(channelID, content string) error
	// ChannelPermissionSet writes a single permission overwrite on a channel.
	// Used to grant interim ownership to a member (allow the owner perms) and to
	// block a member (deny Connect), so the target list stays scoped to one
	// member at a time.
	ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64) error
	// ChannelPermissionDelete removes a member's permission overwrite, undoing an
	// interim-ownership grant (on the creator's return) or a block (on permit).
	ChannelPermissionDelete(channelID, targetID string) error
}

// sessionTempVCManager adapts *discordgo.Session to TempVCManager. Each
// method is a one-line pass-through; keeping it trivial means the
// (hard-to-unit-test) wrapper adds negligible uncovered surface.
type sessionTempVCManager struct {
	s *discordgo.Session
}

// NewSessionTempVCManager wraps a real Discord session for production use.
func NewSessionTempVCManager(s *discordgo.Session) TempVCManager {
	return &sessionTempVCManager{s: s}
}

func (m *sessionTempVCManager) GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData) (*discordgo.Channel, error) {
	return m.s.GuildChannelCreateComplex(guildID, data)
}

func (m *sessionTempVCManager) ChannelDelete(channelID string) (*discordgo.Channel, error) {
	return m.s.ChannelDelete(channelID)
}

func (m *sessionTempVCManager) GuildMemberMove(guildID, userID string, channelID *string) error {
	return m.s.GuildMemberMove(guildID, userID, channelID)
}

func (m *sessionTempVCManager) GuildMember(guildID, userID string) (*discordgo.Member, error) {
	return m.s.GuildMember(guildID, userID)
}

func (m *sessionTempVCManager) Channel(channelID string) (*discordgo.Channel, error) {
	return m.s.Channel(channelID)
}

func (m *sessionTempVCManager) ChannelEdit(channelID string, data *discordgo.ChannelEdit) (*discordgo.Channel, error) {
	return m.s.ChannelEdit(channelID, data)
}

func (m *sessionTempVCManager) ChannelMessageSend(channelID, content string) error {
	_, err := m.s.ChannelMessageSend(channelID, content)
	return err
}

func (m *sessionTempVCManager) ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64) error {
	return m.s.ChannelPermissionSet(channelID, targetID, targetType, allow, deny)
}

func (m *sessionTempVCManager) ChannelPermissionDelete(channelID, targetID string) error {
	return m.s.ChannelPermissionDelete(channelID, targetID)
}

// TempVCConfig carries the settings for the feature: the guild it runs in, the
// shared audit-log channel, and one or more hubs (each with its own category
// and per-hub spawn defaults).
type TempVCConfig struct {
	GuildID      string
	LogChannelID string
	Hubs         []tempVCHub
}

// LoadTempVCConfig builds the temp voice channel config. The hubs and log
// channel are hardcoded tenant identifiers (tempVCHubs / tempVCLogChannelID),
// so there is nothing to read from the environment and the feature is always
// enabled, the same "tenant IDs in code" stance as star_citizen_joiners.go.
// The (config, bool) shape is retained for the main.go call site; the bool is
// always true.
func LoadTempVCConfig(guildID string) (TempVCConfig, bool) {
	return TempVCConfig{
		GuildID:      guildID,
		LogChannelID: tempVCLogChannelID,
		Hubs:         tempVCHubs,
	}, true
}

// tempVC holds the feature's runtime state. All maps are guarded by mu:
// discordgo dispatches each gateway event on its own goroutine (SyncEvents is
// false by default), and grace timers fire on timer goroutines.
type tempVC struct {
	mgr TempVCManager
	cfg TempVCConfig

	// hubs, categories and hubNum are the config indexed for lookup, built once
	// in newTempVC and never mutated after, so they are read lock-free. hubs maps
	// a hub channel ID -> its hub (routing a hub join to the right category and
	// defaults); categories maps a temp category ID -> the hub that owns it (the
	// restart sweep uses it to tell which categories hold temp channels and to
	// recover a survivor's grace on adoption); hubNum maps a hub channel ID -> its
	// 1-based position in the config, for the "from hub N" audit line.
	hubs       map[string]tempVCHub
	categories map[string]tempVCHub
	hubNum     map[string]int

	mu sync.Mutex
	// channelGrace maps a temp channel ID -> the grace period of the hub that
	// spawned it, so an emptied channel is reaped on its own hub's schedule.
	// Recorded at creation and on adoption; a missing entry falls back to
	// defaultTempVCGrace. Guarded by mu.
	channelGrace map[string]time.Duration
	// userChannel tracks every member's current voice channel (any channel,
	// not just temp ones) so a VOICE_STATE_UPDATE can be diffed into a
	// leave + join without relying on discordgo's state cache. Seeded from
	// GUILD_CREATE voice states, then maintained from events.
	userChannel map[string]string
	// occupants tracks membership per temp channel. Presence of a key is
	// what marks a channel as temp-managed.
	occupants map[string]map[string]struct{}
	// owners maps temp channel ID -> creator user ID. Adopted channels
	// (survivors of a restart) have occupants but no owners entry.
	owners map[string]string
	// baseName maps temp channel ID -> its un-suffixed "<Trooper>'s Channel"
	// name (untruncated). It is the stable root the numeric-suffix
	// disambiguation is rebuilt from, so re-suffixing never stacks "(1) (1)".
	baseName map[string]string
	// assignedName maps temp channel ID -> the exact name the bot last set on
	// it (base, or base + " (n)"). Comparing a channel's live name against this
	// is how a manual owner rename is detected before a retro-rename. Only
	// bot-created channels have entries; adopted channels are owner-controlled
	// and never carry one.
	assignedName map[string]string
	// graceTimers holds the pending empty-channel deletion timer per temp
	// channel, cancelled if anyone rejoins before it fires.
	graceTimers map[string]*time.Timer
	// pendingRename suppresses the audit line for the bot's own disambiguation
	// rename: renameForSecondChannel records the name it is about to set here,
	// and the resulting CHANNEL_UPDATE consumes the marker instead of logging a
	// second (owner-attributed) rename line.
	pendingRename map[string]string
	// controller maps a temp channel ID -> the member currently holding INTERIM
	// ownership because the creator has stepped out of the channel. Absent when
	// the creator is present (they always retain their own overwrite) or when no
	// eligible member is available. The interim member is granted a temporary
	// owner overwrite for the duration; it is removed when the creator returns or
	// the interim member leaves.
	controller map[string]string
	// memberMeta caches each seen member's rank seniority and active-role status,
	// captured from gateway events (which carry the acting member), so an
	// election reads ranks without a REST call per occupant. Keyed by user ID.
	memberMeta map[string]memberRankMeta
}

// memberRankMeta is the cached election input for a member, read from their
// roles: their status tier (smaller = higher; noStatusTier if in none) and their
// rank index within that status (smaller = higher; noRankIndex if none).
type memberRankMeta struct {
	statusIdx int
	rankIdx   int
}

// worstMemberMeta is the election input for a member we have never cached (no
// status role, no rank role), so an unseen occupant never outranks a known one.
var worstMemberMeta = memberRankMeta{statusIdx: noStatusTier, rankIdx: noRankIndex}

// newTempVC builds the runtime state around a manager and config, indexing the
// hubs by hub channel and by category for lock-free lookup.
func newTempVC(mgr TempVCManager, cfg TempVCConfig) *tempVC {
	hubs := make(map[string]tempVCHub, len(cfg.Hubs))
	categories := make(map[string]tempVCHub, len(cfg.Hubs))
	hubNum := make(map[string]int, len(cfg.Hubs))
	for i, h := range cfg.Hubs {
		hubs[h.HubChannelID] = h
		categories[h.CategoryID] = h
		hubNum[h.HubChannelID] = i + 1
	}
	return &tempVC{
		mgr:           mgr,
		cfg:           cfg,
		hubs:          hubs,
		categories:    categories,
		hubNum:        hubNum,
		userChannel:   make(map[string]string),
		occupants:     make(map[string]map[string]struct{}),
		owners:        make(map[string]string),
		baseName:      make(map[string]string),
		assignedName:  make(map[string]string),
		graceTimers:   make(map[string]*time.Timer),
		pendingRename: make(map[string]string),
		channelGrace:  make(map[string]time.Duration),
		controller:    make(map[string]string),
		memberMeta:    make(map[string]memberRankMeta),
	}
}

// StartTempVC wires the temp voice channel feature onto a Discord session:
// a GUILD_CREATE handler that seeds occupancy and sweeps orphans (fires on
// initial connect and again on any reconnect), a VOICE_STATE_UPDATE handler that
// drives the create/cleanup/interim-ownership lifecycle, and a CHANNEL_UPDATE
// handler for the audit trail. Call before dg.Open(). It returns the /voice
// owner-management command bound to this runtime, for the caller to register
// with the command registry.
func StartTempVC(dg *discordgo.Session, cfg TempVCConfig) Command {
	t := newTempVC(NewSessionTempVCManager(dg), cfg)

	utils.Info("Starting temp voice channels",
		"hubs", len(cfg.Hubs),
		"log_channel_id", cfg.LogChannelID,
		"max_per_user", maxTempChannelsPerUser,
	)
	for _, h := range cfg.Hubs {
		utils.Debug("Temp VC hub configured",
			"hub_channel_id", h.HubChannelID,
			"category_id", h.CategoryID,
			"user_limit", h.UserLimit,
			"bitrate", h.Bitrate,
			"grace", h.Grace.String(),
		)
	}

	dg.AddHandler(func(_ *discordgo.Session, g *discordgo.GuildCreate) {
		defer utils.RecoverPanic("tempvc-guild-create")
		t.handleGuildCreate(g)
	})
	dg.AddHandler(func(_ *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		defer utils.RecoverPanic("tempvc-voice-state")
		t.handleVoiceStateUpdate(vs)
	})
	dg.AddHandler(func(_ *discordgo.Session, c *discordgo.ChannelUpdate) {
		defer utils.RecoverPanic("tempvc-channel-update")
		t.handleChannelUpdate(c)
	})

	return t.voiceCommand()
}

// handleGuildCreate seeds voice-state tracking from the GUILD_CREATE payload
// and sweeps the temp category: empty channels (orphans of a restart, or
// stragglers whose delete failed) are removed, non-empty ones are adopted so
// their eventual emptying still triggers cleanup. GUILD_CREATE re-fires on
// gateway reconnects, so this also resynchronizes tracking after any missed
// events.
func (t *tempVC) handleGuildCreate(g *discordgo.GuildCreate) {
	if g.ID != t.cfg.GuildID {
		return
	}

	// Compute the sweep under the lock, but issue deletes after releasing
	// it, ChannelDelete is a network call.
	t.mu.Lock()
	for _, timer := range t.graceTimers {
		timer.Stop()
	}
	t.userChannel = make(map[string]string)
	t.occupants = make(map[string]map[string]struct{})
	t.owners = make(map[string]string)
	t.baseName = make(map[string]string)
	t.assignedName = make(map[string]string)
	t.graceTimers = make(map[string]*time.Timer)
	t.pendingRename = make(map[string]string)
	t.channelGrace = make(map[string]time.Duration)
	t.controller = make(map[string]string)
	t.memberMeta = make(map[string]memberRankMeta)

	for _, vs := range g.VoiceStates {
		if vs.ChannelID != "" {
			t.userChannel[vs.UserID] = vs.ChannelID
		}
		// Seed the rank/active cache from any voice state that carries a member,
		// so an election right after reconnect has ranks without a REST fetch.
		if vs.Member != nil {
			t.rememberMemberLocked(vs.UserID, vs.Member)
		}
	}

	var toDelete []string
	adopted := 0
	for _, ch := range g.Channels {
		// A channel is temp-managed when it sits under some hub's category and is
		// not itself a hub. hubs/categories are immutable after construction, so
		// reading them here (under the lock, harmlessly) is safe.
		hub, inTempCategory := t.categories[ch.ParentID]
		if !inTempCategory {
			continue
		}
		if _, isHub := t.hubs[ch.ID]; isHub {
			continue
		}
		if ch.Type != discordgo.ChannelTypeGuildVoice {
			continue
		}
		members := make(map[string]struct{})
		for _, vs := range g.VoiceStates {
			if vs.ChannelID == ch.ID {
				members[vs.UserID] = struct{}{}
			}
		}
		if len(members) == 0 {
			toDelete = append(toDelete, ch.ID)
			continue
		}
		t.occupants[ch.ID] = members
		// Recover the survivor's grace from its category's hub, so its eventual
		// emptying is reaped on that hub's schedule rather than a global default.
		t.channelGrace[ch.ID] = hub.Grace
		adopted++
	}
	t.mu.Unlock()

	for _, id := range toDelete {
		if _, err := t.mgr.ChannelDelete(id); err != nil {
			utils.CaptureError("Temp VC orphan sweep delete failed", err,
				"channel_id", id, "guild_id", g.ID)
		} else {
			utils.Info("Temp VC orphan deleted", "channel_id", id)
			t.logEvent(fmt.Sprintf("🤖 Auto-deleted orphaned channel `%s`, was empty, left over from a restart", id))
		}
	}

	utils.Info("Temp VC sweep complete",
		"orphans_deleted", len(toDelete), "adopted", adopted)
}

// handleVoiceStateUpdate diffs a member's voice move into a leave + join and
// runs the temp-channel lifecycle on each side.
func (t *tempVC) handleVoiceStateUpdate(vs *discordgo.VoiceStateUpdate) {
	if vs.GuildID != t.cfg.GuildID {
		return
	}

	t.mu.Lock()
	// Refresh the acting member's cached rank/active status whenever the gateway
	// gives us their member object, so elections read current data.
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

	// A temp channel is one we track in occupants; capture whether the vacated
	// channel was one (and its name) before applyLeaveLocked mutates occupancy,
	// so a leave can be audit-logged by name.
	_, leftTemp := t.occupants[oldChannel]
	leftName := t.assignedName[oldChannel]
	t.applyLeaveLocked(vs.UserID, oldChannel)
	joinedTemp := t.applyJoinLocked(vs.UserID, newChannel)
	joinedName := t.assignedName[newChannel]
	ownedCount := t.ownedCountLocked(vs.UserID)
	// Re-elect interim ownership on both sides of the move: leaving may have made
	// a creator absent (hand off) or removed the interim holder (re-elect);
	// joining may have brought the creator back (hand back). Computed under the
	// lock, applied off-lock.
	var ops []controllerOp
	if leftTemp {
		ops = append(ops, t.reconcileControllerLocked(oldChannel)...)
	}
	if joinedTemp {
		ops = append(ops, t.reconcileControllerLocked(newChannel)...)
	}
	t.mu.Unlock()

	// Audit join/leave of temp channels only (hub and other channels are not
	// tracked, so this never logs ordinary server voice traffic). Off-lock.
	if leftTemp {
		t.logEvent(fmt.Sprintf("⬅️ <@%s> left %s", vs.UserID, channelLabel(oldChannel, leftName)))
	}
	if joinedTemp {
		t.logEvent(fmt.Sprintf("➡️ <@%s> joined %s", vs.UserID, channelLabel(newChannel, joinedName)))
	}
	t.applyControllerOps(ops)

	// Creating happens only when the user joined a hub they are not already
	// tracked inside as a temp channel. hubs is immutable after construction, so
	// this read needs no lock.
	if joinedTemp {
		return
	}
	hub, isHub := t.hubs[newChannel]
	if !isHub {
		return
	}
	t.handleHubJoin(vs, hub, ownedCount)
}

// logEvent posts one audit line to the temp-VC log channel, prefixed with the
// UTC (Zulu) wall-clock time so each entry is timestamped in-text regardless of
// the reader's Discord locale. Best-effort and deliberately non-critical: a
// failed post is logged at WARN, not captured to Sentry, so a log-channel
// hiccup or rate-limit never pages and never blocks the lifecycle. Skipped when
// no log channel is configured.
func (t *tempVC) logEvent(content string) {
	if t.cfg.LogChannelID == "" {
		return
	}
	line := "`" + time.Now().UTC().Format("15:04:05") + "Z` " + content
	if err := t.mgr.ChannelMessageSend(t.cfg.LogChannelID, line); err != nil {
		utils.Warn("Temp VC audit log post failed",
			"error", err, "channel_id", t.cfg.LogChannelID)
	}
}

// channelLabel renders a channel for an audit line. It prefers the bot-tracked
// name as bold text, so the entry stays readable after the channel is deleted
// (a <#id> mention renders as "#unknown" once the channel is gone). It falls
// back to a live mention only when no name is known, an adopted restart
// survivor, which is still alive when referenced.
func channelLabel(channelID, name string) string {
	if name != "" {
		return "**" + name + "**"
	}
	return "<#" + channelID + ">"
}

// handleChannelUpdate audit-logs owner-initiated edits to a tracked temp
// channel: rename, user-limit change, lock/unlock (@everyone Connect), and
// hide/reveal (@everyone View Channel). It diffs the event's BeforeUpdate
// (discordgo's pre-update state cache) against the new channel, so an unrelated
// field change is not misreported as, say, a rename. Non-temp channels and the
// bot's own disambiguation rename are ignored. Best-effort: when BeforeUpdate
// is absent (channel not cached) there is no baseline, so it skips.
func (t *tempVC) handleChannelUpdate(c *discordgo.ChannelUpdate) {
	if c == nil || c.Channel == nil || c.GuildID != t.cfg.GuildID {
		return
	}

	t.mu.Lock()
	_, tracked := t.occupants[c.ID]
	owner := t.owners[c.ID]
	// While the creator is away an interim owner holds the controlling overwrite,
	// so attribute edits to them when one is in place; otherwise to the creator.
	if ctrl, ok := t.controller[c.ID]; ok {
		owner = ctrl
	}
	ourRename := t.pendingRename[c.ID] == c.Name
	if ourRename {
		delete(t.pendingRename, c.ID)
	}
	t.mu.Unlock()

	if !tracked || c.BeforeUpdate == nil {
		return
	}
	before := c.BeforeUpdate

	// These edits come through Discord's channel UI, which the CHANNEL_UPDATE
	// event does not attribute to an actor. Only someone holding Manage Channel
	// on the temp channel can make them, the creator, or the interim owner while
	// the creator is away, so attribute to that member as the best available
	// signal; an owner-less adopted channel could only have been edited by an
	// admin.
	by := actorSuffix(owner)

	if before.Name != c.Name && !ourRename {
		t.logEvent(fmt.Sprintf("✏️ **%s** renamed to **%s**%s", before.Name, c.Name, by))
	}

	if before.UserLimit != c.UserLimit {
		if c.UserLimit == 0 {
			t.logEvent(fmt.Sprintf("👥 **%s** user limit removed%s", c.Name, by))
		} else {
			t.logEvent(fmt.Sprintf("👥 **%s** user limit set to %d%s", c.Name, c.UserLimit, by))
		}
	}

	wasLocked := everyoneDenies(before, c.GuildID, discordgo.PermissionVoiceConnect)
	nowLocked := everyoneDenies(c.Channel, c.GuildID, discordgo.PermissionVoiceConnect)
	if wasLocked != nowLocked {
		if nowLocked {
			t.logEvent(fmt.Sprintf("🔒 **%s** locked (@everyone can no longer connect)%s", c.Name, by))
		} else {
			t.logEvent(fmt.Sprintf("🔓 **%s** unlocked%s", c.Name, by))
		}
	}

	wasHidden := everyoneDenies(before, c.GuildID, discordgo.PermissionViewChannel)
	nowHidden := everyoneDenies(c.Channel, c.GuildID, discordgo.PermissionViewChannel)
	if wasHidden != nowHidden {
		if nowHidden {
			t.logEvent(fmt.Sprintf("🙈 **%s** hidden (@everyone can no longer see it)%s", c.Name, by))
		} else {
			t.logEvent(fmt.Sprintf("👁️ **%s** revealed%s", c.Name, by))
		}
	}
}

// actorSuffix renders who performed a channel edit for the audit line. Owner
// edits (the common case) are attributed to the owner; an owner-less adopted
// channel can only have been edited by an admin.
func actorSuffix(owner string) string {
	if owner == "" {
		return " by a server admin"
	}
	return fmt.Sprintf(" by <@%s>", owner)
}

// everyoneDenies reports whether the channel's @everyone permission overwrite
// denies the given permission bit. The @everyone role's ID equals the guild ID.
func everyoneDenies(ch *discordgo.Channel, guildID string, perm int64) bool {
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeRole && o.ID == guildID {
			return o.Deny&perm != 0
		}
	}
	return false
}

// applyLeaveLocked removes the user from a temp channel's occupancy and
// starts the grace timer if that emptied it. Caller holds mu.
func (t *tempVC) applyLeaveLocked(userID, channelID string) {
	members, ok := t.occupants[channelID]
	if !ok {
		return
	}
	delete(members, userID)
	if len(members) > 0 {
		return
	}
	t.scheduleDeleteLocked(channelID)
}

// applyJoinLocked adds the user to a temp channel's occupancy, cancelling any
// pending deletion. Returns whether the joined channel is temp-managed.
// Caller holds mu.
func (t *tempVC) applyJoinLocked(userID, channelID string) bool {
	members, ok := t.occupants[channelID]
	if !ok {
		return false
	}
	members[userID] = struct{}{}
	if timer, ok := t.graceTimers[channelID]; ok {
		timer.Stop()
		delete(t.graceTimers, channelID)
	}
	return true
}

// ownedCountLocked counts temp channels currently owned by a user. Caller
// holds mu. Linear over live temp channels, which is bounded and tiny. Interim
// control is deliberately excluded, it never counts toward the interim holder's
// create cap, since they did not create the channel and lose it on the creator's
// return.
func (t *tempVC) ownedCountLocked(userID string) int {
	n := 0
	for _, owner := range t.owners {
		if owner == userID {
			n++
		}
	}
	return n
}

// rememberMemberLocked caches a member's status tier and rank index, both read
// from their roles, for future elections. Caller holds mu.
func (t *tempVC) rememberMemberLocked(userID string, m *discordgo.Member) {
	t.memberMeta[userID] = memberRankMeta{
		statusIdx: lowestRoleIndex(m, statusRoleIndex, noStatusTier),
		rankIdx:   lowestRoleIndex(m, rankRoleIndex, noRankIndex),
	}
}

// controllerOp is a deferred permission-overwrite change for interim ownership,
// computed under the lock and applied off-lock (each is a Discord REST call).
// grant/revoke are user IDs; either may be empty.
type controllerOp struct {
	channelID string
	grant     string
	revoke    string
}

// electInterimControllerLocked picks the member who should hold interim
// ownership of a bot-created channel whose creator is currently absent. Every
// occupant is eligible; they are ordered by status tier first (the present
// occupant in the highest of tempVCStatusRoles wins), then by rank role within a
// tier, then by lowest user ID for determinism. Returns "" only when the channel
// is empty. Caller holds mu.
func (t *tempVC) electInterimControllerLocked(channelID string) string {
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
func (t *tempVC) metaForLocked(userID string) memberRankMeta {
	if m, ok := t.memberMeta[userID]; ok {
		return m
	}
	return worstMemberMeta
}

// outranksForInterim reports whether candidate (m, uid) should beat the current
// best (bm, buid) for interim ownership: higher status tier, then higher rank,
// then lower user ID.
func outranksForInterim(m memberRankMeta, uid string, bm memberRankMeta, buid string) bool {
	if m.statusIdx != bm.statusIdx {
		return m.statusIdx < bm.statusIdx
	}
	if m.rankIdx != bm.rankIdx {
		return m.rankIdx < bm.rankIdx
	}
	return uid < buid
}

// reconcileControllerLocked recomputes who should hold interim ownership of a
// temp channel and returns the overwrite ops needed to reach that state. The
// creator keeps their own overwrite permanently; interim control applies only
// while the creator is out of the channel. Adopted channels (no recorded
// creator) never get an interim owner. An emptied channel is left alone, it is
// about to be grace-deleted, so churning its overwrites is pointless. Caller
// holds mu.
func (t *tempVC) reconcileControllerLocked(channelID string) []controllerOp {
	occ, tracked := t.occupants[channelID]
	if !tracked || len(occ) == 0 {
		return nil
	}
	current := t.controller[channelID]

	desired := ""
	creator, hasCreator := t.owners[channelID]
	if _, creatorPresent := occ[creator]; hasCreator && !creatorPresent {
		// Creator is away: elect a stand-in from the (non-empty) occupants.
		desired = t.electInterimControllerLocked(channelID)
	}

	if desired == current {
		return nil
	}
	if desired == "" {
		delete(t.controller, channelID)
	} else {
		t.controller[channelID] = desired
	}
	return []controllerOp{{channelID: channelID, grant: desired, revoke: current}}
}

// applyControllerOps executes interim-ownership overwrite changes off-lock: it
// removes the outgoing holder's grant and adds the incoming holder's, logging
// the handoff. Best-effort, a failed Discord call is captured but never blocks
// the lifecycle (the next voice event reconciles again). The creator's own
// overwrite is never touched here; only the interim grant is.
func (t *tempVC) applyControllerOps(ops []controllerOp) {
	for _, op := range ops {
		if op.revoke != "" {
			if err := t.mgr.ChannelPermissionDelete(op.channelID, op.revoke); err != nil {
				utils.CaptureError("Temp VC interim-owner revoke failed", err,
					"channel_id", op.channelID, "user_id", op.revoke)
			}
		}
		if op.grant != "" {
			if err := t.mgr.ChannelPermissionSet(op.channelID, op.grant,
				discordgo.PermissionOverwriteTypeMember, tempVCOwnerPerms, 0); err != nil {
				utils.CaptureError("Temp VC interim-owner grant failed", err,
					"channel_id", op.channelID, "user_id", op.grant)
				continue
			}
		}
		switch {
		case op.grant != "" && op.revoke != "":
			t.logEvent(fmt.Sprintf("👑 Interim ownership of %s passed to <@%s> (previous stand-in <@%s> left)",
				channelLabel(op.channelID, t.assignedNameOf(op.channelID)), op.grant, op.revoke))
		case op.grant != "":
			t.logEvent(fmt.Sprintf("👑 <@%s> is interim owner of %s while the owner is away",
				op.grant, channelLabel(op.channelID, t.assignedNameOf(op.channelID))))
		case op.revoke != "":
			t.logEvent(fmt.Sprintf("👑 Interim ownership of %s ended (owner back or channel empty); <@%s> stood down",
				channelLabel(op.channelID, t.assignedNameOf(op.channelID)), op.revoke))
		}
	}
}

// assignedNameOf reads a channel's tracked name under the lock, for off-lock
// audit lines.
func (t *tempVC) assignedNameOf(channelID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.assignedName[channelID]
}

// scheduleDeleteLocked arms (or re-arms) the empty-channel grace timer using
// the grace of the hub that spawned the channel. Caller holds mu.
func (t *tempVC) scheduleDeleteLocked(channelID string) {
	if timer, ok := t.graceTimers[channelID]; ok {
		timer.Stop()
	}
	grace := t.channelGrace[channelID]
	if grace <= 0 {
		// No recorded grace (a channel tracked without a create/adopt record);
		// fall back to the default rather than fire immediately.
		grace = defaultTempVCGrace
	}
	t.graceTimers[channelID] = time.AfterFunc(grace, func() {
		defer utils.RecoverPanic("tempvc-grace-delete")
		t.deleteIfStillEmpty(channelID)
	})
}

// deleteIfStillEmpty re-checks occupancy when the grace timer fires, a
// rejoin between scheduling and firing normally cancels the timer, but the
// re-check closes the race where the timer fires while a join is waiting on
// the lock.
func (t *tempVC) deleteIfStillEmpty(channelID string) {
	t.mu.Lock()
	members, tracked := t.occupants[channelID]
	if !tracked || len(members) > 0 {
		t.mu.Unlock()
		return
	}
	// Drop only the (now-fired) grace timer here. Ownership/occupancy tracking
	// is kept until Discord confirms the delete, because a channel that fails
	// to delete is still live and MUST keep counting toward its owner's cap.
	// Removing it before the API call is what let a user exceed the cap with
	// zombie channels when deletes 403'd (missing Manage Channels).
	delete(t.graceTimers, channelID)
	t.mu.Unlock()

	if _, err := t.mgr.ChannelDelete(channelID); err != nil {
		// Still live, still tracked, so it still counts toward the cap. The
		// GUILD_CREATE sweep on the next reconnect is the retry (no in-cycle
		// retry, so a persistent permission fault does not flood Sentry); a
		// rejoin+leave of this channel also re-arms the grace delete.
		utils.CaptureError("Temp VC delete failed", err,
			"channel_id", channelID, "guild_id", t.cfg.GuildID)
		return
	}

	t.mu.Lock()
	deletedName := t.assignedName[channelID]
	deletedOwner := t.owners[channelID]
	delete(t.occupants, channelID)
	delete(t.owners, channelID)
	delete(t.baseName, channelID)
	delete(t.assignedName, channelID)
	delete(t.pendingRename, channelID)
	delete(t.channelGrace, channelID)
	delete(t.controller, channelID)
	t.mu.Unlock()
	utils.Info("Temp VC deleted", "channel_id", channelID)
	t.logEvent(deleteLogLine(channelID, deletedName, deletedOwner))

	// If that left the owner with a single channel, drop its "(n)" suffix so a
	// lone channel is never numbered.
	t.demoteSoleChannelToUnnumbered(deletedOwner)
}

// deleteLogLine renders the audit line for a deleted temp channel. A channel
// mention (<#id>) no longer resolves once the channel is gone, so the name is
// carried as text. Both name and owner fall back gracefully for adopted
// channels (survivors of a restart) that carry no recorded name or owner.
func deleteLogLine(channelID, name, owner string) string {
	if name == "" {
		name = channelID
	}
	if owner == "" {
		// No recorded owner means an adopted restart survivor (an orphan).
		return fmt.Sprintf("🤖 Auto-deleted orphaned channel **%s** (`%s`), was empty, left over from a restart", name, channelID)
	}
	return fmt.Sprintf("🤖 Auto-deleted **%s** (`%s`), owner <@%s>, was empty", name, channelID, owner)
}

// handleHubJoin creates a personal channel for a hub joiner (or disconnects
// them if they're at the ownership cap) and moves them into it. Runs without
// the lock held, creation and moves are network calls, and re-locks only
// to commit tracking state.
func (t *tempVC) handleHubJoin(vs *discordgo.VoiceStateUpdate, hub tempVCHub, ownedCount int) {
	if ownedCount >= maxTempChannelsPerUser {
		utils.Info("Temp VC cap reached, disconnecting hub joiner",
			"user_id", vs.UserID, "owned", ownedCount)
		if err := t.mgr.GuildMemberMove(t.cfg.GuildID, vs.UserID, nil); err != nil {
			utils.CaptureError("Temp VC over-cap disconnect failed", err,
				"user_id", vs.UserID, "guild_id", t.cfg.GuildID)
		}
		// Tell the user why they were bounced, in the hub's own chat, tagging
		// them so it surfaces. Best-effort and independent of the disconnect:
		// even if the move above failed, the explanation still goes out.
		t.notifyCapReached(vs.UserID, hub)
		t.logEvent(fmt.Sprintf("⛔ <@%s> (`%s`) hit hub %d's %d-channel limit; join refused",
			vs.UserID, vs.UserID, t.hubNum[hub.HubChannelID], maxTempChannelsPerUser))
		return
	}

	// Slot 1 is unsuffixed; slots 2+ are numbered "... (n)". The slot is the
	// smallest number not already in use by this user's channels (see
	// nextChannelIndexLocked), NOT the channel count, which collides after a
	// lower-numbered channel is deleted. Creating slot 2 retro-numbers the
	// user's first, previously-unsuffixed channel to "(1)".
	base := t.baseChannelName(vs)
	t.mu.Lock()
	index := t.nextChannelIndexLocked(vs.UserID)
	t.mu.Unlock()
	name := truncateChannelName(base)
	if index >= 2 {
		name = nameWithIndex(base, index)
	}

	// Stamp the hub's per-hub defaults on the new channel. UserLimit and Bitrate
	// of 0 are Discord's own defaults, so a hub that leaves them unset creates a
	// plain unlimited channel. The owner can change either afterward.
	channel, err := t.mgr.GuildChannelCreateComplex(t.cfg.GuildID, discordgo.GuildChannelCreateData{
		Name:      name,
		Type:      discordgo.ChannelTypeGuildVoice,
		ParentID:  hub.CategoryID,
		UserLimit: hub.UserLimit,
		Bitrate:   hub.Bitrate,
		PermissionOverwrites: []*discordgo.PermissionOverwrite{
			{
				ID:   vs.UserID,
				Type: discordgo.PermissionOverwriteTypeMember,
				// The owner marker, matching MEE6's owner model (see
				// tempVCOwnerPerms): rename/limit/bitrate, kick/move, and
				// lock/hide/block. Without ManageRoles the owner could only rename
				// their channel, not lock or hide it.
				Allow: tempVCOwnerPerms,
			},
		},
	})
	if err != nil {
		utils.CaptureError("Temp VC create failed", err,
			"user_id", vs.UserID, "guild_id", t.cfg.GuildID)
		return
	}

	t.mu.Lock()
	t.occupants[channel.ID] = make(map[string]struct{})
	t.owners[channel.ID] = vs.UserID
	t.baseName[channel.ID] = base
	t.assignedName[channel.ID] = name
	// Record the spawning hub's grace so this channel is reaped on that hub's
	// schedule when it later empties.
	t.channelGrace[channel.ID] = hub.Grace
	t.mu.Unlock()

	if err := t.mgr.GuildMemberMove(t.cfg.GuildID, vs.UserID, &channel.ID); err != nil {
		// The user vanished (disconnected mid-create) or the move was
		// refused; without them the new channel would sit empty until the
		// grace timer, so reap it immediately.
		utils.CaptureError("Temp VC move-into failed, deleting channel", err,
			"user_id", vs.UserID, "channel_id", channel.ID)
		t.mu.Lock()
		delete(t.occupants, channel.ID)
		delete(t.owners, channel.ID)
		delete(t.baseName, channel.ID)
		delete(t.assignedName, channel.ID)
		delete(t.pendingRename, channel.ID)
		delete(t.channelGrace, channel.ID)
		delete(t.controller, channel.ID)
		t.mu.Unlock()
		if _, delErr := t.mgr.ChannelDelete(channel.ID); delErr != nil {
			utils.CaptureError("Temp VC post-move-failure delete failed", delErr,
				"channel_id", channel.ID)
		}
		return
	}

	utils.Info("Temp VC created",
		"channel_id", channel.ID, "owner_id", vs.UserID, "name", channel.Name, "hub", t.hubNum[hub.HubChannelID])
	t.logEvent(fmt.Sprintf("🆕 <@%s> (`%s`) created %s from hub %d",
		vs.UserID, vs.UserID, channelLabel(channel.ID, name), t.hubNum[hub.HubChannelID]))

	// Creating the user's second channel (slot 2) retro-numbers their first,
	// previously-unsuffixed channel to "(1)".
	if index == 2 {
		t.renameForSecondChannel(vs.UserID, channel.ID)
	}
}

// notifyCapReached posts a message in the joined hub's chat tagging a joiner
// who hit the ownership cap, so the disconnect is not silent. Posted to the hub
// the user actually joined, and quoting that hub's grace. Best-effort: a send
// failure is captured but never blocks. The copy carries no em dash, per the
// user-facing-copy style.
func (t *tempVC) notifyCapReached(userID string, hub tempVCHub) {
	// A channel only frees a slot once it is actually deleted, which happens
	// after it sits empty for the grace period, not the moment someone leaves.
	// Spell that out (and quote the real grace) so the user does not just leave
	// a still-occupied channel and expect the cap to drop.
	graceSeconds := int(hub.Grace.Round(time.Second).Seconds())
	msg := fmt.Sprintf(
		"<@%s> You already own the maximum of %d temporary voice channels. To make another, empty one of yours and wait about %d seconds for it to be deleted, then rejoin the hub.",
		userID, maxTempChannelsPerUser, graceSeconds,
	)
	if err := t.mgr.ChannelMessageSend(hub.HubChannelID, msg); err != nil {
		utils.CaptureError("Temp VC cap notification failed", err,
			"user_id", userID, "channel_id", hub.HubChannelID)
	}
}

// renameForSecondChannel numbers a user's first temp channel "... (1)" the
// moment they have a second, so a bare unnumbered channel never sits alongside
// numbered ones. Fires whether or not the first channel is currently occupied
// (a lone survivor is later demoted back to unnumbered on delete). Skipped when
// the first channel was renamed outside the bot, never overwrite a chosen name.
func (t *tempVC) renameForSecondChannel(userID, newChannelID string) {
	t.mu.Lock()
	firstID, base, assigned, matches := "", "", "", 0
	for id, owner := range t.owners {
		if owner == userID && id != newChannelID {
			firstID, base, assigned = id, t.baseName[id], t.assignedName[id]
			matches++
		}
	}
	t.mu.Unlock()

	// Only the exact "one pre-existing channel" case is numbered. If a
	// concurrent delete/create left some other count, skip rather than guess
	// which channel the "(1)" belongs on.
	if matches != 1 || firstID == "" {
		return
	}

	if name, ok := t.renameChannelIfUnaltered(firstID, assigned, nameWithIndex(base, 1)); ok {
		t.logEvent(fmt.Sprintf("🤖 Auto-renamed **%s** to **%s**, owner <@%s> now has 2 channels", assigned, name, userID))
	}
}

// demoteSoleChannelToUnnumbered strips the numeric suffix from a user's channel
// when a deletion has left them with exactly one, so a lone channel never
// keeps a "(n)". No-op when they have zero or several channels, when the sole
// channel is already unnumbered, or when it was renamed outside the bot.
func (t *tempVC) demoteSoleChannelToUnnumbered(ownerID string) {
	if ownerID == "" {
		return
	}
	t.mu.Lock()
	soleID, base, assigned, count := "", "", "", 0
	for id, owner := range t.owners {
		if owner == ownerID {
			soleID, base, assigned = id, t.baseName[id], t.assignedName[id]
			count++
		}
	}
	t.mu.Unlock()

	if count != 1 || soleID == "" {
		return
	}

	if name, ok := t.renameChannelIfUnaltered(soleID, assigned, truncateChannelName(base)); ok {
		t.logEvent(fmt.Sprintf("🤖 Auto-renumbered **%s** to **%s**, owner <@%s> back to 1 channel", assigned, name, ownerID))
	}
}

// renameChannelIfUnaltered renames a tracked channel to target, but only if its
// live name still matches what the bot last assigned, so a name a user or admin
// changed is never overwritten (the "don't touch altered names" rule). It marks
// the edit as the bot's own so the resulting CHANNEL_UPDATE is not re-logged as
// an owner rename, and updates the tracked assigned name on success. Returns the
// applied name and true when it renamed; the existing name and false when it
// skipped (already correct, altered, unreadable) or the edit failed. Off-lock.
func (t *tempVC) renameChannelIfUnaltered(channelID, assigned, target string) (string, bool) {
	if target == "" || assigned == target {
		return assigned, false // nothing to do (already the desired name)
	}

	live, err := t.mgr.Channel(channelID)
	if err != nil {
		utils.Warn("Temp VC rename skipped, could not read channel",
			"channel_id", channelID, "error", err)
		return assigned, false
	}
	if live.Name != assigned {
		utils.Info("Temp VC rename skipped, channel was altered outside the bot",
			"channel_id", channelID)
		return assigned, false
	}

	// Mark as our own rename before issuing it, so handleChannelUpdate does not
	// re-log the resulting CHANNEL_UPDATE as an owner rename.
	t.mu.Lock()
	t.pendingRename[channelID] = target
	t.mu.Unlock()

	if _, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{Name: target}); err != nil {
		t.mu.Lock()
		delete(t.pendingRename, channelID)
		t.mu.Unlock()
		utils.CaptureError("Temp VC rename failed", err,
			"channel_id", channelID, "guild_id", t.cfg.GuildID)
		return assigned, false
	}

	t.mu.Lock()
	// Only update bookkeeping if the channel is still tracked (it may have been
	// deleted while we were off-lock issuing the edit).
	if _, ok := t.assignedName[channelID]; ok {
		t.assignedName[channelID] = target
	}
	t.mu.Unlock()

	utils.Info("Temp VC renamed", "channel_id", channelID, "name", target)
	return target, true
}

// baseChannelName derives the un-suffixed "<RANK> <Name>'s Channel" name. The
// RANK comes from the member's rank ROLE (authoritative; see rankAbbrevFromRoles)
// and falls back to the rank parsed from the nickname only when no rank role is
// configured/held. The NAME comes from the nickname with its leading rank token
// and trailing callsigns stripped (see splitNick). The member object is resolved
// from the gateway event, then a REST fetch if the event carried no usable
// member; a missing name falls back to "Trooper". The result is NOT truncated,
// callers apply truncateChannelName / nameWithIndex once the numeric suffix (if
// any) is known.
func (t *tempVC) baseChannelName(vs *discordgo.VoiceStateUpdate) string {
	m := vs.Member
	if displayNameFromMember(m) == "" {
		// The gateway event carried no usable member; fetch to get the nickname
		// and roles for naming.
		if fetched, err := t.mgr.GuildMember(t.cfg.GuildID, vs.UserID); err == nil {
			m = fetched
		} else {
			utils.Warn("Temp VC name lookup failed, using fallback",
				"user_id", vs.UserID, "error", err)
		}
	}

	rankFromNick, nameCore := splitNick(displayNameFromMember(m))
	nameCore = enforceNameCasing(nameCore)
	rank := rankAbbrevFromRoles(m)
	if rank == "" {
		rank = rankFromNick
	}
	return composeChannelName(rank, nameCore) + tempVCNameSuffix
}

// enforceNameCasing applies the 7Cav "<Last>.<F>" casing convention to a name
// core. The last name (the part before the first ".") gets its first letter
// capitalized with the rest of its casing preserved, so intentional mid-name
// capitals (McCarthy) survive. Each following dot-segment is treated as an
// initials group and upper-cased when it is short (<= 3 characters), so
// "smith.j" becomes "Smith.J", "laui.m" becomes "Laui.M", and "smith.jw" becomes
// "Smith.JW". A longer following segment is left as a name word (only its first
// letter capitalized), so a malformed nick like "CAPT.Smith.J" is not shouted
// into "CAPT.SMITH.J".
func enforceNameCasing(name string) string {
	if name == "" {
		return name
	}
	segments := strings.Split(name, ".")
	for i, seg := range segments {
		if i > 0 && len([]rune(seg)) <= 3 {
			segments[i] = strings.ToUpper(seg)
		} else {
			segments[i] = upperFirstLetter(seg)
		}
	}
	return strings.Join(segments, ".")
}

// upperFirstLetter upper-cases the first letter of s, preserving the rest.
func upperFirstLetter(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// rankAbbrevFromRoles returns the abbreviation of the highest rank role the
// member holds, or "" when they hold none (or no rank roles are configured yet).
func rankAbbrevFromRoles(m *discordgo.Member) string {
	idx := lowestRoleIndex(m, rankRoleIndex, noRankIndex)
	if idx == noRankIndex {
		return ""
	}
	return tempVCRankRoles[idx].abbrev
}

// composeChannelName joins a rank (which may come from a role) and a name core
// into the un-suffixed base name, with a "Trooper" fallback when both are empty.
func composeChannelName(rank, nameCore string) string {
	switch {
	case rank != "" && nameCore != "":
		return rank + " " + nameCore
	case rank != "":
		return rank
	case nameCore != "":
		return nameCore
	default:
		return "Trooper"
	}
}

// truncateChannelName clamps a name to Discord's 100-character channel limit.
func truncateChannelName(s string) string {
	if len(s) > discordChannelNameLimit {
		return s[:discordChannelNameLimit]
	}
	return s
}

// nameWithIndex appends a " (n)" disambiguation suffix, first truncating the
// base so the whole result still fits Discord's channel-name limit. The suffix
// is always preserved (the base is what gets cut).
func nameWithIndex(base string, n int) string {
	suffix := fmt.Sprintf(" (%d)", n)
	if len(base)+len(suffix) > discordChannelNameLimit {
		base = base[:discordChannelNameLimit-len(suffix)]
	}
	return base + suffix
}

// nextChannelIndexLocked returns the numeric label for a user's new channel:
// the smallest positive integer not already in use by any channel they own (an
// unsuffixed channel is slot 1). Deriving it from the set actually in use,
// rather than the channel count, is what prevents a freed lower number from
// colliding with a surviving higher one: deleting the unsuffixed channel then
// creating another yields slot 1 again, never a second "(4)". Caller holds mu.
func (t *tempVC) nextChannelIndexLocked(userID string) int {
	used := make(map[int]bool)
	for id, owner := range t.owners {
		if owner == userID {
			used[suffixNumber(t.assignedName[id])] = true
		}
	}
	for n := 1; ; n++ {
		if !used[n] {
			return n
		}
	}
}

// suffixNumber reads the trailing " (n)" index off a bot-assigned channel name,
// returning n. A tracked name with no such suffix is the user's first channel,
// numbered 1. Empty (no assigned name, e.g. an adopted survivor) returns 0 so
// it never lays claim to slot 1.
func suffixNumber(assignedName string) int {
	if assignedName == "" {
		return 0
	}
	if strings.HasSuffix(assignedName, ")") {
		if open := strings.LastIndex(assignedName, " ("); open >= 0 {
			if n, err := strconv.Atoi(assignedName[open+2 : len(assignedName)-1]); err == nil && n >= 1 {
				return n
			}
		}
	}
	return 1
}

// displayNameFromMember extracts the best display name from a member object,
// preferring the server nickname.
func displayNameFromMember(m *discordgo.Member) string {
	if m == nil {
		return ""
	}
	if m.Nick != "" {
		return m.Nick
	}
	if m.User != nil {
		return m.User.Username
	}
	return ""
}

// canonicalRanks is the 7Cav rank-abbreviation set (US Army), used to detect the
// rank token at the head of a member nickname when deriving a channel NAME (see
// stripLeadingRank / parseTrooperName), the election itself keys off rank roles,
// not nicknames. It mirrors the authoritative list in the promotion code
// (commands/promo_eligibility.go on develop); kept as a local copy so the
// temp-VC feature stays self-contained. Derived from the rank-role table so the
// abbreviation set has a single source of truth.
var canonicalRanks = func() map[string]struct{} {
	set := make(map[string]struct{}, len(tempVCRankRoles))
	for _, rr := range tempVCRankRoles {
		set[rr.abbrev] = struct{}{}
	}
	return set
}()

// splitNick separates a member's raw display name into (rankFromNick, nameCore).
// 7Cav nicks follow RANK.Last.F (e.g. "1LT.Laui.M"), but real nicks drift: the
// rank/name separator may be a space or ". " ("1LT Laui.M", "1LT. Laui.M"), an
// aviation callsign may trail the name (`1LT.Laui.M "Bobo"`), and unauthorized
// suffixes creep in ("1LT.Laui.M LOA", "1LT.Laui.M <cadre>"). splitNick:
//   - strips a leading rank token (whitelist match, case-insensitive, tolerant
//     of the 0/O look-alike typo, see resolveRank) plus its "." or space
//     separator, returning it as rankFromNick, and
//   - returns the remainder as nameCore, dropping only TRAILING callsign /
//     suffix / decoration tokens (quoted, bracketed, a known suffix keyword, or a
//     pure-symbol token like an emoji, see isDroppableNameToken).
//
// Keeping the whole remainder (rather than just its first token) means a
// multi-word name like "Major Smith" is preserved instead of collapsing to
// "Major". When there is no leading rank token, rankFromNick is "" and nameCore
// is the whole trimmed display name. The channel-name builder (baseChannelName)
// prefers the rank ROLE over rankFromNick and composes "<rank> <nameCore>".
func splitNick(display string) (rankFromNick, nameCore string) {
	raw := strings.TrimSpace(display)
	if raw == "" {
		return "", ""
	}
	rank, rest, ok := stripLeadingRank(raw)
	if !ok {
		return "", raw
	}
	fields := strings.Fields(rest)
	for len(fields) > 0 && isDroppableNameToken(fields[len(fields)-1]) {
		fields = fields[:len(fields)-1]
	}
	return rank, strings.Join(fields, " ")
}

// isDroppableNameToken reports whether a trailing token is a callsign, suffix, or
// decoration to strip from a name rather than part of the name itself: a quoted
// or bracketed tag (`"Bobo"`, `<cadre>`), a known suffix keyword (LOA), or a
// token with no letters or digits (an emoji or stray punctuation).
func isDroppableNameToken(tok string) bool {
	if tok == "" {
		return true
	}
	switch tok[0] {
	case '"', '\'', '<', '[', '(':
		return true
	}
	switch strings.ToUpper(tok) {
	case "LOA":
		return true
	}
	for _, r := range tok {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// stripLeadingRank splits a leading rank token off s. The token is the run of
// characters up to the first "." or space; when it resolves to a canonical rank
// (case-insensitively, tolerating the 0/O typo) the canonical uppercase rank and
// the remainder (with leading "." / space separators trimmed) are returned with
// ok=true. Otherwise it returns ("", s, false) and the caller treats s as
// un-ranked.
func stripLeadingRank(s string) (rank, rest string, ok bool) {
	i := strings.IndexAny(s, ". ")
	if i <= 0 {
		return "", s, false
	}
	canonical, isRank := resolveRank(strings.ToUpper(s[:i]))
	if !isRank {
		return "", s, false
	}
	return canonical, strings.TrimLeft(s[i:], ". "), true
}

// resolveRank maps an already-uppercased leading token to its canonical rank. It
// matches directly, then retries with the 0/O look-alike deconfused ("W01" ->
// "WO1", "C0L" -> "COL"): no canonical rank contains a literal digit 0, so the
// substitution can never turn one valid rank into another and is safe to apply
// unconditionally.
func resolveRank(token string) (string, bool) {
	if _, ok := canonicalRanks[token]; ok {
		return token, true
	}
	if deconfused := strings.ReplaceAll(token, "0", "O"); deconfused != token {
		if _, ok := canonicalRanks[deconfused]; ok {
			return deconfused, true
		}
	}
	return "", false
}

// The /voice command group lets a temp channel's owner manage who may join,
// mirroring MEE6's owner controls for the actions Discord's native UI does not
// make one-click: blocking a member (deny Connect and disconnect them if
// present) and permitting them back. Rename / user-limit / lock / hide are left
// to Discord's own channel UI, which the owner can already use via the
// ManageChannels + ManageRoles overwrite granted at spawn time.
//
// The command acts on the temp channel the invoker is currently sitting in, and
// only the channel's owner (or the interim owner while the creator is away) may
// use it (the same control set the permission overwrite grants).

// voiceSubcommands are the choices for the /voice `command` option.
var voiceSubcommands = []string{"block", "permit"}

// tempVCBlockDeny is what a block overwrite denies the target: connecting to the
// channel. View is left intact so a blocked member can still see it exists.
const tempVCBlockDeny = discordgo.PermissionVoiceConnect

// voiceCommand builds the /voice slash command bound to this tempVC runtime, so
// the handlers can resolve and act on the invoker's live temp channel. Returned
// by StartTempVC for registration in the command registry.
func (t *tempVC) voiceCommand() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "voice",
			Description: "Manage your temporary voice channel",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "command",
					Description: "Choose between " + strings.Join(voiceSubcommands, ", "),
					Required:    true,
					Choices:     stringChoices(voiceSubcommands),
				},
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "The member to block or permit",
					Required:    true,
				},
			},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			t.runVoiceCommand(utils.NewSessionResponder(s), i)
		},
	}
}

// runVoiceCommand handles a /voice invocation: it resolves the invoker's current
// temp channel, authorizes them as its owner (or interim owner), and dispatches
// to block/permit. Responses are ephemeral (owner-management convention).
func (t *tempVC) runVoiceCommand(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		utils.HandleError(r, i, "❌ This command can only be used in a server (guild).")
		return
	}
	username, invokerID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting Voice", "command", "Voice", "username", username, "discord_id", invokerID)

	if err := deferEphemeral(r, i); err != nil {
		utils.CaptureError("Temp VC /voice defer failed", err, "user_id", invokerID)
		return
	}

	data := i.ApplicationCommandData()
	sub, ok := getOptionString(data, "command")
	if !ok || !slices.Contains(voiceSubcommands, sub) {
		editEphemeral(r, i, "❌ Invalid command; must be block or permit.")
		return
	}
	target := optionUserID(data, "user")
	if target == "" {
		editEphemeral(r, i, "❌ You must specify a member.")
		return
	}

	// Resolve the invoker's current temp channel and control status in one lock
	// hold. A blocked/permitted target is scoped to the channel the invoker is
	// physically in, matching the MEE6 "run it from inside your channel" model.
	t.mu.Lock()
	channelID := t.userChannel[invokerID]
	occ, isTemp := t.occupants[channelID]
	owner := t.owners[channelID]
	controller := t.controller[channelID]
	_, targetPresent := occ[target]
	t.mu.Unlock()

	if channelID == "" || !isTemp {
		editEphemeral(r, i, "❌ You must be sitting in a temporary voice channel you own to use this.")
		return
	}
	if invokerID != owner && invokerID != controller {
		editEphemeral(r, i, "❌ Only the channel's owner can block or permit members.")
		return
	}

	switch sub {
	case "block":
		t.voiceBlock(r, i, channelID, invokerID, owner, target, targetPresent)
	case "permit":
		t.voicePermit(r, i, channelID, invokerID, target)
	}
}

// voiceBlock denies the target Connect on the channel and disconnects them if
// they are currently inside. The owner can neither block themselves nor the
// channel's creator (a creator block would lock the owner out on their return).
func (t *tempVC) voiceBlock(r utils.InteractionResponder, i *discordgo.InteractionCreate, channelID, invokerID, owner, target string, targetPresent bool) {
	switch target {
	case invokerID:
		editEphemeral(r, i, "❌ You can't block yourself.")
		return
	case owner:
		editEphemeral(r, i, "❌ You can't block the channel's owner.")
		return
	}

	if err := t.mgr.ChannelPermissionSet(channelID, target, discordgo.PermissionOverwriteTypeMember, 0, tempVCBlockDeny); err != nil {
		utils.CaptureError("Temp VC block failed", err,
			"channel_id", channelID, "target_id", target, "invoker_id", invokerID)
		editEphemeral(r, i, "❌ Failed to block the member; the bot may be missing permissions.")
		return
	}

	// Kick them out if they are in the channel right now. Best-effort: the block
	// overwrite already prevents them rejoining, so a failed disconnect is
	// captured but not surfaced as a command failure.
	if targetPresent {
		if err := t.mgr.GuildMemberMove(t.cfg.GuildID, target, nil); err != nil {
			utils.CaptureError("Temp VC block disconnect failed", err,
				"channel_id", channelID, "target_id", target)
		}
	}

	t.logEvent(fmt.Sprintf("🚫 <@%s> blocked <@%s> from %s",
		invokerID, target, channelLabel(channelID, t.assignedNameOf(channelID))))
	editEphemeral(r, i, fmt.Sprintf("🚫 Blocked <@%s> from this channel.", target))
	utils.Info("✨ Done!", "command", "Voice", "action", "block")
}

// voicePermit clears the target's permission overwrite on the channel, undoing a
// prior block so they can connect again.
func (t *tempVC) voicePermit(r utils.InteractionResponder, i *discordgo.InteractionCreate, channelID, invokerID, target string) {
	if err := t.mgr.ChannelPermissionDelete(channelID, target); err != nil {
		utils.CaptureError("Temp VC permit failed", err,
			"channel_id", channelID, "target_id", target, "invoker_id", invokerID)
		editEphemeral(r, i, "❌ Failed to permit the member; the bot may be missing permissions.")
		return
	}

	t.logEvent(fmt.Sprintf("✅ <@%s> permitted <@%s> back into %s",
		invokerID, target, channelLabel(channelID, t.assignedNameOf(channelID))))
	editEphemeral(r, i, fmt.Sprintf("✅ Permitted <@%s> back into this channel.", target))
	utils.Info("✨ Done!", "command", "Voice", "action", "permit")
}

// optionUserID reads a user option's target ID. UserValue(nil) resolves the
// option to a *User carrying just the ID, which is all block/permit need (the
// same pattern as milpac.go). Returns "" when the option is absent.
func optionUserID(data discordgo.ApplicationCommandInteractionData, name string) string {
	for _, opt := range data.Options {
		if opt != nil && opt.Name == name {
			if u := opt.UserValue(nil); u != nil {
				return u.ID
			}
		}
	}
	return ""
}
