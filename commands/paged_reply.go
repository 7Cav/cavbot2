package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// pagedList is a list a command sends as embeds over its reply and public
// follow-ups, as /loa and /awol send theirs.
type pagedList struct {
	// command names the command in what reaches Sentry.
	command  string
	position string
	lines    []pagedLine
	// title titles page idx of total pages.
	title  func(idx, total int) string
	footer string
}

// sendPagedList lays list out as pages and messages Discord accepts, edits
// the reply with the first message, clearing the reply's text, and sends
// each later message as a public follow-up. A message Discord refuses
// reaches Sentry and ends the list there. The page numbers on the messages
// already sent show the member it stopped short. It reports whether every
// message went out.
func sendPagedList(r utils.InteractionResponder, i *discordgo.InteractionCreate, now time.Time, list pagedList) bool {
	// No layout has more pages than lines, so a title numbered with the
	// line count is as long as any page's title can be.
	pageOverhead := len(list.title(len(list.lines)-1, len(list.lines))) + len(list.footer)
	pagesByMessage := pagedMessages(list.lines, pageOverhead)
	total := 0
	for _, pages := range pagesByMessage {
		total += len(pages)
	}

	var embedsByMessage [][]*discordgo.MessageEmbed
	idx := 0
	for _, pages := range pagesByMessage {
		var embeds []*discordgo.MessageEmbed
		for _, page := range pages {
			embeds = append(embeds, &discordgo.MessageEmbed{
				Title:       list.title(idx, total),
				Description: page,
				Color:       0xfbcc29,
				Footer:      &discordgo.MessageEmbedFooter{Text: list.footer},
				Timestamp:   now.Format(time.RFC3339),
			})
			idx++
		}
		embedsByMessage = append(embedsByMessage, embeds)
	}

	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(""),
		Embeds:  &embedsByMessage[0],
	}); err != nil {
		captureDeferredEditFailure(i, list.command, err)
		return false
	}
	for _, embeds := range embedsByMessage[1:] {
		if err := r.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Embeds: embeds}); err != nil {
			captureError(fmt.Sprintf("Failed to deliver a /%s follow-up", list.command), err,
				"command", list.command, "guild_id", i.GuildID, "position", list.position)
			return false
		}
	}
	return true
}

// pagedLine is one line of a paged list and the heading it runs under.
type pagedLine struct {
	heading string
	text    string
}

// pagedMessages lays lines out, in order and each whole, as pages, and the
// pages as messages Discord accepts. A page is one embed's description, of
// at most discordEmbedDescriptionLimit bytes. A message holds at most
// maxEmbedsPerMsg pages, whose descriptions, with pageOverhead bytes of
// title and footer for each, come to at most discordMessageEmbedTextLimit
// bytes. Bytes never undercount the characters Discord counts.
//
// Each section opens with its heading, after a blank line when another
// section came before it on the page. A page that continues a section
// opens with that section's heading too, so each page reads on its own.
func pagedMessages(lines []pagedLine, pageOverhead int) [][]string {
	layout := pageLayout{pageOverhead: pageOverhead}
	for _, l := range lines {
		layout.add(l)
	}
	return layout.finish()
}

// pageLayout holds pagedMessages' work in progress. It keeps the messages
// it has filled and the message and page it is filling.
type pageLayout struct {
	pageOverhead int
	messages     [][]string
	// pages are the open message's full pages, and used the bytes they
	// take of it.
	pages []string
	used  int
	page  strings.Builder
	// heading is the one the open page's last line runs under, or "" while
	// the open page is empty.
	heading string
}

// add puts l on the open page, or on a new page when it doesn't fit, which
// starts a new message when the page doesn't fit the open one.
func (lay *pageLayout) add(l pagedLine) {
	text := pageText(lay.heading, l)
	if lay.page.Len() > 0 && !lay.fits(len(text)) {
		lay.closePage()
		text = pageText(lay.heading, l)
	}
	if lay.page.Len() == 0 && len(lay.pages) > 0 && !lay.fits(len(text)) {
		lay.closeMessage()
	}
	lay.page.WriteString(text)
	lay.heading = l.heading
}

// fits reports whether n more bytes fit on the open page within its
// message.
func (lay *pageLayout) fits(n int) bool {
	if lay.page.Len() == 0 && len(lay.pages) == maxEmbedsPerMsg {
		return false
	}
	pageLen := lay.page.Len() + n
	return pageLen <= discordEmbedDescriptionLimit &&
		lay.used+lay.pageOverhead+pageLen <= discordMessageEmbedTextLimit
}

func (lay *pageLayout) closePage() {
	lay.pages = append(lay.pages, lay.page.String())
	lay.used += lay.pageOverhead + lay.page.Len()
	lay.page.Reset()
	lay.heading = ""
}

func (lay *pageLayout) closeMessage() {
	lay.messages = append(lay.messages, lay.pages)
	lay.pages, lay.used = nil, 0
}

// finish closes the open page and message and returns every message.
func (lay *pageLayout) finish() [][]string {
	if lay.page.Len() > 0 {
		lay.closePage()
	}
	if len(lay.pages) > 0 {
		lay.closeMessage()
	}
	return lay.messages
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
