package commands

import (
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

// replyMessages returns the embeds of each message the run sent, in order: the
// reply as its last edit left it, then each follow-up after that edit. It
// fails the test when the run never edited its reply, or made any call but
// a follow-up after the edit.
func replyMessages(t *testing.T, f *fakeResponder) [][]*discordgo.MessageEmbed {
	t.Helper()
	calls := f.Calls()
	last := -1
	for k, c := range calls {
		if c.Method == "Edit" && c.Edit != nil {
			last = k
		}
	}
	if last < 0 {
		t.Fatalf("the run never edited its reply; it last showed %q", lastReply(t, calls))
	}
	var reply []*discordgo.MessageEmbed
	if calls[last].Edit.Embeds != nil {
		reply = *calls[last].Edit.Embeds
	}
	parts := [][]*discordgo.MessageEmbed{reply}
	for _, c := range calls[last+1:] {
		if c.Method != "Followup" || c.Params == nil {
			t.Fatalf("the run's reply edit was followed by a %s call", c.Method)
		}
		parts = append(parts, c.Params.Embeds)
	}
	return parts
}

// assertWithinDiscordLimits fails for each message Discord would refuse
// for its embeds: more than 10 embeds, a description over 4096
// characters, or over 6000 characters of embed text in all. The limits are
// Discord's, at
// https://docs.discord.com/developers/resources/message#embed-object-embed-limits.
func assertWithinDiscordLimits(t *testing.T, parts [][]*discordgo.MessageEmbed) {
	t.Helper()
	for p, embeds := range parts {
		if len(embeds) > 10 {
			t.Errorf("message %d holds %d embeds, over Discord's 10", p+1, len(embeds))
		}
		total := 0
		for e, embed := range embeds {
			if n := utf8.RuneCountInString(embed.Description); n > 4096 {
				t.Errorf("message %d embed %d has a %d-character description, over Discord's 4096", p+1, e+1, n)
			}
			total += embedTextChars(embed)
		}
		if total > 6000 {
			t.Errorf("message %d holds %d characters of embed text, over Discord's 6000", p+1, total)
		}
	}
}

// embedTextChars counts the characters of e that Discord holds to a
// message's 6000: its title, description, footer, author name and fields.
func embedTextChars(e *discordgo.MessageEmbed) int {
	n := utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description)
	if e.Footer != nil {
		n += utf8.RuneCountInString(e.Footer.Text)
	}
	if e.Author != nil {
		n += utf8.RuneCountInString(e.Author.Name)
	}
	for _, field := range e.Fields {
		n += utf8.RuneCountInString(field.Name) + utf8.RuneCountInString(field.Value)
	}
	return n
}
