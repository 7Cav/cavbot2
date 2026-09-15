package commands

import (
	"fmt"
	"sync"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Temporary voice channels (issue #100, first slice of the MEE6 migration).
//
// A member joining any configured hub voice channel gets a voice channel
// spawned under that hub's category and is moved into it. Each hub carries its
// own defaults (name base, user limit, bitrate) that are stamped on the
// channels it spawns, so an "Arma" hub and a "Briefing" hub can behave
// differently. The channel is created with no permission overwrites of its
// own, so Discord copies its category's: the bot holds channel permissions and
// members get none. Ownership is a bot-internal marker (the creator, or an
// interim stand-in while the creator is away) that grants no Discord
// permission. Nothing is persisted.
//
// Cleanup is event-driven: a temp channel is deleted the moment its last
// occupant leaves. Orphans left by a bot restart are reaped by the
// GUILD_CREATE sweep. A hub's category is the marker for "temp", so any empty
// voice channel under any hub category (except the hubs themselves) is deleted
// on every connect/resume, and non-empty survivors are adopted with no recorded
// owner.
//
// docs/temp-vc-decisions.md records what is settled, what it removed from the
// original PR, and what is still open.
//
// This is the codebase's first gateway-event feature. GuildVoiceStates is an
// unprivileged intent already covered by IntentsAllWithoutPrivileged in
// main.go, so no identify or Developer Portal change is needed.

const (
	// maxTempChannelsPerUser caps concurrently owned temp channels. Four
	// supports the "operation host spinning up team channels" case; a fifth
	// hub join disconnects the user instead of creating another.
	maxTempChannelsPerUser = 4

	// discordChannelNameLimit is Discord's hard cap on channel name length.
	discordChannelNameLimit = 100
)

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

// tempVCLogChannelID is where the audit trail (create / join / leave / delete /
// rename / cap) is posted, so who did what is on record. It is shared across
// all hubs, one audit log for the whole feature. Hardcoded for the same reason
// as the hub IDs below: it is fixed 7Cav infrastructure.
const tempVCLogChannelID = "1530898079316705430"

// tempVCHub is one "join to create" hub and the defaults it stamps on the
// channels it spawns. A member joining HubChannelID gets a channel created
// under CategoryID named "<Name> - <n>" and carrying UserLimit / Bitrate.
type tempVCHub struct {
	// HubChannelID is the "join to create" hub voice channel.
	HubChannelID string
	// CategoryID is the category this hub's temp channels are spawned under. It
	// is also the durable marker the restart sweep keys on: every empty voice
	// channel under it (except a hub) is reaped on connect, which is how orphans
	// from a bot restart are cleaned up without persisted state, so it must
	// contain only this hub's temp channels.
	CategoryID string
	// Name is the base every channel spawned from this hub is named from:
	// "<Name> - <n>", numbered per hub from 1 (see nextChannelIndexLocked).
	Name string
	// UserLimit is the default max occupants stamped on spawned channels
	// (0 = unlimited, Discord's default).
	UserLimit int
	// Bitrate is the default bitrate in bits/sec for spawned channels
	// (0 = Discord's default, currently 64000).
	Bitrate int
}

// tempVCHubs is the hardcoded hub table. It is a stand-in until the panel and
// its store exist (docs/temp-vc-decisions.md): the values are the test guild's,
// and the names are placeholders. Add a hub by adding an entry (its own
// distinct hub channel and category); the runtime supports any number.
// mustTempVCHubs validates the table at init.
var tempVCHubs = mustTempVCHubs([]tempVCHub{
	{
		HubChannelID: "1391707962929709091",
		CategoryID:   "1391707962929709089",
		Name:         "Voice A",
	},
	{
		HubChannelID: "1530946025114828870",
		CategoryID:   "1530945960870543470",
		Name:         "Voice B",
	},
})

// mustTempVCHubs validates the hardcoded hub table at package init, panicking
// on a misconfiguration, an empty or reused hub/category ID, a hub that is its
// own category, or an empty name, so a bad edit fails at startup rather than
// silently half-working. Same fail-at-init stance as mustWeeklyFireTime in
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
		case h.Name == "":
			panic(fmt.Sprintf("tempVCHubs[%d]: name must be set", i))
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
	// ChannelMessageSend posts a plain message to a channel. Used for the audit
	// trail and to notify an over-cap hub joiner in the hub's chat (the message
	// return value is dropped as the other write sites here do, matching
	// /warden's GuildManager seam).
	ChannelMessageSend(channelID, content string) error
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

func (m *sessionTempVCManager) ChannelMessageSend(channelID, content string) error {
	_, err := m.s.ChannelMessageSend(channelID, content)
	return err
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
// false by default).
type tempVC struct {
	mgr TempVCManager
	cfg TempVCConfig

	// hubs, categories and hubNum are the config indexed for lookup, built once
	// in newTempVC and never mutated after, so they are read lock-free. hubs maps
	// a hub channel ID -> its hub (routing a hub join to the right category and
	// defaults); categories maps a temp category ID -> the hub that owns it (the
	// restart sweep uses it to tell which categories hold temp channels and to
	// record a survivor's hub on adoption); hubNum maps a hub channel ID -> its
	// 1-based position in the config, for the "from hub N" audit line.
	hubs       map[string]tempVCHub
	categories map[string]tempVCHub
	hubNum     map[string]int

	mu sync.Mutex
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
	// assignedName maps temp channel ID -> the name the bot created it with,
	// for audit lines that must stay readable after the channel is deleted.
	// Only bot-created channels have entries; adopted channels never carry one.
	assignedName map[string]string
	// channelHub maps a temp channel ID -> the hub channel it belongs to.
	// Recorded at creation and on adoption (from the survivor's category), so
	// per-hub numbering sees every live channel of a hub.
	channelHub map[string]string
	// channelIndex maps a bot-created temp channel ID -> the number in its
	// name. Adopted channels carry no entry, so they never hold a number.
	channelIndex map[string]int
	// controller maps a temp channel ID -> the member currently holding INTERIM
	// ownership because the creator has stepped out of the channel. Absent when
	// the creator is present or when no eligible member is available.
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
		mgr:          mgr,
		cfg:          cfg,
		hubs:         hubs,
		categories:   categories,
		hubNum:       hubNum,
		userChannel:  make(map[string]string),
		occupants:    make(map[string]map[string]struct{}),
		owners:       make(map[string]string),
		assignedName: make(map[string]string),
		channelHub:   make(map[string]string),
		channelIndex: make(map[string]int),
		controller:   make(map[string]string),
		memberMeta:   make(map[string]memberRankMeta),
	}
}

// StartTempVC wires the temp voice channel feature onto a Discord session:
// a GUILD_CREATE handler that seeds occupancy and sweeps orphans (fires on
// initial connect and again on any reconnect), a VOICE_STATE_UPDATE handler that
// drives the create/cleanup/interim-ownership lifecycle, and a CHANNEL_UPDATE
// handler for the audit trail. Call before dg.Open().
func StartTempVC(dg *discordgo.Session, cfg TempVCConfig) {
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
			"name", h.Name,
			"user_limit", h.UserLimit,
			"bitrate", h.Bitrate,
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
	t.userChannel = make(map[string]string)
	t.occupants = make(map[string]map[string]struct{})
	t.owners = make(map[string]string)
	t.assignedName = make(map[string]string)
	t.channelHub = make(map[string]string)
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
		// Record the survivor's hub from its category, so it counts as one of
		// that hub's live channels.
		t.channelHub[ch.ID] = hub.HubChannelID
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

	// A temp channel is one we track in occupants; capture whether the vacated
	// channel was one (and its name) before applyLeaveLocked mutates occupancy,
	// so a leave can be audit-logged by name.
	_, leftTemp := t.occupants[oldChannel]
	leftName := t.assignedName[oldChannel]
	emptied := t.applyLeaveLocked(vs.UserID, oldChannel)
	joinedTemp := t.applyJoinLocked(vs.UserID, newChannel)
	joinedName := t.assignedName[newChannel]
	ownedCount := t.ownedCountLocked(vs.UserID)
	// Re-elect interim ownership on both sides of the move: leaving may have made
	// a creator absent (hand off) or removed the interim holder (re-elect);
	// joining may have brought the creator back (hand back). Bookkeeping only;
	// ownership changes nothing in Discord.
	if leftTemp {
		t.reconcileControllerLocked(oldChannel)
	}
	if joinedTemp {
		t.reconcileControllerLocked(newChannel)
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
	// The vacated channel emptied: delete it now. Off-lock, the delete is a
	// network call.
	if emptied {
		t.deleteIfStillEmpty(oldChannel)
	}

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

// handleChannelUpdate audit-logs a rename of a tracked temp channel. It diffs
// the event's BeforeUpdate (discordgo's pre-update state cache) against the new
// channel, so an unrelated field change is not misreported as a rename.
// Non-temp channels are ignored. Best-effort: when BeforeUpdate is absent
// (channel not cached) there is no baseline, so it skips.
//
// CHANNEL_UPDATE does not name an actor. The line is attributed to the
// channel's owner (the interim stand-in while the creator is away), which held
// while owners renamed through Discord's own UI. The bot now holds channel
// permissions, so a rename reaching here came from an admin, or from the bot
// on an owner's behalf once /voice-rename exists. What the audit trail should
// say then is open in docs/temp-vc-decisions.md.
func (t *tempVC) handleChannelUpdate(c *discordgo.ChannelUpdate) {
	if c == nil || c.Channel == nil || c.GuildID != t.cfg.GuildID {
		return
	}

	t.mu.Lock()
	_, tracked := t.occupants[c.ID]
	owner := t.owners[c.ID]
	if ctrl, ok := t.controller[c.ID]; ok {
		owner = ctrl
	}
	t.mu.Unlock()

	if !tracked || c.BeforeUpdate == nil {
		return
	}
	before := c.BeforeUpdate

	if before.Name != c.Name {
		t.logEvent(fmt.Sprintf("✏️ **%s** renamed to **%s**%s", before.Name, c.Name, actorSuffix(owner)))
	}
}

// actorSuffix renders who performed a channel edit for the audit line. Owner
// edits are attributed to the owner; an owner-less adopted channel can only
// have been edited by an admin.
func actorSuffix(owner string) string {
	if owner == "" {
		return " by a server admin"
	}
	return fmt.Sprintf(" by <@%s>", owner)
}

// applyLeaveLocked removes the user from a temp channel's occupancy and
// reports whether that emptied it. The caller deletes an emptied channel
// off-lock. Caller holds mu.
func (t *tempVC) applyLeaveLocked(userID, channelID string) bool {
	members, ok := t.occupants[channelID]
	if !ok {
		return false
	}
	delete(members, userID)
	return len(members) == 0
}

// applyJoinLocked adds the user to a temp channel's occupancy. Returns whether
// the joined channel is temp-managed. Caller holds mu.
func (t *tempVC) applyJoinLocked(userID, channelID string) bool {
	members, ok := t.occupants[channelID]
	if !ok {
		return false
	}
	members[userID] = struct{}{}
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

// rememberMemberLocked caches a member's rank index, read from their roles,
// for future elections. Caller holds mu.
func (t *tempVC) rememberMemberLocked(userID string, m *discordgo.Member) {
	t.memberMeta[userID] = memberRankMeta{
		rankIdx: lowestRoleIndex(m, rankRoleIndex, noRankIndex),
	}
}

// electInterimControllerLocked picks the member who should hold interim
// ownership of a bot-created channel whose creator is currently absent. Every
// occupant is eligible; they are ordered by rank role (the present occupant
// with the highest of tempVCRankRoles wins), then by lowest user ID for
// determinism. Returns "" only when the channel is empty. Caller holds mu.
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
// best (bm, buid) for interim ownership: higher rank, then lower user ID.
func outranksForInterim(m memberRankMeta, uid string, bm memberRankMeta, buid string) bool {
	if m.rankIdx != bm.rankIdx {
		return m.rankIdx < bm.rankIdx
	}
	return uid < buid
}

// reconcileControllerLocked recomputes who should hold interim ownership of a
// temp channel and records it in controller. The creator keeps their claim;
// interim control applies only while the creator is out of the channel.
// Adopted channels (no recorded creator) never get an interim owner. An
// emptied channel is left alone, it is about to be deleted. Caller holds mu.
func (t *tempVC) reconcileControllerLocked(channelID string) {
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

// deleteIfStillEmpty deletes a temp channel that just emptied. It re-checks
// occupancy under the lock first: the leave that emptied the channel was
// applied under the lock, but a join can land in the gap before this runs.
func (t *tempVC) deleteIfStillEmpty(channelID string) {
	t.mu.Lock()
	members, tracked := t.occupants[channelID]
	if !tracked || len(members) > 0 {
		t.mu.Unlock()
		return
	}
	// Ownership/occupancy tracking is kept until Discord confirms the delete,
	// because a channel that fails to delete is still live and MUST keep
	// counting toward its owner's cap. Removing it before the API call is what
	// let a user exceed the cap with zombie channels when deletes 403'd
	// (missing Manage Channels).
	t.mu.Unlock()

	if _, err := t.mgr.ChannelDelete(channelID); err != nil {
		// Still live, still tracked, so it still counts toward the cap. The
		// GUILD_CREATE sweep on the next reconnect is the retry (no in-cycle
		// retry, so a persistent permission fault does not flood Sentry); a
		// rejoin+leave of this channel also retries the delete.
		utils.CaptureError("Temp VC delete failed", err,
			"channel_id", channelID, "guild_id", t.cfg.GuildID)
		return
	}

	t.mu.Lock()
	deletedName := t.assignedName[channelID]
	deletedOwner := t.owners[channelID]
	delete(t.occupants, channelID)
	delete(t.owners, channelID)
	delete(t.assignedName, channelID)
	delete(t.channelHub, channelID)
	delete(t.channelIndex, channelID)
	delete(t.controller, channelID)
	t.mu.Unlock()
	utils.Info("Temp VC deleted", "channel_id", channelID)
	t.logEvent(deleteLogLine(channelID, deletedName, deletedOwner))
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

// handleHubJoin creates a channel for a hub joiner (or disconnects them if
// they're at the ownership cap) and moves them into it. Runs without the lock
// held, creation and moves are network calls, and re-locks only to commit
// tracking state.
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

	// Every channel a hub spawns is "<hub name> - <n>", n being the smallest
	// number no live channel of that hub holds (see nextChannelIndexLocked),
	// NOT the channel count, which collides after a lower-numbered channel is
	// deleted.
	t.mu.Lock()
	index := t.nextChannelIndexLocked(hub.HubChannelID)
	t.mu.Unlock()
	name := nameWithIndex(hub.Name, index)

	// Stamp the hub's per-hub defaults on the new channel. UserLimit and Bitrate
	// of 0 are Discord's own defaults, so a hub that leaves them unset creates a
	// plain unlimited channel. No PermissionOverwrites: an omitted list makes
	// Discord copy the category's, which is how the channel inherits its
	// parent (docs/research/discord-channel-overwrites.md).
	channel, err := t.mgr.GuildChannelCreateComplex(t.cfg.GuildID, discordgo.GuildChannelCreateData{
		Name:      name,
		Type:      discordgo.ChannelTypeGuildVoice,
		ParentID:  hub.CategoryID,
		UserLimit: hub.UserLimit,
		Bitrate:   hub.Bitrate,
	})
	if err != nil {
		utils.CaptureError("Temp VC create failed", err,
			"user_id", vs.UserID, "guild_id", t.cfg.GuildID)
		return
	}

	t.mu.Lock()
	t.occupants[channel.ID] = make(map[string]struct{})
	t.owners[channel.ID] = vs.UserID
	t.assignedName[channel.ID] = name
	t.channelHub[channel.ID] = hub.HubChannelID
	t.channelIndex[channel.ID] = index
	t.mu.Unlock()

	if err := t.mgr.GuildMemberMove(t.cfg.GuildID, vs.UserID, &channel.ID); err != nil {
		// The user vanished (disconnected mid-create) or the move was
		// refused; without them the new channel would sit empty, so reap it
		// immediately.
		utils.CaptureError("Temp VC move-into failed, deleting channel", err,
			"user_id", vs.UserID, "channel_id", channel.ID)
		t.mu.Lock()
		delete(t.occupants, channel.ID)
		delete(t.owners, channel.ID)
		delete(t.assignedName, channel.ID)
		delete(t.channelHub, channel.ID)
		delete(t.channelIndex, channel.ID)
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
}

// notifyCapReached posts a message in the joined hub's chat tagging a joiner
// who hit the ownership cap, so the disconnect is not silent. Posted to the hub
// the user actually joined. Best-effort: a send failure is captured but never
// blocks. The copy carries no em dash, per the user-facing-copy style.
func (t *tempVC) notifyCapReached(userID string, hub tempVCHub) {
	// A channel frees a slot the moment its last occupant leaves. Spell that
	// out so the user does not leave a still-occupied channel and expect the
	// cap to drop.
	msg := fmt.Sprintf(
		"<@%s> You already own the maximum of %d temporary voice channels. Empty one of yours, it is deleted as soon as the last person leaves, then rejoin the hub.",
		userID, maxTempChannelsPerUser,
	)
	if err := t.mgr.ChannelMessageSend(hub.HubChannelID, msg); err != nil {
		utils.CaptureError("Temp VC cap notification failed", err,
			"user_id", userID, "channel_id", hub.HubChannelID)
	}
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
// smallest positive integer no live bot-created channel of that hub holds.
// Deriving it from the set actually in use, rather than the channel count, is
// what prevents a freed lower number from colliding with a surviving higher
// one: deleting "- 1" then creating another yields "- 1" again, never a second
// "- 4". Adopted survivors hold no number, so a survivor's name can be reused;
// Discord allows duplicate channel names. Caller holds mu.
func (t *tempVC) nextChannelIndexLocked(hubID string) int {
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
