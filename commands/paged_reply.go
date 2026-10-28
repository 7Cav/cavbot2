package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// pagedListUnsavedLine opens the reply of a long list whose pages couldn't
// be saved, which shows page 1 alone.
const pagedListUnsavedLine = "⚠️ The other pages couldn't be kept. Run the command again to see them."

// pageMarkerFormat ends the title of each page of a list of more than one,
// with the page's number and the page count in place of the %ds.
const pageMarkerFormat = "(Page %d/%d)"

// pagedList is a list a command sends as pages of one embed each, as /loa
// and /awol send theirs (ADR 0020).
type pagedList struct {
	// command names the command in its buttons' CustomIDs and in what
	// reaches Sentry.
	command  string
	position string
	lines    []pagedLine
	// title titles page idx of total pages.
	title  func(idx, total int) string
	footer string
}

// pagedListStoreTimeout bounds the save of a run's pages.
const pagedListStoreTimeout = 5 * time.Second

// sendPagedList lays list out as pages of one embed each and edits the
// reply to show the first, clearing the reply's text. A list of one page
// shows it alone. A longer list's pages are saved in st, under the member
// who ran the command, before the reply shows page 1 with ◀ and ▶, which
// read the rest back from st (runPagedListPress). When the pages can't be
// saved, page 1 shows with both arrows disabled and pagedListUnsavedLine,
// and a failed save goes to Sentry. It reports whether the reply went out.
func sendPagedList(r utils.InteractionResponder, st store.Store, i *discordgo.InteractionCreate, now time.Time, list pagedList) bool {
	// No layout has more pages than lines, so a title numbered with the
	// line count is as long as any page's title can be.
	pageOverhead := len(list.title(len(list.lines)-1, len(list.lines))) + len(list.footer)
	descriptions := pagedDescriptions(list.lines, pageOverhead)
	pages := make([]store.ListPage, len(descriptions))
	for idx, desc := range descriptions {
		pages[idx] = store.ListPage{Title: list.title(idx, len(descriptions)), Description: desc, Footer: list.footer}
	}

	edit := &discordgo.WebhookEdit{
		Content: stringPtr(""),
		Embeds:  &[]*discordgo.MessageEmbed{pageEmbed(pages[0], now)},
	}
	if len(pages) > 1 {
		_, discordID := interactionUsernameAndID(i)
		key, saved := savePagedList(st, i, list, store.PagedList{RunnerID: discordID, RanAt: now, Pages: pages})
		if saved {
			if err := checkPageArrowCustomIDs(list.command, key, len(pages)); err != nil {
				captureError(fmt.Sprintf("Arrows of a long /%s list too long for Discord", list.command), err,
					"command", list.command, "guild_id", i.GuildID, "position", list.position)
				key, saved = "", false
			}
		}
		if !saved {
			edit.Content = stringPtr(pagedListUnsavedLine)
		}
		arrows := pageArrows(list.command, pageArrowsOnList, key, 0, len(pages))
		edit.Components = &arrows
	}
	if err := r.InteractionResponseEdit(i.Interaction, edit); err != nil {
		captureDeferredEditFailure(i, list.command, err)
		return false
	}
	return true
}

// savePagedList saves the pages of a run of list in st and returns their
// key. saved is false with no store, or when the save failed, which goes to
// Sentry.
func savePagedList(st store.Store, i *discordgo.InteractionCreate, list pagedList, pages store.PagedList) (key string, saved bool) {
	if st == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pagedListStoreTimeout)
	defer cancel()
	key, err := st.SavePagedList(ctx, pages)
	if err != nil {
		captureError(fmt.Sprintf("Pages of a long /%s list not saved", list.command), err,
			"command", list.command, "guild_id", i.GuildID, "position", list.position)
		return "", false
	}
	return key, true
}

// checkPageArrowCustomIDs refuses a key whose arrows' CustomIDs would run
// past Discord's cap, since a truncated ID misroutes (ADR 0007). The
// longest is ▶'s on the last page, and a private copy's are as long.
func checkPageArrowCustomIDs(command, key string, total int) error {
	if id := pageArrowCustomID(command, pageArrowsOnList, key, total); len(id) > discordCustomIDLimit {
		return fmt.Errorf("page arrow CustomID %q is %d characters, over Discord's %d", id, len(id), discordCustomIDLimit)
	}
	return nil
}

// pageEmbed is a saved page as its embed shows it, stamped with the time
// of the run it came from.
func pageEmbed(page store.ListPage, ranAt time.Time) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       page.Title,
		Description: page.Description,
		Color:       0xfbcc29,
		Footer:      &discordgo.MessageEmbedFooter{Text: page.Footer},
		Timestamp:   ranAt.Format(time.RFC3339),
	}
}

// The actions of a page arrow's CustomID: pageArrowsOnList on the message
// the command posted, and pageArrowsOnCopy on a private copy of it.
const (
	pageArrowsOnList = "page"
	pageArrowsOnCopy = "copy"
)

// pageArrows is the ◀ ▶ row of page idx of total pages saved under key.
// ◀ is disabled on the first page and ▶ on the last. With no key the pages
// weren't saved, and both are disabled.
func pageArrows(command, action, key string, idx, total int) []discordgo.MessageComponent {
	back := discordgo.Button{Label: "◀", Style: discordgo.SecondaryButton,
		CustomID: pageArrowCustomID(command, action, key, idx-1), Disabled: key == "" || idx == 0}
	next := discordgo.Button{Label: "▶", Style: discordgo.SecondaryButton,
		CustomID: pageArrowCustomID(command, action, key, idx+1), Disabled: key == "" || idx == total-1}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{back, next}}}
}

// pageArrowCustomID is the CustomID of an arrow that shows page idx of the
// list saved under key: <command>::<action>::<key>:<idx> (ADR 0007).
func pageArrowCustomID(command, action, key string, idx int) string {
	return strings.Join([]string{command, action, fmt.Sprintf("%s:%d", key, idx)}, customIDSeparator)
}

// pagedLine is one line of a paged list and the heading it runs under.
type pagedLine struct {
	heading string
	text    string
}

// pagedDescriptions lays lines out, in order and each whole, as pages of
// one embed each. A page is an embed's description, of at most
// discordEmbedDescriptionLimit bytes, which with pageOverhead bytes of
// title and footer come to at most discordMessageEmbedTextLimit bytes.
// Bytes never undercount the characters Discord counts.
//
// Each section opens with its heading, after a blank line when another
// section came before it on the page. A page that continues a section
// opens with that section's heading too, so each page reads on its own.
func pagedDescriptions(lines []pagedLine, pageOverhead int) []string {
	limit := min(discordEmbedDescriptionLimit, discordMessageEmbedTextLimit-pageOverhead)
	var (
		pages []string
		page  strings.Builder
		// heading is the one the open page's last line runs under, or ""
		// while the open page is empty.
		heading string
	)
	for _, l := range lines {
		text := pageText(heading, l)
		if page.Len() > 0 && page.Len()+len(text) > limit {
			pages = append(pages, page.String())
			page.Reset()
			heading = ""
			text = pageText(heading, l)
		}
		page.WriteString(text)
		heading = l.heading
	}
	if page.Len() > 0 {
		pages = append(pages, page.String())
	}
	return pages
}

// pageText is what l adds to a page whose last line ran under heading, or
// to an empty page when heading is "". It puts l's own heading first when
// l opens a section there.
func pageText(heading string, l pagedLine) string {
	switch {
	case heading == "":
		return l.heading + "\n" + l.text + "\n"
	case l.heading != heading:
		return "\n" + l.heading + "\n" + l.text + "\n"
	default:
		return l.text + "\n"
	}
}

// pagedListReadTimeout bounds a press's read of its page. The bot answers
// a press only once it has the page, and Discord drops a press it hasn't
// answered within 3 seconds.
const pagedListReadTimeout = 2 * time.Second

// pagedListRetention is how long a run's saved pages last, the window
// recordings use (ADR 0020).
const pagedListRetention = RecordingRetention

// The private notes a press gets in place of a page. Each is a new message
// only the presser sees, so the message pressed stays as it was.
const (
	// pagedListExpiredNote answers a press on a list whose saved pages are
	// gone or 30 days old, or on a host with no store.
	pagedListExpiredNote = "❌ This list has expired. Run the command again to see it."
	// pagedListUnreadableNote answers a press whose page the store couldn't
	// read.
	pagedListUnreadableNote = "❌ This page couldn't be shown. Try again in a moment."
)

// runPagedListPress answers a press on a list's arrow with the page the
// arrow points to, read from the run's saved pages (ADR 0020).
func runPagedListPress(r utils.InteractionResponder, st store.Store, now time.Time, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID
	username, discordID := interactionUsernameAndID(i)
	command, _, _ := strings.Cut(customID, customIDSeparator)
	action, key, idx, ok := parsePageArrowCustomID(customID)
	utils.Info("🚀 Starting page turn", "command", command, "username", username, "discord_id", discordID, "custom_id", customID)

	if !ok || st == nil {
		answerPress(r, i, command, notePress(pagedListExpiredNote))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pagedListReadTimeout)
	saved, err := st.PagedListPage(ctx, key, idx)
	cancel()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		captureError("Saved list page unreadable", err, "command", command, "custom_id", customID, "guild_id", i.GuildID)
		answerPress(r, i, command, notePress(pagedListUnreadableNote))
		return
	}
	if err != nil || now.Sub(saved.RanAt) >= pagedListRetention {
		answerPress(r, i, command, notePress(pagedListExpiredNote))
		return
	}

	// The runner flips the message the command posted, and anyone else
	// gets a private copy at the page they asked for, which they flip in
	// place.
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{pageEmbed(saved.Page, saved.RanAt)},
			Components: pageArrows(command, action, key, idx, saved.Total),
		},
	}
	if action == pageArrowsOnList && discordID != saved.RunnerID {
		resp.Type = discordgo.InteractionResponseChannelMessageWithSource
		resp.Data.Flags = discordgo.MessageFlagsEphemeral
		resp.Data.Components = pageArrows(command, pageArrowsOnCopy, key, idx, saved.Total)
	}
	answerPress(r, i, command, resp)
	utils.Info("✨ Done!", "command", command)
}

// notePress is a press's answer of note, a new message only the presser
// sees.
func notePress(note string) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: note, Flags: discordgo.MessageFlagsEphemeral},
	}
}

// answerPress sends a press's answer. One Discord refuses goes to Sentry
// under command.
func answerPress(r utils.InteractionResponder, i *discordgo.InteractionCreate, command string, resp *discordgo.InteractionResponse) {
	if err := acknowledge(r, i, resp); err != nil {
		captureAckFailure("Failed to answer a page turn", err,
			"command", command, "custom_id", i.MessageComponentData().CustomID, "guild_id", i.GuildID)
	}
}

// parsePageArrowCustomID reads a page arrow's action, list key and page
// back out of its CustomID. ok is false for any CustomID pageArrowCustomID
// could not have built.
func parsePageArrowCustomID(customID string) (action, key string, idx int, ok bool) {
	parts := strings.Split(customID, customIDSeparator)
	if len(parts) != 3 || (parts[1] != pageArrowsOnList && parts[1] != pageArrowsOnCopy) {
		return "", "", 0, false
	}
	key, page, found := strings.Cut(parts[2], ":")
	if !found {
		return "", "", 0, false
	}
	// A disabled ◀ on page 1 points at page -1, which no press reaches
	// from a Discord client.
	idx, err := strconv.Atoi(page)
	if err != nil || idx < 0 {
		return "", "", 0, false
	}
	return parts[1], key, idx, true
}
