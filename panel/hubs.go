package panel

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	fieldHubChannel = "hub_channel"
	fieldBaseString = "base_string"
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

// hubPage is what the hub page renders from. Form and Error are set when a
// register was refused and the page re-renders with the field named.
type hubPage struct {
	Hubs   []hubRow
	Picker []pickerChannel
	Form   registerInput
	Error  *fieldError
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

// snapshot is what both service functions start from: the guild's hub rows
// and its channel list, each read once.
type snapshot struct {
	hubs  []store.Hub
	guild guildChannels
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

// list merges the store's hub rows with the guild's channel list and the
// runtime's live state, and builds the register picker from the voice
// channels that are not hubs.
func (s *hubService) list(ctx context.Context) (hubPage, error) {
	sn, err := s.read(ctx)
	if err != nil {
		return hubPage{}, err
	}

	page := hubPage{Hubs: make([]hubRow, 0, len(sn.hubs))}
	for _, h := range sn.hubs {
		row := hubRow{ID: h.ID, BaseString: h.BaseString, Enabled: h.Enabled, Spawned: s.deps.Runtime.SpawnedCount(h.ID)}
		if ch, ok := sn.guild.channel(h.HubChannelID); ok {
			row.ChannelName = ch.Name
			row.CategoryName = sn.guild.categoryName(ch)
		}
		page.Hubs = append(page.Hubs, row)
	}
	// The store promises no order. Base string, then ID, so two hubs with one
	// base string keep their places between loads.
	sort.Slice(page.Hubs, func(i, j int) bool {
		a, b := strings.ToLower(page.Hubs[i].BaseString), strings.ToLower(page.Hubs[j].BaseString)
		if a != b {
			return a < b
		}
		return page.Hubs[i].ID < page.Hubs[j].ID
	})

	for _, ch := range sn.guild.voiceChannels() {
		if _, taken := sn.hubOn(ch.ID); taken {
			continue
		}
		page.Picker = append(page.Picker, pickerChannel{ID: ch.ID, Name: ch.Name, CategoryName: sn.guild.categoryName(ch)})
	}
	sort.Slice(page.Picker, func(i, j int) bool {
		a, b := page.Picker[i], page.Picker[j]
		if a.CategoryName != b.CategoryName {
			return a.CategoryName < b.CategoryName
		}
		return a.Name < b.Name
	})
	return page, nil
}

// register makes an existing voice channel a hub with the defaults, writes
// the row and applies it to the runtime, so a join spawns from it at once
// with no restart. A refusal is a *fieldError naming the field; the store
// is not written and the runtime is not touched.
func (s *hubService) register(ctx context.Context, in registerInput) (store.Hub, error) {
	in.ChannelID = strings.TrimSpace(in.ChannelID)
	in.BaseString = strings.TrimSpace(in.BaseString)
	if n := utf8.RuneCountInString(in.BaseString); n < baseStringMin || n > baseStringMax {
		return store.Hub{}, &fieldError{fieldBaseString,
			fmt.Sprintf("Enter a base string of %d to %d characters.", baseStringMin, baseStringMax)}
	}
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
	return stored, nil
}

// asFieldError reports whether an error is a validation refusal.
func asFieldError(err error) (*fieldError, bool) {
	var fe *fieldError
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}
