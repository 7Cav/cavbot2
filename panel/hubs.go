package panel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
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
// the store calls, the Discord reads through the manager seam, and the
// runtime updates. Handlers parse the request, call one function here, and
// render what comes back.
type hubService struct {
	deps Deps
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

// pickerChannel is one voice channel the register form offers.
type pickerChannel struct {
	ID           string
	Name         string
	CategoryName string
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
	ChannelName      string
	BaseString       string
	PermissionSource string
	ModeratorRoleIDs []string
	UserLimit        string
	Bitrate          string
	Enabled          bool
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

// fieldError is a validation refusal: which field, and why. The field name
// is the form field's name and the data-error attribute on the note the
// page shows, a test contract; the message is not.
type fieldError struct {
	Field   string
	Message string
}

func (e *fieldError) Error() string { return e.Field + ": " + e.Message }

// errHubBroken is the refusal an update of a broken hub gets: its channel is
// gone or has no category, so there is nothing to rename and nothing to
// spawn under. It carries no message: the handler answers with the Broken
// hub view, which explains the state and offers Remove alone, and that view
// renders no refusal note.
var errHubBroken = &fieldError{fieldHubChannel, ""}

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
	fieldEnabled          = "enabled"
)

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
// the guild's boost tier's, read live at save through bitrateCeiling.
const (
	userLimitMin = 0
	userLimitMax = 99
	bitrateMin   = 8000
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
	// Moderators is the guild-wide section at the top of the page.
	Moderators moderatorsPage
	Hubs       []hubRow
	Picker     []pickerChannel
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
// the guild's roles the moderator picker offers, and the form's fields as
// stored or as posted back after a refusal.
type editPage struct {
	ID int64
	// Broken is the broken hub state: the section shows the remove form and
	// the change log, and no edit form.
	Broken bool
	// CategoryName is empty when the hub channel has no parent.
	CategoryName string
	Roles        []guildRole
	// GuildRoles are the guild-wide moderator roles, shown read-only above
	// the hub's own picker so the effective set is visible. Only the
	// guild-wide section changes them.
	GuildRoles []guildRole
	// BitrateMax is the ceiling the guild's boost tier allows, for the
	// input's own bound.
	BitrateMax int
	Form       editInput
	// Changes are the hub's last entries, newest first.
	Changes []changeView
}

// guildRole is one control of a moderator picker: an eligible role the
// picker offers, or an unavailable moderator role the record stores, and
// whether the form has it checked.
type guildRole struct {
	ID string
	// Name is the role's name, or its ID for a deleted role, whose name
	// nothing remembers.
	Name    string
	Checked bool
	// Unavailable is why a stored role is no longer eligible, and empty on
	// an eligible role. It is the data-unavailable attribute on the
	// control, a test contract; the label copy is not.
	Unavailable unavailable
}

// unavailable is the reason a stored moderator role is no longer eligible.
type unavailable string

const (
	// unavailableDeleted: no live role has the ID.
	unavailableDeleted unavailable = "deleted"
	// unavailableManaged: the live role is managed by an integration.
	unavailableManaged unavailable = "managed"
)

// editInputOf is the edit form as the stored hub and its live channel name
// fill it.
func editInputOf(h store.Hub, channelName string) editInput {
	return editInput{
		ChannelName:      channelName,
		BaseString:       h.BaseString,
		PermissionSource: string(h.PermissionSource),
		ModeratorRoleIDs: h.ModeratorRoleIDs,
		UserLimit:        strconv.Itoa(h.UserLimit),
		Bitrate:          strconv.Itoa(h.Bitrate),
		Enabled:          h.Enabled,
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

// snapshot is what the page and every save start from: the guild's hub rows
// and its channel list, each read once.
type snapshot struct {
	hubs  []store.Hub
	guild guildChannels
}

// hubChannelState is a hub's channel as the guild list has it at this
// read: its name and its category's, each empty when absent, and the
// broken hub state of CONTEXT.md, the channel gone or with no category.
type hubChannelState struct {
	Name         string
	CategoryName string
	Broken       bool
}

// hubChannel reads a hub's channel off the snapshot.
func (sn snapshot) hubChannel(h store.Hub) hubChannelState {
	ch, ok := sn.guild.channel(h.HubChannelID)
	if !ok {
		return hubChannelState{Broken: true}
	}
	return hubChannelState{Name: ch.Name, CategoryName: sn.guild.categoryName(ch), Broken: ch.ParentID == ""}
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

func (s *hubService) read(ctx context.Context) (snapshot, error) {
	hubs, err := s.deps.Store.ListHubs(ctx, s.deps.GuildID)
	if err != nil {
		return snapshot{}, fmt.Errorf("list hubs: %w", err)
	}
	channels, err := s.deps.Manager.GuildChannels(s.deps.GuildID)
	if err != nil {
		return snapshot{}, fmt.Errorf("guild channels: %w", err)
	}
	guild := guildChannels{byID: make(map[string]*discordgo.Channel, len(channels)), all: channels}
	for _, ch := range channels {
		guild.byID[ch.ID] = ch
	}
	return snapshot{hubs: hubs, guild: guild}, nil
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

// isEligible reports whether a role may be offered and added: live, not
// managed and not @everyone.
func (g guildInfo) isEligible(id string) bool {
	r, ok := g.live[id]
	return ok && !r.Managed
}

// unavailability is why a stored role is no longer eligible, or empty when
// it is eligible. A stored @everyone reads as deleted: the guild read drops
// it, and nothing can store it today.
func (g guildInfo) unavailability(id string) unavailable {
	r, ok := g.live[id]
	switch {
	case !ok:
		return unavailableDeleted
	case r.Managed:
		return unavailableManaged
	}
	return ""
}

// readGuild reads the guild through the manager seam, once per page load or
// save, so the roles offered and the bitrate bound are the guild's now. A
// managed role is one Discord made for an integration, a bot's own role or
// the booster role, and no picker offers it. The @everyone role, whose ID
// is the guild's, is left out too; every member holds it.
func (s *hubService) readGuild() (guildInfo, error) {
	g, err := s.deps.Manager.Guild(s.deps.GuildID)
	if err != nil {
		return guildInfo{}, fmt.Errorf("guild read: %w", err)
	}
	info := guildInfo{eligible: make([]*discordgo.Role, 0, len(g.Roles)), live: make(map[string]*discordgo.Role, len(g.Roles)),
		names: make(map[string]string, len(g.Roles)), bitrateMax: bitrateCeiling(g.PremiumTier)}
	for _, r := range g.Roles {
		info.names[r.ID] = r.Name
		if r.ID == s.deps.GuildID {
			continue
		}
		info.live[r.ID] = r
		if !r.Managed {
			info.eligible = append(info.eligible, r)
		}
	}
	sort.Slice(info.eligible, func(i, j int) bool { return info.eligible[i].Position > info.eligible[j].Position })
	return info, nil
}

// rolePicker builds a moderator picker: the eligible roles, then one
// unavailable moderator role per ID in stored that is no longer eligible,
// by ID, so the record's whole set is on the form and a save can keep or
// remove each. Those in checked are ticked. On a page load stored and
// checked are one set; on a refusal, checked is the form as posted, so an
// unticked unavailable role stays unticked and a posted ID the record
// never stored renders no control.
func rolePicker(guild guildInfo, stored, checked []string) []guildRole {
	out := make([]guildRole, 0, len(guild.eligible))
	for _, r := range guild.eligible {
		out = append(out, guildRole{ID: r.ID, Name: r.Name, Checked: slices.Contains(checked, r.ID)})
	}
	var kept []guildRole
	for _, id := range stored {
		reason := guild.unavailability(id)
		if reason == "" {
			continue
		}
		name := id
		if r, ok := guild.live[id]; ok {
			name = r.Name
		}
		kept = append(kept, guildRole{ID: id, Name: name, Checked: slices.Contains(checked, id), Unavailable: reason})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
	return append(out, kept...)
}

// page reads everything the hub page renders from, once: the hub rows and
// the guild's channel list for the list and the picker, the guild's roles
// for the guild-wide section and the edit form, the guild-wide set and its
// last entries, and, when an edit form shows, the hub's last entries.
// store.ErrNotFound means no hub has the requested ID.
func (s *hubService) page(ctx context.Context, req pageRequest) (hubPage, error) {
	sn, err := s.read(ctx)
	if err != nil {
		return hubPage{}, err
	}
	guild, err := s.readGuild()
	if err != nil {
		return hubPage{}, err
	}
	page := hubPage{Hubs: s.rows(sn), Picker: picker(sn), Categories: categoryPicker(sn),
		Register: req.Register, Create: req.Create, Error: req.Error, Refused: req.Refused}
	guildWide, err := s.deps.Store.GetGuildModeratorRoles(ctx, s.deps.GuildID)
	if err != nil {
		return hubPage{}, fmt.Errorf("read guild moderator roles: %w", err)
	}
	if page.Moderators, err = s.moderatorsSection(ctx, guild, guildWide, req.Moderators); err != nil {
		return hubPage{}, err
	}
	if req.HubID != 0 {
		if page.Edit, err = s.editForm(ctx, sn, guild, guildWide, req.HubID, req.Edit); err != nil {
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
		st := sn.hubChannel(h)
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

// picker builds the register picker from the voice channels that are not
// hubs.
func picker(sn snapshot) []pickerChannel {
	var out []pickerChannel
	for _, ch := range sn.guild.voiceChannels() {
		if _, taken := sn.hubOn(ch.ID); taken {
			continue
		}
		out = append(out, pickerChannel{ID: ch.ID, Name: ch.Name, CategoryName: sn.guild.categoryName(ch)})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CategoryName != b.CategoryName {
			return a.CategoryName < b.CategoryName
		}
		return a.Name < b.Name
	})
	return out
}

// categoryPicker builds the create form's category picker from the guild's
// categories, by name.
func categoryPicker(sn snapshot) []pickerChannel {
	var out []pickerChannel
	for _, ch := range sn.guild.categories() {
		out = append(out, pickerChannel{ID: ch.ID, Name: ch.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// editForm builds one hub's edit form from the snapshot and the guild read:
// the stored values, or the form as posted when a save was refused, the
// guild's roles for the hub's own picker, the guild-wide set shown
// read-only, and the hub's last entries.
func (s *hubService) editForm(ctx context.Context, sn snapshot, guild guildInfo, guildWide []string, hubID int64, posted *editInput) (*editPage, error) {
	hub, ok := sn.hubByID(hubID)
	if !ok {
		return nil, store.ErrNotFound
	}
	entries, err := s.deps.Store.ListChangeLog(ctx, hub.ID, changeLogLimit)
	if err != nil {
		return nil, fmt.Errorf("list change log: %w", err)
	}
	st := sn.hubChannel(hub)
	form := editInputOf(hub, st.Name)
	if posted != nil {
		form = *posted
	}
	page := &editPage{ID: hub.ID, Broken: st.Broken, CategoryName: st.CategoryName, Form: form,
		Roles: rolePicker(guild, hub.ModeratorRoleIDs, form.ModeratorRoleIDs), BitrateMax: guild.bitrateMax,
		Changes: changeViews(entries, guild.names)}
	for _, r := range rolePicker(guild, guildWide, guildWide) {
		if r.Checked {
			page.GuildRoles = append(page.GuildRoles, r)
		}
	}
	return page, nil
}

// newHub is a hub on a channel with the defaults a create or register
// writes, before the store fills its ID and times.
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
	}
}

// applyNew is what follows a new hub's row write: the runtime learns the hub,
// so a join spawns from it at once with no restart, and the change log gets
// the entry carrying every field with a null before, plus extra, the fields
// the action carries beyond the stored ones.
func (s *hubService) applyNew(ctx context.Context, stored store.Hub, action store.ChangeAction, extra diff, by actor) error {
	s.deps.Runtime.ApplyHub(stored)
	d := diffHubs(nil, &stored)
	for field, c := range extra {
		d[field] = c
	}
	return s.appendChange(ctx, stored.ID, action, d, by)
}

// create makes a new hub in one step: a voice channel under the chosen
// category, created with overwrites omitted so it takes the category's
// permissions from birth, then the hub row with the defaults, applied to
// the runtime and change-logged the way a register is, with the channel
// name typed in the entry. A refusal is a *fieldError naming the field. A
// create Discord refuses is a *fieldError on the category field, since the
// category cap and a category the bot cannot see are what Discord refuses
// on, with a body-free phrase, and no row is written. A row write that
// fails deletes the channel just made, so Discord and the store never
// disagree, and returns the error.
func (s *hubService) create(ctx context.Context, in createInput, by actor) (store.Hub, error) {
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
	if !sn.guild.hasCategory(in.CategoryID) {
		return store.Hub{}, &fieldError{fieldCategory, "That category is no longer in the server. Choose another."}
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
		return store.Hub{}, &fieldError{fieldCategory, "Discord did not create the channel under that category: " + commands.DiscordErrorDetail(err) + "."}
	}

	stored, err := s.deps.Store.UpsertHub(ctx, s.newHub(ch.ID, in.BaseString))
	if err != nil {
		if _, delErr := s.deps.Manager.ChannelDelete(ch.ID, "Panel: hub row write failed"); delErr != nil {
			utils.CaptureError("Panel hub channel left behind after a failed row write", delErr, "channel_id", ch.ID)
		}
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	named := diff{fieldChannelName: {Before: nil, After: in.ChannelName}}
	if err := s.applyNew(ctx, stored, store.ChangeCreate, named, by); err != nil {
		return store.Hub{}, err
	}
	return stored, nil
}

// register makes an existing voice channel a hub with the defaults, writes
// the row, applies it to the runtime, so a join spawns from it at once with
// no restart, and appends a change log entry carrying every field. A
// refusal is a *fieldError naming the field; the store is not written and
// the runtime is not touched.
func (s *hubService) register(ctx context.Context, in registerInput, by actor) (store.Hub, error) {
	in.ChannelID = strings.TrimSpace(in.ChannelID)
	baseString, err := validBaseString(in.BaseString)
	if err != nil {
		return store.Hub{}, err
	}
	in.BaseString = baseString
	sn, err := s.read(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	ch, ok := sn.guild.voiceChannel(in.ChannelID)
	if !ok {
		return store.Hub{}, &fieldError{fieldHubChannel, "That channel is no longer a voice channel in the server. Choose another."}
	}
	if _, taken := sn.hubOn(in.ChannelID); taken {
		return store.Hub{}, &fieldError{fieldHubChannel, "That channel is already a hub."}
	}
	if ch.ParentID == "" {
		return store.Hub{}, &fieldError{fieldHubChannel, "That channel has no category. Move it into one first."}
	}

	stored, err := s.deps.Store.UpsertHub(ctx, s.newHub(in.ChannelID, in.BaseString))
	if err != nil {
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	if err := s.applyNew(ctx, stored, store.ChangeRegister, nil, by); err != nil {
		return store.Hub{}, err
	}
	return stored, nil
}

// update saves a hub's settings from the edit form, writes the row, applies
// it to the runtime, so a disabled hub stops spawning at once, and appends a
// change log entry with the changed fields. A refusal is a *fieldError
// naming the field, and nothing is written; store.ErrNotFound means no hub
// has the ID.
//
// A broken hub is refused before anything else: its channel is gone or has
// no category, and the page offers Remove alone. A changed hub channel name
// renames the channel in Discord before the row saves, with an audit log
// reason naming the panel user. A rename Discord refuses is a *fieldError
// on the name field with a body-free phrase, and nothing is written, so
// the row and Discord never disagree. The rename joins the entry's diff as
// channel_name.
//
// The order is rename, row, runtime, entry. The runtime apply cannot fail,
// so once the row is written the runtime matches the store; an entry the
// store refuses is an error the handler reports, with the save already
// made.
func (s *hubService) update(ctx context.Context, hubID int64, in editInput, by actor) (store.Hub, error) {
	sn, err := s.read(ctx)
	if err != nil {
		return store.Hub{}, err
	}
	before, ok := sn.hubByID(hubID)
	if !ok {
		return store.Hub{}, store.ErrNotFound
	}
	st := sn.hubChannel(before)
	if st.Broken {
		return store.Hub{}, errHubBroken
	}
	// The name as the guild list had it at this read, before any rename.
	oldName := st.Name
	channelName, err := validChannelName(in.ChannelName)
	if err != nil {
		return store.Hub{}, err
	}
	guild, err := s.readGuild()
	if err != nil {
		return store.Hub{}, err
	}
	hub := before
	if err := applyEdit(&hub, in, guild); err != nil {
		return store.Hub{}, err
	}
	d := diffHubs(&before, &hub)
	if channelName != oldName {
		reason := "Panel: hub channel renamed by " + by.String()
		if _, err := s.deps.Manager.ChannelEdit(hub.HubChannelID, &discordgo.ChannelEdit{Name: channelName}, reason); err != nil {
			utils.Warn("Panel hub channel rename refused", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID, "error", err)
			return store.Hub{}, &fieldError{fieldChannelName, "Discord did not rename the channel: " + commands.DiscordErrorDetail(err) + "."}
		}
		d[fieldChannelName] = change{Before: oldName, After: channelName}
	}

	stored, err := s.deps.Store.UpsertHub(ctx, hub)
	if err != nil {
		if channelName != oldName {
			// The channel is renamed and the row is not. The error names
			// both so the mismatch is traceable from Sentry.
			return store.Hub{}, fmt.Errorf("write hub after renaming its channel from %q to %q: %w", oldName, channelName, err)
		}
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	s.deps.Runtime.ApplyHub(stored)
	if err := s.appendChange(ctx, stored.ID, store.ChangeUpdate, d, by); err != nil {
		return store.Hub{}, err
	}
	return stored, nil
}

// remove deletes a hub's row, drops it from the runtime, so a join to its
// channel spawns nothing more, and appends a change log entry carrying
// every field with a null after. No Discord call: the hub channel stays, so
// a removal is undone by registering the channel again. Spawned channels of
// the hub keep their rows and die when empty, which the runtime does on its
// own. store.ErrNotFound means no hub has the ID.
//
// The entry references no hub: the row is gone, and the store clears the
// hub's earlier entries to match, so the whole log of a removed hub lists
// under no hub.
func (s *hubService) remove(ctx context.Context, hubID int64, by actor) (store.Hub, error) {
	hub, err := s.deps.Store.GetHub(ctx, hubID)
	if err != nil {
		return store.Hub{}, err
	}
	if err := s.deps.Store.DeleteHub(ctx, hub.ID); err != nil {
		return store.Hub{}, fmt.Errorf("delete hub: %w", err)
	}
	s.deps.Runtime.RemoveHub(hub.HubChannelID)
	if err := s.appendChange(ctx, 0, store.ChangeRemove, diffHubs(&hub, nil), by); err != nil {
		return store.Hub{}, err
	}
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
		return &fieldError{fieldPermissionSource, "Choose where spawned channels take their permissions from."}
	}
	// hub still carries the stored set here: what an unavailable role may
	// be kept from.
	roles, err := validRoleSet(in.ModeratorRoleIDs, guild, hub.ModeratorRoleIDs)
	if err != nil {
		return err
	}
	hub.ModeratorRoleIDs = roles
	var ok bool
	if hub.UserLimit, ok = intInRange(in.UserLimit, userLimitMin, userLimitMax); !ok {
		return &fieldError{fieldUserLimit,
			fmt.Sprintf("Enter a user limit of %d to %d. 0 means no limit.", userLimitMin, userLimitMax)}
	}
	if hub.Bitrate, ok = intInRange(in.Bitrate, bitrateMin, guild.bitrateMax); !ok {
		return &fieldError{fieldBitrate,
			fmt.Sprintf("Enter a bitrate of %d to %d.", bitrateMin, guild.bitrateMax)}
	}
	hub.Enabled = in.Enabled
	return nil
}

// validBaseString trims a posted base string and checks its length. A
// refusal is a *fieldError naming the field.
func validBaseString(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(base); n < baseStringMin || n > baseStringMax {
		return "", &fieldError{fieldBaseString,
			fmt.Sprintf("Enter a base string of %d to %d characters.", baseStringMin, baseStringMax)}
	}
	return base, nil
}

// validChannelName trims a posted hub channel name and checks its length
// against Discord's limit. A refusal is a *fieldError naming the field.
func validChannelName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < channelNameMin || n > channelNameMax {
		return "", &fieldError{fieldChannelName,
			fmt.Sprintf("Enter a channel name of %d to %d characters.", channelNameMin, channelNameMax)}
	}
	return name, nil
}

// intInRange parses a posted number and reports whether it lies in [lo, hi].
func intInRange(raw string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return n, err == nil && n >= lo && n <= hi
}

// asFieldError reports whether an error is a validation refusal.
func asFieldError(err error) (*fieldError, bool) {
	var fe *fieldError
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}
