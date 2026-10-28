package commands

import (
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

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
