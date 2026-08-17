package commands

import (
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// helplineIntro is wording approved by GenStaff and is reproduced verbatim. It
// deliberately promises nothing on the regiment's behalf — the card points at
// outside organisations, and the invoker is free to add their own words in a
// follow-up message.
const helplineIntro = "This is a list of helpline links that may be useful to you. These are professionals who will be best able to assist you."

// helplineResource is one organisation on the card. Contact is separate from
// URL because a phone number is the route that matters in an acute moment: a
// member should not have to open a browser first.
type helplineResource struct {
	Name         string
	URL          string
	Blurb        string
	Contact      string // dial/text routes, omitted where the service has none
	AccessRoutes string // language and accessibility alternatives
}

// helplineSection groups resources by how urgently they answer. Crisis lines
// come first so nobody scans past them to reach long-term programs.
type helplineSection struct {
	Header    string
	Resources []helplineResource
}

// helplineSections is the card's content. Contact details here are an external
// contract in the same way the PAF field labels are: if a crisis line changes
// its routing, this card is silently wrong and nothing in CI will notice.
// Treat a change to any number as a correctness change, not a copy edit.
var helplineSections = []helplineSection{
	{
		Header: "Immediate help",
		Resources: []helplineResource{
			{
				Name:         "988 Suicide & Crisis Lifeline",
				URL:          "https://www.988lifeline.org/",
				Blurb:        "Available for everyone, free, and confidential.",
				Contact:      "Call or text **988** · [Chat online](https://chat.988lifeline.org/)",
				AccessRoutes: "Español: press 2 or text AYUDA · Deaf/HoH: dial 711 then 988",
			},
			{
				Name:    "Veterans Crisis Line",
				URL:     "https://www.veteranscrisisline.net/",
				Blurb:   "For Veterans and their loved ones. You don't have to be enrolled in VA benefits or health care to connect.",
				Contact: "Dial **988 then press 1** · Text **838255**",
			},
		},
	},
	{
		Header: "Ongoing support",
		Resources: []helplineResource{
			{
				Name:  "Stack Up",
				URL:   "https://www.stackup.org/",
				Blurb: "Mental health support and suicide prevention for US and Allied veterans, through gaming and geek culture.",
			},
			{
				Name:  "Wounded Warrior Project",
				URL:   "https://www.woundedwarriorproject.org/",
				Blurb: "Mental health services for veterans and their families.",
			},
		},
	},
	{
		Header: "Outside the US",
		Resources: []helplineResource{
			{
				Name:  "International hotlines",
				URL:   "https://blog.opencounseling.com/suicide-hotlines/",
				Blurb: "Crisis lines by country.",
			},
		},
	},
}

// buildHelplineCard renders the sections into a single embed description.
// Headers and masked links both render inside a description, which embed fields
// cannot do — fields have no section-header concept.
func buildHelplineCard() *discordgo.MessageEmbed {
	var sb strings.Builder
	for _, section := range helplineSections {
		fmt.Fprintf(&sb, "## %s\n", section.Header)
		for _, resource := range section.Resources {
			fmt.Fprintf(&sb, "**[%s](%s)**\n%s\n", resource.Name, resource.URL, resource.Blurb)
			if resource.Contact != "" {
				fmt.Fprintf(&sb, "%s\n", resource.Contact)
			}
			if resource.AccessRoutes != "" {
				fmt.Fprintf(&sb, "*%s*\n", resource.AccessRoutes)
			}
			sb.WriteString("\n")
		}
	}

	return &discordgo.MessageEmbed{
		Title:       "Helplines & Support",
		Description: strings.TrimRight(sb.String(), "\n"),
		Color:       0xfbcc29, // Cav Yellow
		Footer: &discordgo.MessageEmbedFooter{
			Text: "These organizations are independent of 7Cav.",
		},
	}
}

func Helpline() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name: "helpline",
			// This description sits in every member's command picker, so it is
			// plain and searchable rather than euphemistic; the card itself is
			// where the explicit language belongs.
			Description: "Share mental health and crisis support resources",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Who to address this to",
					Required:    false,
				},
			},
			// No DefaultMemberPermissions: access is a Discord server setting,
			// so restricting the command never needs a PR and a deploy.
		},
		Handler: handleHelplineCommand,
	}
}

func handleHelplineCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runHelpline(utils.NewSessionResponder(s), i)
}

func runHelpline(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)

	options := i.ApplicationCommandData().Options
	optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt
	}

	// The mention has to live in the message content: a mention rendered inside
	// an embed displays as a chip but never notifies.
	content := helplineIntro
	var targetID string
	if opt, ok := optionMap["user"]; ok {
		targetID = opt.UserValue(nil).ID
		content = fmt.Sprintf("<@%s> %s", targetID, helplineIntro)
	}

	// target_id is logged so that use of the addressed form as a jab can be
	// found after the fact. It stays in the log line only — never a metric
	// label (ADR 0011).
	utils.Info("🚀 Starting Helpline", "command", "Helpline", "username", username,
		"discord_id", discordID, "target_id", targetID)

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Embeds:  []*discordgo.MessageEmbed{buildHelplineCard()},
		},
	})
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "Helpline")
}
