package panel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Deps is what the hub page acts through: the store the rows live in, the
// runtime the saves apply to, and the manager seam the guild is read through.
// main fills it after StartTempVC; the tests fill it with the store fake, a
// runtime built over their Discord fake, and that fake.
type Deps struct {
	Store   store.Store
	Runtime *commands.TempVC
	Manager commands.TempVCManager
	GuildID string
}

// hubService is the service layer under the hub page's handlers: validation,
// the store calls, the guild's reads from the gateway state and the Discord
// calls through the manager seam, and the runtime updates. Handlers parse
// the request, call one function here, and render what comes back.
type hubService struct {
	deps Deps
	// storeTimeout is the deadline forSave gives each store call a save
	// makes. New sets it to the constant of the same name; a test shortens
	// it.
	storeTimeout time.Duration
	// guildWait bounds a save's wait for the guild's data while it is on
	// its way, since a save's context has no deadline: forSave sets it to
	// the page's time budget. Zero for a page load, whose context carries
	// the budget.
	guildWait time.Duration
	// saveLock is the lock under which the saves of this process take turns
	// (#373). create, register, update, remove and the guild-wide
	// moderator save each take it before their first store read and hold it
	// until the running bot has their update. So each save reads what the save
	// before it wrote, and the running bot gets saves in the order the store
	// took them.
	//
	// One lock covers all five. They share the hub rows and the running
	// bot: a create or a register can meet another register on a channel,
	// an update can meet a remove on one hub, and every save updates the
	// running bot after its write. Saves are rare, a few people saving by
	// hand, so taking turns one at a time costs nothing a finer lock would
	// save, and no one has to work out which saves can meet.
	//
	// A save holds it through its Discord calls, the rename or the channel
	// create between its read and its write. Released around them, another
	// save could read and write in between, landing after this save's
	// version check and before its write, and the running bot could get the
	// two updates out of order. It has no wait limit of its own: every step
	// the holder runs is bounded, each store call by storeTimeout and each
	// Discord call by discordgo's client timeout, so a save waits at most
	// for the bounded steps of the saves ahead of it.
	//
	// A pointer, since forSave copies the service and every copy must share
	// the one lock.
	saveLock *sync.Mutex
}

// hubRow is one hub as the list shows it: the stored settings, the channel
// and category names read from the guild at this page load, the broken hub
// state derived from the same read, and the live spawned count from the
// runtime.
type hubRow struct {
	ID         int64
	BaseString string
	// ChannelName is empty when the hub channel is not in the guild.
	ChannelName string
	// CategoryName is empty when the hub channel has no parent.
	CategoryName string
	// Broken is the broken hub state of CONTEXT.md: the hub channel is gone
	// from the guild, or has no category. Discord's state makes it so, and
	// the list offers Remove alone.
	Broken  bool
	Enabled bool
	Spawned int
	// Failure is the hub's last spawn failure since its last successful
	// spawn, from the runtime. Nil when there is none.
	Failure *commands.SpawnFailure
}

// pickerChannel is one category the create form's category picker offers.
type pickerChannel struct {
	ID   string
	Name string
}

// registerInput is the register form as posted. The service trims and
// validates it; the template renders it back on a refusal so nothing typed
// is lost.
type registerInput struct {
	ChannelID  string
	BaseString string
}

// createInput is the create form as posted: the category to create the hub
// channel under, the channel's name and the base string. The service trims
// and validates it; the template renders it back on a refusal.
type createInput struct {
	CategoryID  string
	ChannelName string
	BaseString  string
}

// editInput is the edit form as posted: strings as the browser sent them,
// parsed and validated by the service, and rendered back on a refusal so
// nothing typed is lost. The hub channel is not a field: it is fixed at
// create or register. Its name is one: a changed name renames the channel
// on save.
type editInput struct {
	// Version is the hub's version when the form loaded, from a hidden
	// input (ADR 0013: the form works without script). A save whose
	// version the hub is no longer at is a stale form.
	Version     string
	ChannelName string
	// LoadedChannelName is the hub channel's name when the form loaded,
	// from a hidden input.
	LoadedChannelName string
	BaseString        string
	PermissionSource  string
	ModeratorRoleIDs  []string
	UserLimit         string
	Bitrate           string
	DeleteDelay       string
	Enabled           bool
	RenamingAllowed   bool
	LockingAllowed    bool
}

// actor is the signed-in forum user a save is recorded against.
type actor struct {
	userID   int
	username string
}

// String names the actor the way an audit log reason does.
func (a actor) String() string {
	return fmt.Sprintf("%s (forum user %d)", a.username, a.userID)
}

// fieldError is a refused save: the field refused and why, or, with stale
// set, a stale form (#373), which names no field. The note the page shows
// carries DataError as its data-error attribute, a test contract; the
// message is not.
type fieldError struct {
	Field   string
	Message string
	// stale marks a stale form's refusal, which answers 409 and logs an
	// INFO line, where every other refusal answers 422.
	stale bool
}

func (e *fieldError) Error() string { return e.DataError() + ": " + e.Message }

// DataError is the data-error value on the refusal's note: the refused
// field's name, or refusalStale for a stale form.
func (e *fieldError) DataError() string {
	if e.stale {
		return refusalStale
	}
	return e.Field
}

// errAlreadyHub is the refusal a register of a channel a hub already stands
// on gets.
var errAlreadyHub = &fieldError{Field: fieldHubChannel, Message: "That channel is already a hub."}

// errHubBroken is the refusal an update of a broken hub gets: its channel is
// gone or has no category, so there is nothing to rename and nothing to
// spawn under. It carries no message: the handler answers with the Broken
// hub view, which explains the state and offers Remove alone, and that view
// renders no refusal note.
var errHubBroken = &fieldError{Field: fieldHubChannel}

// Form field names, as posted and as named in a refusal.
const (
	fieldHubChannel       = "hub_channel"
	fieldCategory         = "category"
	fieldChannelName      = "channel_name"
	fieldBaseString       = "base_string"
	fieldPermissionSource = "permission_source"
	fieldModeratorRoles   = "moderator_roles"
	fieldUserLimit        = "user_limit"
	fieldBitrate          = "bitrate"
	fieldDeleteDelay      = "delete_delay_minutes"
	fieldEnabled          = "enabled"
	fieldRenamingAllowed  = "renaming_allowed"
	fieldLockingAllowed   = "locking_allowed"
	// fieldVersion and fieldLoadedChannelName are the edit form's hidden
	// inputs, and fieldVersion the guild-wide section's too.
	fieldVersion           = "version"
	fieldLoadedChannelName = "loaded_channel_name"
)

// refusalStale is the data-error value on a stale refusal's note, a test
// contract like the field names.
const refusalStale = "stale"

// errStaleHub is the refusal a save from a stale hub form gets: the hub is
// not at the version the form loaded. Nothing is written and no Discord
// call is made. The handler answers it with 409 and the form as posted,
// carrying the hub's version now, so a second save goes through.
var errStaleHub = &fieldError{stale: true, Message: "Someone saved this hub after you opened this form, so your changes were not saved. " +
	"Their save is at the top of the change log below. Your values are still in the form. Save again to keep them."}

// formIsCurrent reports whether a form was loaded at the version its
// record is at now. A version that is missing or does not parse is not
// current.
//
// A save checks twice. It checks here first, at its read of the record,
// before validation and before any Discord call, so a stale form is refused
// with nothing done: no rename from it, and no refusal of a field whose
// value it may only have carried. The store checks again at the write,
// since the save lock orders the saves of this process alone. Another
// process writing the same settings, such as two containers overlapping in
// a deploy, can land between the read and the write, and the write refuses
// to go over it.
func formIsCurrent(posted string, version int64) bool {
	v, err := strconv.ParseInt(strings.TrimSpace(posted), 10, 64)
	return err == nil && v == version
}

// postedBackVersion is the version a form rendered back after a refusal
// carries, the hub's edit form and the guild-wide section alike. After a
// stale refusal it is the record's version now, stored, so the user's next
// save goes through over the save the note points at (#373 rule 5). After
// any other refusal it is the version the form posted, so a save of it
// still meets any save that landed since the form loaded (rule 6).
func postedBackVersion(stored int64, posted string, stale bool) string {
	if stale {
		return strconv.FormatInt(stored, 10)
	}
	return posted
}

// Bounds on the base string. Discord's channel name limit is 100 and the
// runtime appends " - n", so 90 leaves room for the number.
const (
	baseStringMin = 1
	baseStringMax = 90
)

// Bounds on a hub channel's name: Discord's channel name limit.
const (
	channelNameMin = 1
	channelNameMax = 100
)

// Defaults a create or register writes for the fields the form does not
// carry.
const (
	defaultUserLimit = 0
	defaultBitrate   = 64000
)

// Bounds on the user limit and the bitrate. The user limit is Discord's
// range, 0 meaning no limit. The bitrate floor is Discord's; the ceiling is
// the guild's boost tier's, read from the gateway state at save through
// bitrateCeiling.
const (
	userLimitMin = 0
	userLimitMax = 99
	bitrateMin   = 8000
)

// Bounds on the delete delay, in whole minutes (#372 Q3). 0 deletes a
// spawned channel the moment it empties. The ceiling bounds how many empty
// channels a busy hub holds against Discord's 50 channels per category.
const (
	deleteDelayMin = 0
	deleteDelayMax = 240
)

// bitrateCeiling is the highest bitrate Discord accepts on a voice channel
// of a guild at the given boost tier, in bits per second. Discord raises it
// with each tier; a guild that loses a tier keeps its channels as they are,
// so the bound applies to a save and never to a stored row.
func bitrateCeiling(tier discordgo.PremiumTier) int {
	switch tier {
	case discordgo.PremiumTier1:
		return 128000
	case discordgo.PremiumTier2:
		return 256000
	case discordgo.PremiumTier3:
		return 384000
	default:
		return 96000
	}
}

// hubPage is what the hub page renders from. The create and register forms
// show together, or, with Edit set, one hub's edit form alone. Error names
// the refused field and Refused the form it belongs to, so the note renders
// on the form that was posted; Register and Create are those forms as
// posted, so nothing typed is lost on a refusal.
type hubPage struct {
	// Disconnected is the bot's gateway connection down at this load: the
	// page shows the guild as the bot held it when the connection dropped,
	// and says its channel and role details may be out of date.
	Disconnected bool
	// Moderators is the guild-wide section at the top of the page.
	Moderators moderatorsPage
	Hubs       []hubRow
	// Picker is the register picker: the chosen channel as a tag, if any,
	// and the voice channels its channel search offers.
	Picker     pickerView
	Categories []pickerChannel
	Register   registerInput
	Create     createInput
	Error      *fieldError
	// Refused is which form the error belongs to: formCreate, formRegister,
	// formEdit or formModerators. Empty with no error.
	Refused string
	Edit    *editPage
}

// The forms a refusal can belong to, as Refused names them and as the
// template asks RefusalFor.
const (
	formCreate     = "create"
	formRegister   = "register"
	formEdit       = "edit"
	formModerators = "moderators"
)

// RefusalFor is the refusal to render on a form, or nil when the error
// belongs to another form or there is none. The template calls it once per
// form so the note lands on the form that was posted.
func (p hubPage) RefusalFor(form string) *fieldError {
	if p.Refused != form {
		return nil
	}
	return p.Error
}

// staleFor reports whether the request shows a stale refusal on the form.
func (r pageRequest) staleFor(form string) bool {
	return r.Refused == form && r.Error != nil && r.Error.stale
}

// pageRequest is what a handler asks the page to show beyond the list:
// which hub's edit form, if any, and the form as posted with its refusal
// when a save was just refused, so nothing typed is lost.
type pageRequest struct {
	// HubID names the hub whose edit form shows. Zero shows the register
	// form.
	HubID int64
	// Register is the register form as posted back after a refusal.
	Register registerInput
	// Create is the create form as posted back after a refusal.
	Create createInput
	// Edit is the edit form as posted back after a refusal. Nil shows the
	// stored values.
	Edit *editInput
	// Moderators is the guild-wide section's form as posted back after a
	// refusal. Nil shows the stored set.
	Moderators *moderatorsInput
	Error      *fieldError
	// Refused is the form Error belongs to.
	Refused string
}

// editPage is one hub's edit form: the category the form shows read-only,
// the hub's moderator picker, and the form's fields as stored or as posted
// back after a refusal.
type editPage struct {
	ID int64
	// Broken is the broken hub state: the section shows the remove form and
	// the change log, and no edit form.
	Broken bool
	// CategoryName is empty when the hub channel has no parent.
	CategoryName string
	// Picker is the hub's own moderator picker.
	Picker pickerView
	// GuildRoles are the guild-wide moderator roles, shown read-only above
	// the hub's own picker so the effective set is visible. Only the
	// guild-wide section changes them.
	GuildRoles []pickerItem
	// BitrateMax is the ceiling the guild's boost tier allows, for the
	// input's own bound.
	BitrateMax int
	Form       editInput
	// Changes are the hub's last entries, newest first.
	Changes []changeView
}

// unavailableReason is why a stored moderator role is no longer eligible:
// deleted, when no live role has the ID, or managed.
type unavailableReason string

const (
	unavailableDeleted unavailableReason = "deleted"
	unavailableManaged unavailableReason = "managed"
)

// editInputOf is the edit form as the stored hub and its live channel name
// fill it.
func editInputOf(h store.Hub, channelName string) editInput {
	return editInput{
		Version:           strconv.FormatInt(h.Version, 10),
		LoadedChannelName: channelName,
		ChannelName:       channelName,
		BaseString:        h.BaseString,
		PermissionSource:  string(h.PermissionSource),
		ModeratorRoleIDs:  h.ModeratorRoleIDs,
		UserLimit:         strconv.Itoa(h.UserLimit),
		Bitrate:           strconv.Itoa(h.Bitrate),
		DeleteDelay:       strconv.Itoa(h.DeleteDelayMinutes),
		Enabled:           h.Enabled,
		RenamingAllowed:   h.RenamingAllowed,
		LockingAllowed:    h.LockingAllowed,
	}
}

// guildChannels is the guild's channel list as read at one page load,
// indexed for the lookups the page makes.
type guildChannels struct {
	byID map[string]*discordgo.Channel
	all  []*discordgo.Channel
}

// channel returns the channel with the ID, if the guild has one.
func (g guildChannels) channel(id string) (*discordgo.Channel, bool) {
	ch, ok := g.byID[id]
	return ch, ok
}

// voiceChannel returns the channel with the ID when the guild has it and it
// is a voice channel.
func (g guildChannels) voiceChannel(id string) (*discordgo.Channel, bool) {
	ch, ok := g.byID[id]
	if !ok || ch.Type != discordgo.ChannelTypeGuildVoice {
		return nil, false
	}
	return ch, true
}

// hasCategory reports whether the guild has a category with the ID.
func (g guildChannels) hasCategory(id string) bool {
	ch, ok := g.byID[id]
	return ok && ch.Type == discordgo.ChannelTypeGuildCategory
}

// categories returns the guild's categories in the list's order.
func (g guildChannels) categories() []*discordgo.Channel {
	var out []*discordgo.Channel
	for _, ch := range g.all {
		if ch.Type == discordgo.ChannelTypeGuildCategory {
			out = append(out, ch)
		}
	}
	return out
}

// voiceChannels returns the guild's voice channels in the list's order.
func (g guildChannels) voiceChannels() []*discordgo.Channel {
	var out []*discordgo.Channel
	for _, ch := range g.all {
		if ch.Type == discordgo.ChannelTypeGuildVoice {
			out = append(out, ch)
		}
	}
	return out
}

// categoryName is the name of a channel's parent, or empty when it has none
// or the parent is not in the list.
func (g guildChannels) categoryName(ch *discordgo.Channel) string {
	if ch == nil || ch.ParentID == "" {
		return ""
	}
	if parent, ok := g.byID[ch.ParentID]; ok {
		return parent.Name
	}
	return ""
}

// snapshot is what the page, a create and a register start from: the
// guild's hub rows and one read of the guild from the gateway state.
type snapshot struct {
	hubs []store.Hub
	guildState
}

// hubChannelState is a hub's channel as the guild list has it at this
// read: its name and its category's, each empty when absent, and the
// broken hub state of CONTEXT.md, the channel gone or with no category.
type hubChannelState struct {
	Name         string
	CategoryName string
	Broken       bool
}

// hubChannel reads a hub's channel off the guild's channel list.
func (g guildChannels) hubChannel(h store.Hub) hubChannelState {
	ch, ok := g.channel(h.HubChannelID)
	if !ok {
		return hubChannelState{Broken: true}
	}
	return hubChannelState{Name: ch.Name, CategoryName: g.categoryName(ch), Broken: ch.ParentID == ""}
}

// hubByID returns the hub row with the ID, if there is one.
func (sn snapshot) hubByID(id int64) (store.Hub, bool) {
	for _, h := range sn.hubs {
		if h.ID == id {
			return h, true
		}
	}
	return store.Hub{}, false
}

// hubOn returns the hub row on a channel, if there is one.
func (sn snapshot) hubOn(channelID string) (store.Hub, bool) {
	for _, h := range sn.hubs {
		if h.HubChannelID == channelID {
			return h, true
		}
	}
	return store.Hub{}, false
}

// The names the page's store reads' errors carry, which a page that ran out
// of time also reports each read under (#395). A read the page's time budget
// ran out on fails under the same name as its own failure.
const (
	listHubsRead            = "list hubs"
	guildModeratorRolesRead = "read guild moderator roles"
	moderatorChangesRead    = "list moderator changes"
	changeLogRead           = "list change log"
)

// read reads the guild from the gateway state, then the hub rows from the
// store.
func (s *hubService) read(ctx context.Context) (snapshot, error) {
	guild, err := s.readGuild(ctx)
	if err != nil {
		return snapshot{}, err
	}
	hubs, err := s.deps.Store.ListHubs(ctx, s.deps.GuildID)
	if err != nil {
		return snapshot{}, fmt.Errorf("%s: %w", listHubsRead, err)
	}
	return snapshot{hubs: hubs, guildState: guild}, nil
}

// guildState is the guild as one read of the gateway state gives it: its
// channel list, its roles and boost tier, and whether the bot's gateway
// connection was down, which leaves the state as it was when the
// connection dropped.
type guildState struct {
	channels     guildChannels
	info         guildInfo
	disconnected bool
}

// errNoGuildData is a page load or a save that found no data for the guild
// in the gateway state: Discord has not sent it to the bot. Its page is its
// own, not a failed read's.
var errNoGuildData = errors.New("the gateway state holds no data for the guild")

// guildDataPoll is how often a read that found the guild's data on its way
// looks again.
const guildDataPoll = 100 * time.Millisecond

// readGuild reads the guild's channels, roles and boost tier from the
// gateway state through the manager seam, never Discord's API, once per
// page load or save, so the page and a save's checks see the guild as the
// bot holds it now.
//
// While the state holds the placeholder a READY leaves, the data is on its
// way, and readGuild looks again until it lands or ctx ends. A deadline
// that ends the wait fails with errNoGuildData, not the deadline, so the
// page says Discord has not sent the data rather than that it took too
// long. A guild absent from the state fails with errNoGuildData at once:
// Discord has marked it unavailable, and waiting would cost the whole
// budget.
func (s *hubService) readGuild(ctx context.Context) (guildState, error) {
	if s.guildWait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.guildWait)
		defer cancel()
	}
	poll := time.NewTicker(guildDataPoll)
	defer poll.Stop()
	for {
		data := s.deps.Manager.GuildData(s.deps.GuildID)
		switch data.Status {
		case commands.GuildDataPresent:
			return s.guildStateOf(data), nil
		case commands.GuildDataAbsent:
			return guildState{}, fmt.Errorf("%w: the guild is absent", errNoGuildData)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return guildState{}, fmt.Errorf("%w: still on its way when the wait ran out", errNoGuildData)
			}
			return guildState{}, fmt.Errorf("wait for guild data: %w", ctx.Err())
		case <-poll.C:
		}
	}
}

// lockSave takes the save lock once the gateway state holds the guild's
// data, and returns its unlock. The wait comes first, so saves made while
// the data is on its way wait for it side by side: waiting in turn, a queue
// of them would each wait out a budget after the one before and outlast the
// reverse proxy's timeout. A save that finds no data fails with
// errNoGuildData without taking its turn. Under the lock the save reads the
// guild again, since the saves ahead of it may have changed it.
func (s *hubService) lockSave(ctx context.Context) (func(), error) {
	if _, err := s.readGuild(ctx); err != nil {
		return nil, err
	}
	s.saveLock.Lock()
	return s.saveLock.Unlock, nil
}

// guildStateOf indexes one read of the guild for the lookups the page and
// the saves make.
func (s *hubService) guildStateOf(data commands.GuildSnapshot) guildState {
	channels := guildChannels{byID: make(map[string]*discordgo.Channel, len(data.Channels)), all: data.Channels}
	for _, ch := range data.Channels {
		channels.byID[ch.ID] = ch
	}
	return guildState{channels: channels, info: s.guildInfoOf(data), disconnected: !data.Connected}
}

// guildInfo is what one read of the guild gives a page load or a save: the
// eligible roles the two moderator pickers offer and a save may add, the
// name of every role for the change log, and the bitrate ceiling the
// guild's boost tier allows.
type guildInfo struct {
	// eligible are the eligible roles of CONTEXT.md, live, not managed and
	// not @everyone, highest position first.
	eligible []*discordgo.Role
	// live is every role but @everyone, keyed by ID: the eligible ones and
	// the managed ones, for the reason a stored role is unavailable.
	live map[string]*discordgo.Role
	// names maps each role's ID to its name, for a change log entry that
	// stores IDs. Every role the guild returned is here, managed and
	// @everyone included, so an older entry that names one still reads.
	names      map[string]string
	bitrateMax int
}

// unavailability is why a role with the ID is not eligible, or empty when
// it is. The rule lives here alone: a role is eligible when it is live,
// not managed and not @everyone. A stored @everyone reads as deleted, since
// live leaves it out, and nothing can store it today.
func (g guildInfo) unavailability(id string) unavailableReason {
	r, ok := g.live[id]
	switch {
	case !ok:
		return unavailableDeleted
	case r.Managed:
		return unavailableManaged
	}
	return ""
}

// isEligible reports whether a save may add the role with the ID.
func (g guildInfo) isEligible(id string) bool {
	return g.unavailability(id) == ""
}

// guildInfoOf builds the eligible roles, the role names and the bitrate
// bound from one read of the guild. The @everyone role, whose ID is the
// guild's, is not live here: every member holds it.
func (s *hubService) guildInfoOf(g commands.GuildSnapshot) guildInfo {
	info := guildInfo{eligible: make([]*discordgo.Role, 0, len(g.Roles)), live: make(map[string]*discordgo.Role, len(g.Roles)),
		names: make(map[string]string, len(g.Roles)), bitrateMax: bitrateCeiling(g.PremiumTier)}
	for _, r := range g.Roles {
		info.names[r.ID] = r.Name
		if r.ID != s.deps.GuildID {
			info.live[r.ID] = r
		}
	}
	for _, r := range g.Roles {
		if info.isEligible(r.ID) {
			info.eligible = append(info.eligible, r)
		}
	}
	sort.Slice(info.eligible, func(i, j int) bool { return info.eligible[i].Position > info.eligible[j].Position })
	return info
}

// page reads everything the hub page renders from, once: the guild from
// the gateway state, for the list, the pickers, the guild-wide section and
// the edit form, then from the store the hub rows, the guild-wide set and
// its last entries, and, when an edit form shows, the hub's last entries.
// store.ErrNotFound means no hub has the requested ID. ctx carries the
// page's time budget; a store read the budget runs out on fails with it.
func (s *hubService) page(ctx context.Context, req pageRequest) (hubPage, error) {
	sn, err := s.read(ctx)
	if err != nil {
		return hubPage{}, err
	}
	page := hubPage{Disconnected: sn.disconnected, Hubs: s.rows(sn), Picker: channelPicker(sn, req.Register.ChannelID), Categories: categoryPicker(sn),
		Register: req.Register, Create: req.Create, Error: req.Error, Refused: req.Refused}
	guildWide, err := s.deps.Store.GetGuildModeratorRoles(ctx, s.deps.GuildID)
	if err != nil {
		return hubPage{}, fmt.Errorf("%s: %w", guildModeratorRolesRead, err)
	}
	if page.Moderators, err = s.moderatorsSection(ctx, sn.info, guildWide, req); err != nil {
		return hubPage{}, err
	}
	if req.HubID != 0 {
		if page.Edit, err = s.editForm(ctx, sn, sn.info, guildWide.RoleIDs, req); err != nil {
			return hubPage{}, err
		}
	}
	return page, nil
}

// rows merges the store's hub rows with the guild's channel list and the
// runtime's live state: the spawned count and the last spawn failure.
func (s *hubService) rows(sn snapshot) []hubRow {
	rows := make([]hubRow, 0, len(sn.hubs))
	for _, h := range sn.hubs {
		row := hubRow{ID: h.ID, BaseString: h.BaseString, Enabled: h.Enabled, Spawned: s.deps.Runtime.SpawnedCount(h.ID)}
		if f, ok := s.deps.Runtime.LastSpawnFailure(h.ID); ok {
			row.Failure = &f
		}
		st := sn.channels.hubChannel(h)
		row.ChannelName, row.CategoryName, row.Broken = st.Name, st.CategoryName, st.Broken
		rows = append(rows, row)
	}
	// The store promises no order. Base string, then ID, so two hubs with one
	// base string keep their places between loads.
	sort.Slice(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].BaseString), strings.ToLower(rows[j].BaseString)
		if a != b {
			return a < b
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// categoryPicker builds the create form's category picker from the guild's
// categories, by name.
func categoryPicker(sn snapshot) []pickerChannel {
	var out []pickerChannel
	for _, ch := range sn.channels.categories() {
		out = append(out, pickerChannel{ID: ch.ID, Name: ch.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// editForm builds the edit form of the hub the request names from the
// snapshot and the guild read: the stored values, or the form as posted
// when a save was refused, with the version postedBackVersion gives it, the
// hub's own moderator picker, the guild-wide set shown read-only, and the
// hub's last entries. The hub is read before its entries, so the log shown
// holds every save up to the hub's version as read here.
func (s *hubService) editForm(ctx context.Context, sn snapshot, guild guildInfo, guildWide []string, req pageRequest) (*editPage, error) {
	hub, ok := sn.hubByID(req.HubID)
	if !ok {
		return nil, store.ErrNotFound
	}
	entries, err := s.deps.Store.ListChangeLog(ctx, hub.ID, changeLogLimit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", changeLogRead, err)
	}
	st := sn.channels.hubChannel(hub)
	form := editInputOf(hub, st.Name)
	if req.Edit != nil {
		form = *req.Edit
		form.Version = postedBackVersion(hub.Version, req.Edit.Version, req.staleFor(formEdit))
	}
	page := &editPage{ID: hub.ID, Broken: st.Broken, CategoryName: st.CategoryName, Form: form,
		Picker: rolePicker(guild, hub.ModeratorRoleIDs, form.ModeratorRoleIDs), BitrateMax: guild.bitrateMax,
		GuildRoles: rolePicker(guild, guildWide, guildWide).Tags, Changes: changeViews(entries, guild.names)}
	return page, nil
}

// newHub is a hub on a channel with the defaults a create or register
// writes, before the store fills its ID and times. Renaming is on: the
// store writes the field as given, so leaving it out would store it off.
func (s *hubService) newHub(channelID, baseString string) store.Hub {
	return store.Hub{
		GuildID:          s.deps.GuildID,
		HubChannelID:     channelID,
		BaseString:       baseString,
		PermissionSource: store.PermissionCategory,
		ModeratorRoleIDs: []string{},
		UserLimit:        defaultUserLimit,
		Bitrate:          defaultBitrate,
		Enabled:          true,
		RenamingAllowed:  true,
	}
}

// saveHub writes the hub and its change log entry in one store call, then
// applies the stored hub to the runtime, so a join spawns from it at once
// with no restart. The runtime learns only of a write that landed: a failed
// write leaves it on the settings the store still holds.
func (s *hubService) saveHub(ctx context.Context, hub store.Hub, action store.ChangeAction, d diff, by actor) (store.Hub, error) {
	entry, err := changeEntry(action, d, by)
	if err != nil {
		return store.Hub{}, err
	}
	stored, err := s.deps.Store.SaveHub(ctx, hub, entry)
	if err != nil {
		return store.Hub{}, err
	}
	s.deps.Runtime.ApplyHub(stored)
	return stored, nil
}

// create makes a new hub in one step: a voice channel under the chosen
// category, created with overwrites omitted so it takes the category's
// permissions from birth, then the hub row with the defaults and its change
// log entry, which carries every field with a null before and the channel
// name typed, applied to the runtime the way a register is. A refusal is a
// *fieldError naming the field. A create Discord refuses is a *fieldError on
// the category field, since the category cap and a category the bot cannot
// see are what Discord refuses on, with a body-free phrase, and no row is
// written. A write that fails deletes the channel just made, so Discord and
// the store never disagree, and returns the error.
func (s *hubService) create(ctx context.Context, in createInput, by actor) (store.Hub, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	defer unlock()
	in.CategoryID = strings.TrimSpace(in.CategoryID)
	baseString, err := validBaseString(in.BaseString)
	if err != nil {
		return store.Hub{}, err
	}
	in.BaseString = baseString
	channelName, err := validChannelName(in.ChannelName)
	if err != nil {
		return store.Hub{}, err
	}
	in.ChannelName = channelName
	sn, err := s.read(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	if !sn.channels.hasCategory(in.CategoryID) {
		return store.Hub{}, &fieldError{Field: fieldCategory, Message: "That category is no longer in the server. Choose another."}
	}

	ch, err := s.deps.Manager.GuildChannelCreateComplex(s.deps.GuildID, discordgo.GuildChannelCreateData{
		Name:      in.ChannelName,
		Type:      discordgo.ChannelTypeGuildVoice,
		ParentID:  in.CategoryID,
		UserLimit: defaultUserLimit,
		Bitrate:   defaultBitrate,
	}, "Panel: hub created by "+by.String())
	if err != nil {
		utils.Warn("Panel hub channel create refused", "category_id", in.CategoryID, "error", err)
		return store.Hub{}, &fieldError{Field: fieldCategory, Message: "Discord did not create the channel under that category: " + commands.DiscordErrorDetail(err) + "."}
	}

	hub := s.newHub(ch.ID, in.BaseString)
	d := diffHubs(nil, &hub)
	d[fieldChannelName] = change{Before: nil, After: in.ChannelName}
	stored, err := s.saveHub(ctx, hub, store.ChangeCreate, d, by)
	if err != nil {
		if _, delErr := s.deps.Manager.ChannelDelete(ch.ID, "Panel: hub row write failed"); delErr != nil {
			utils.CaptureError("Panel hub channel left behind after a failed row write", delErr, "channel_id", ch.ID)
		}
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	return stored, nil
}

// register makes an existing voice channel a hub with the defaults, writes
// the row with a change log entry carrying every field, and applies it to
// the runtime, so a join spawns from it at once with no restart. A refusal
// is a *fieldError naming the field; the store is not written and the
// runtime is not touched.
func (s *hubService) register(ctx context.Context, in registerInput, by actor) (store.Hub, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	defer unlock()
	in.ChannelID = strings.TrimSpace(in.ChannelID)
	baseString, err := validBaseString(in.BaseString)
	if err != nil {
		return store.Hub{}, err
	}
	in.BaseString = baseString
	// The register picker cannot make the browser require a choice the way
	// the select it replaced did, so an empty post gets its own answer.
	if in.ChannelID == "" {
		return store.Hub{}, &fieldError{Field: fieldHubChannel, Message: "Choose a voice channel."}
	}
	sn, err := s.read(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	ch, ok := sn.channels.voiceChannel(in.ChannelID)
	if !ok {
		return store.Hub{}, &fieldError{Field: fieldHubChannel, Message: "That channel is no longer a voice channel in the server. Choose another."}
	}
	if _, taken := sn.hubOn(in.ChannelID); taken {
		return store.Hub{}, errAlreadyHub
	}
	if ch.ParentID == "" {
		return store.Hub{}, &fieldError{Field: fieldHubChannel, Message: "That channel has no category. Move it into one first."}
	}

	hub := s.newHub(in.ChannelID, in.BaseString)
	stored, err := s.saveHub(ctx, hub, store.ChangeRegister, diffHubs(nil, &hub), by)
	if errors.Is(err, store.ErrStale) {
		// Another process made the channel a hub after this save's read.
		return store.Hub{}, errAlreadyHub
	}
	if err != nil {
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	return stored, nil
}

// update saves a hub's settings from the edit form: it writes the row with
// a change log entry of the changed fields and applies it to the runtime, so
// a disabled hub stops spawning at once, a change to "Renaming allowed" or
// "Locking allowed" reaches /voice-rename or /voice-lock at once, and a
// changed delete delay reaches the channels already waiting. A refusal is a
// *fieldError naming the field, and nothing is written; store.ErrNotFound
// means no hub has the ID, and an update never makes one.
//
// A stale form is refused before anything else, as errStaleHub (#373). Then
// a broken hub: its channel is gone or has no category, and the page offers
// Remove alone.
//
// The hub channel's name is not stored, so no version covers it, and a
// rename made in Discord never makes a form stale. The save renames the
// channel only when the user changed the name field from the name the form
// loaded; otherwise a rename made in Discord since stands. A rename goes to
// Discord before the row saves, with an audit log reason naming the panel
// user, and joins the entry's diff as channel_name, its before the live
// name. A rename Discord refuses is a *fieldError on the name field with a
// body-free phrase, and nothing is written, so the row and Discord never
// disagree.
//
// The order is rename, then the row and its entry in one store write, then
// the runtime. A write that fails after a rename leaves the channel renamed
// and the row as it was (#356), and its error names both channel names so
// the mismatch is traceable from Sentry. That error never reads as
// store.ErrNotFound, even when another process removed the hub between the
// read and the write: the rename changed something, so the save failed
// with a 500, and the 404 is for an update that changed nothing (#373
// rules 7 and 9).
func (s *hubService) update(ctx context.Context, hubID int64, in editInput, by actor) (store.Hub, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	defer unlock()
	before, err := s.deps.Store.GetHub(ctx, hubID)
	if err != nil {
		return store.Hub{}, fmt.Errorf("read hub: %w", err)
	}
	if !formIsCurrent(in.Version, before.Version) {
		return store.Hub{}, errStaleHub
	}
	guild, err := s.readGuild(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	st := guild.channels.hubChannel(before)
	if st.Broken {
		return store.Hub{}, errHubBroken
	}
	// The name as the guild list had it at this read, before any rename.
	oldName := st.Name
	channelName, err := validChannelName(in.ChannelName)
	if err != nil {
		return store.Hub{}, err
	}
	rename := channelName != strings.TrimSpace(in.LoadedChannelName) && channelName != oldName
	hub := before
	if err := applyEdit(&hub, in, guild.info); err != nil {
		return store.Hub{}, err
	}
	d := diffHubs(&before, &hub)
	if rename {
		reason := "Panel: hub channel renamed by " + by.String()
		if _, err := s.deps.Manager.ChannelEdit(hub.HubChannelID, &discordgo.ChannelEdit{Name: channelName}, reason); err != nil {
			utils.Warn("Panel hub channel rename refused", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID, "error", err)
			return store.Hub{}, &fieldError{Field: fieldChannelName, Message: "Discord did not rename the channel: " + commands.DiscordErrorDetail(err) + "."}
		}
		d[fieldChannelName] = change{Before: oldName, After: channelName}
	}

	stored, err := s.saveHub(ctx, hub, store.ChangeUpdate, d, by)
	switch {
	case err == nil:
		return stored, nil
	case rename:
		// The store's error is named, not wrapped, so a hub removed
		// meanwhile does not turn this failure into a 404.
		return store.Hub{}, fmt.Errorf("write hub after renaming its channel from %q to %q: %v", oldName, channelName, err)
	case errors.Is(err, store.ErrStale):
		// Another process saved the hub after this save's read.
		return store.Hub{}, errStaleHub
	}
	return store.Hub{}, fmt.Errorf("write hub: %w", err)
}

// remove deletes a hub's row with a change log entry carrying every field
// with a null after, in one store write, then drops the hub from the
// runtime, so a join to its channel spawns nothing more. No Discord call:
// the hub channel stays, so a removal is undone by registering the channel
// again. Spawned channels of the hub keep their rows and die when empty,
// which the runtime does on its own; those already waiting out the hub's
// delete delay go at once. store.ErrNotFound means no hub has the ID, at
// the read or, when another process removed it in between, at the write;
// either way nothing is written.
//
// The entry references no hub: the row is gone, and the store clears the
// hub's earlier entries to match, so the whole log of a removed hub lists
// under no hub.
func (s *hubService) remove(ctx context.Context, hubID int64, by actor) (store.Hub, error) {
	s.saveLock.Lock()
	defer s.saveLock.Unlock()
	hub, err := s.deps.Store.GetHub(ctx, hubID)
	if err != nil {
		return store.Hub{}, err
	}
	entry, err := changeEntry(store.ChangeRemove, diffHubs(&hub, nil), by)
	if err != nil {
		return store.Hub{}, err
	}
	if err := s.deps.Store.RemoveHub(ctx, hub.ID, entry); err != nil {
		return store.Hub{}, fmt.Errorf("remove hub: %w", err)
	}
	s.deps.Runtime.RemoveHub(hub.HubChannelID)
	return hub, nil
}

// applyEdit validates the edit form against the guild as read now and puts
// its values on the hub. The first refusal wins, as a *fieldError naming the
// field; the hub is then half written and must not be stored.
func applyEdit(hub *store.Hub, in editInput, guild guildInfo) error {
	baseString, err := validBaseString(in.BaseString)
	if err != nil {
		return err
	}
	hub.BaseString = baseString
	switch source := store.PermissionSource(in.PermissionSource); source {
	case store.PermissionCategory, store.PermissionHubChannel:
		hub.PermissionSource = source
	default:
		return &fieldError{Field: fieldPermissionSource, Message: "Choose where spawned channels take their permissions from."}
	}
	// hub still carries the stored set here: what an unavailable role may
	// be kept from.
	roles, err := acceptedRoles(in.ModeratorRoleIDs, guild, hub.ModeratorRoleIDs)
	if err != nil {
		return err
	}
	hub.ModeratorRoleIDs = roles
	var ok bool
	if hub.UserLimit, ok = intInRange(in.UserLimit, userLimitMin, userLimitMax); !ok {
		return &fieldError{Field: fieldUserLimit,
			Message: fmt.Sprintf("Enter a user limit of %d to %d. 0 means no limit.", userLimitMin, userLimitMax)}
	}
	if hub.Bitrate, ok = intInRange(in.Bitrate, bitrateMin, guild.bitrateMax); !ok {
		return &fieldError{Field: fieldBitrate,
			Message: fmt.Sprintf("Enter a bitrate of %d to %d.", bitrateMin, guild.bitrateMax)}
	}
	if hub.DeleteDelayMinutes, ok = intInRange(in.DeleteDelay, deleteDelayMin, deleteDelayMax); !ok {
		return &fieldError{Field: fieldDeleteDelay,
			Message: fmt.Sprintf("Enter a delete delay of %d to %d minutes. 0 deletes a channel the moment it empties.", deleteDelayMin, deleteDelayMax)}
	}
	hub.Enabled = in.Enabled
	hub.RenamingAllowed = in.RenamingAllowed
	hub.LockingAllowed = in.LockingAllowed
	return nil
}

// validBaseString trims a posted base string and checks its length. A
// refusal is a *fieldError naming the field.
func validBaseString(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(base); n < baseStringMin || n > baseStringMax {
		return "", &fieldError{Field: fieldBaseString,
			Message: fmt.Sprintf("Enter a base string of %d to %d characters.", baseStringMin, baseStringMax)}
	}
	return base, nil
}

// validChannelName trims a posted hub channel name and checks its length
// against Discord's limit. A refusal is a *fieldError naming the field.
func validChannelName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < channelNameMin || n > channelNameMax {
		return "", &fieldError{Field: fieldChannelName,
			Message: fmt.Sprintf("Enter a channel name of %d to %d characters.", channelNameMin, channelNameMax)}
	}
	return name, nil
}

// intInRange parses a posted number and reports whether it lies in [lo, hi].
func intInRange(raw string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return n, err == nil && n >= lo && n <= hi
}

// asFieldError reports whether an error is a refused save, a stale form's
// included.
func asFieldError(err error) (*fieldError, bool) {
	var fe *fieldError
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}
