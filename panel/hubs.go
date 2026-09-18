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
// and category names read from the guild at this page load, and the live
// spawned count from the runtime.
type hubRow struct {
	ID         int64
	BaseString string
	// ChannelName is empty when the hub channel is not in the guild.
	ChannelName string
	// CategoryName is empty when the hub channel has no parent.
	CategoryName string
	Enabled      bool
	Spawned      int
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

// editInput is the edit form as posted: strings as the browser sent them,
// parsed and validated by the service, and rendered back on a refusal so
// nothing typed is lost. The hub channel is not a field: it is fixed at
// register.
type editInput struct {
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

// fieldError is a validation refusal: which field, and why. The field name
// is the form field's name and the data-error attribute on the note the
// page shows, a test contract; the message is not.
type fieldError struct {
	Field   string
	Message string
}

func (e *fieldError) Error() string { return e.Field + ": " + e.Message }

// Form field names, as posted and as named in a refusal.
const (
	fieldHubChannel       = "hub_channel"
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

// Defaults a register writes for the fields the form does not carry.
const (
	defaultUserLimit = 0
	defaultBitrate   = 64000
)

// Bounds on the user limit and the bitrate. The user limit is Discord's
// range, 0 meaning no limit. The bitrate bounds are Discord's hard floor and
// the ceiling a guild at the top boost tier gets; the ceiling the guild's own
// tier allows is read live at save by the next ticket.
const (
	userLimitMin = 0
	userLimitMax = 99
	bitrateMin   = 8000
	bitrateMax   = 384000
)

// hubPage is what the hub page renders from. One form shows at a time: the
// register form, or, with Edit set, one hub's edit form. Error names the
// refused field of whichever form shows; Form is the register form as
// posted, so nothing typed is lost on a refusal.
type hubPage struct {
	Hubs   []hubRow
	Picker []pickerChannel
	Form   registerInput
	Error  *fieldError
	Edit   *editPage
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
	// Edit is the edit form as posted back after a refusal. Nil shows the
	// stored values.
	Edit  *editInput
	Error *fieldError
}

// editPage is one hub's edit form: the fixed values the form shows read-only,
// the guild's roles the moderator picker offers, and the form's fields as
// stored or as posted back after a refusal.
type editPage struct {
	ID int64
	// ChannelName is empty when the hub channel is not in the guild.
	ChannelName string
	// CategoryName is empty when the hub channel has no parent.
	CategoryName string
	Roles        []guildRole
	Form         editInput
	// Changes are the hub's last entries, newest first.
	Changes []changeView
}

// guildRole is one role the moderator picker offers, and whether the form
// has it checked.
type guildRole struct {
	ID      string
	Name    string
	Checked bool
}

// editInputOf is the edit form as the stored hub fills it.
func editInputOf(h store.Hub) editInput {
	return editInput{
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

// guildRoles reads the guild's roles through the manager seam: the ones the
// moderator picker offers and an update accepts. The @everyone role, whose
// ID is the guild's, is left out; every member holds it.
func (s *hubService) guildRoles() ([]*discordgo.Role, error) {
	g, err := s.deps.Manager.Guild(s.deps.GuildID)
	if err != nil {
		return nil, fmt.Errorf("guild roles: %w", err)
	}
	roles := make([]*discordgo.Role, 0, len(g.Roles))
	for _, r := range g.Roles {
		if r.ID != s.deps.GuildID {
			roles = append(roles, r)
		}
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Position > roles[j].Position })
	return roles, nil
}

// page reads everything the hub page renders from, once: the hub rows and
// the guild's channel list for the list and the picker, and, when an edit
// form shows, the guild's roles and the hub's last entries. store.ErrNotFound
// means no hub has the requested ID.
func (s *hubService) page(ctx context.Context, req pageRequest) (hubPage, error) {
	sn, err := s.read(ctx)
	if err != nil {
		return hubPage{}, err
	}
	page := hubPage{Hubs: s.rows(sn), Picker: picker(sn), Form: req.Register, Error: req.Error}
	if req.HubID != 0 {
		if page.Edit, err = s.editForm(ctx, sn, req.HubID, req.Edit); err != nil {
			return hubPage{}, err
		}
	}
	return page, nil
}

// rows merges the store's hub rows with the guild's channel list and the
// runtime's live state.
func (s *hubService) rows(sn snapshot) []hubRow {
	rows := make([]hubRow, 0, len(sn.hubs))
	for _, h := range sn.hubs {
		row := hubRow{ID: h.ID, BaseString: h.BaseString, Enabled: h.Enabled, Spawned: s.deps.Runtime.SpawnedCount(h.ID)}
		if ch, ok := sn.guild.channel(h.HubChannelID); ok {
			row.ChannelName = ch.Name
			row.CategoryName = sn.guild.categoryName(ch)
		}
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

// editForm builds one hub's edit form from the snapshot: the stored values,
// or the form as posted when a save was refused, the guild's roles for the
// moderator picker, and the hub's last entries.
func (s *hubService) editForm(ctx context.Context, sn snapshot, hubID int64, posted *editInput) (*editPage, error) {
	hub, ok := sn.hubByID(hubID)
	if !ok {
		return nil, store.ErrNotFound
	}
	roles, err := s.guildRoles()
	if err != nil {
		return nil, err
	}
	entries, err := s.deps.Store.ListChangeLog(ctx, hub.ID, changeLogLimit)
	if err != nil {
		return nil, fmt.Errorf("list change log: %w", err)
	}
	form := editInputOf(hub)
	if posted != nil {
		form = *posted
	}
	page := &editPage{ID: hub.ID, Form: form, Roles: make([]guildRole, 0, len(roles))}
	if ch, ok := sn.guild.channel(hub.HubChannelID); ok {
		page.ChannelName = ch.Name
		page.CategoryName = sn.guild.categoryName(ch)
	}
	roleNames := make(map[string]string, len(roles))
	for _, r := range roles {
		roleNames[r.ID] = r.Name
		page.Roles = append(page.Roles, guildRole{ID: r.ID, Name: r.Name, Checked: slices.Contains(form.ModeratorRoleIDs, r.ID)})
	}
	page.Changes = changeViews(entries, roleNames)
	return page, nil
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

	stored, err := s.deps.Store.UpsertHub(ctx, store.Hub{
		GuildID:          s.deps.GuildID,
		HubChannelID:     in.ChannelID,
		BaseString:       in.BaseString,
		PermissionSource: store.PermissionCategory,
		ModeratorRoleIDs: []string{},
		UserLimit:        defaultUserLimit,
		Bitrate:          defaultBitrate,
		Enabled:          true,
	})
	if err != nil {
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	s.deps.Runtime.ApplyHub(stored)
	if err := s.appendChange(ctx, stored.ID, store.ChangeRegister, diffHubs(nil, &stored), by); err != nil {
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
// The order is row, runtime, entry. The runtime apply cannot fail, so once
// the row is written the runtime matches the store; an entry the store
// refuses is an error the handler reports, with the save already made.
func (s *hubService) update(ctx context.Context, hubID int64, in editInput, by actor) (store.Hub, error) {
	before, err := s.deps.Store.GetHub(ctx, hubID)
	if err != nil {
		return store.Hub{}, err
	}
	roles, err := s.guildRoles()
	if err != nil {
		return store.Hub{}, err
	}
	hub := before
	if err := applyEdit(&hub, in, roles); err != nil {
		return store.Hub{}, err
	}

	stored, err := s.deps.Store.UpsertHub(ctx, hub)
	if err != nil {
		return store.Hub{}, fmt.Errorf("write hub: %w", err)
	}
	s.deps.Runtime.ApplyHub(stored)
	if err := s.appendChange(ctx, stored.ID, store.ChangeUpdate, diffHubs(&before, &stored), by); err != nil {
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

// applyEdit validates the edit form and puts its values on the hub. The
// first refusal wins, as a *fieldError naming the field; the hub is then
// half written and must not be stored.
func applyEdit(hub *store.Hub, in editInput, roles []*discordgo.Role) error {
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
	known := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		known[r.ID] = struct{}{}
	}
	// A set: a role posted twice is stored once.
	hub.ModeratorRoleIDs = make([]string, 0, len(in.ModeratorRoleIDs))
	for _, id := range in.ModeratorRoleIDs {
		if _, ok := known[id]; !ok {
			return &fieldError{fieldModeratorRoles, "One of those roles is no longer in the server. Choose again."}
		}
		if !slices.Contains(hub.ModeratorRoleIDs, id) {
			hub.ModeratorRoleIDs = append(hub.ModeratorRoleIDs, id)
		}
	}
	var ok bool
	if hub.UserLimit, ok = intInRange(in.UserLimit, userLimitMin, userLimitMax); !ok {
		return &fieldError{fieldUserLimit,
			fmt.Sprintf("Enter a user limit of %d to %d. 0 means no limit.", userLimitMin, userLimitMax)}
	}
	if hub.Bitrate, ok = intInRange(in.Bitrate, bitrateMin, bitrateMax); !ok {
		return &fieldError{fieldBitrate,
			fmt.Sprintf("Enter a bitrate of %d to %d.", bitrateMin, bitrateMax)}
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
