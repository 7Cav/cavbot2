package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func Zulu() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "zulu",
			Description: "Returns the current Zulu time",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "time",
					Description: "Zulu time (HHMM, e.g. 2300)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "date",
					Description: "Zulu date (DDMMMYY, e.g. 01MAY26)",
					Required:    false,
				},
			},
		},
		Handler: handleZuluCommand,
	}
}

func handleZuluCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runZulu(utils.NewSessionResponder(s), time.Now(), i)
}

// resolveZuluInstant turns the supplied Zulu time (and optional Zulu date) into
// an absolute instant. With no date the time is taken as the next occurrence:
// today if it is still ahead of now, otherwise tomorrow. An explicit date is
// used verbatim, so a past date stays in the past.
func resolveZuluInstant(now time.Time, timeStr, dateStr string) (time.Time, error) {
	if dateStr != "" {
		return utils.ParseZuluDateTime(dateStr, timeStr)
	}

	today := now.UTC().Format("02Jan06")
	instant, err := utils.ParseZuluDateTime(today, timeStr)
	if err != nil {
		return time.Time{}, err
	}
	if !instant.After(now) {
		instant = instant.AddDate(0, 0, 1)
	}
	return instant, nil
}

func runZulu(r utils.InteractionResponder, now time.Time, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("Zulu time requested", "command", "Zulu", "username", username, "discord_id", discordID)

	options := i.ApplicationCommandData().Options
	optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt
	}

	var timeStr, dateStr string
	if opt, ok := optionMap["time"]; ok {
		timeStr = opt.StringValue()
	}
	if opt, ok := optionMap["date"]; ok {
		dateStr = opt.StringValue()
	}

	if timeStr == "" && dateStr != "" {
		utils.HandleError(r, i, "❌ A date needs a time; add time (HHMM, e.g. 2300)")
		return
	}

	content := fmt.Sprintf("The current Zulu time is: %s",
		strings.ToUpper(now.UTC().Format("15:04:05 02Jan06")))

	if timeStr != "" {
		instant, err := resolveZuluInstant(now, timeStr, dateStr)
		if err != nil {
			utils.HandleError(r, i, "❌ Invalid time; must be HHMM in Zulu (e.g. 2300 or 2300z)")
			return
		}
		// The absolute token carries a date because it renders in each viewer's
		// own zone: 2300Z is the same day in CDT but the next day in AEST, so a
		// time-only render would tell half the regiment the wrong day.
		content = fmt.Sprintf("%s is <t:%d:f> local (<t:%d:R>)",
			strings.ToUpper(instant.UTC().Format("1504Z 02Jan06")),
			instant.Unix(), instant.Unix())
	}

	if err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "Zulu")
}
