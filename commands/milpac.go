package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"strings"
	"time"
)

type CustomTime struct {
	time.Time
}

func (ct *CustomTime) UnmarshalJSON(b []byte) error {
	t, err := time.Parse("2006-01-02", strings.Trim(string(b), "\""))
	if err != nil {
		return err
	}
	ct.Time = t
	return nil
}

func Milpac() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "milpac",
			Description: "Return the user's Milpac",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Username to use for Milpac.",
					Required:    true,
				},
			},
		},
		Handler: handleMilpacCommand,
	}
}

func handleMilpacCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Fetching Milpac data...",
		},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	options := i.ApplicationCommandData().Options
	optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt
	}

	utils.Info("Milpac requested", "command", "Milpac", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	user := optionMap["user"].UserValue(s)

	go processMilpacRequest(s, i, user)
}

func processMilpacRequest(s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	milpac, err := utils.GetMilpacByDiscordID(ctx, user.ID)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
		return
	}
	joinDate, err := time.Parse("2006-01-02", milpac.JoinDate)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse join date: %v", err))
		return
	}
	formatJoinDate := joinDate.Format("02Jan2006")
	capitalizedJoinDate := strings.ToUpper(formatJoinDate)
	var promotionDate time.Time
	if milpac.PromotionDate != "" {
		var err error
		promotionDate, err = time.Parse("2006-01-02", milpac.PromotionDate)
		if err != nil || promotionDate.IsZero() {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse promotion date: %v", err))
			return
		}
	} else {
		promotionDate = joinDate
	}
	formatPromotionDate := promotionDate.Format("02Jan2006")
	capitalizedPromotionDate := strings.ToUpper(formatPromotionDate)
	timeInService := utils.FormatTimeSinceDuration(joinDate)
	timeInGrade := utils.FormatTimeSinceDuration(promotionDate)

	secondaryPositions := make([]string, 0)
	for _, secondary := range milpac.Secondary {
		secondaryPositions = append(secondaryPositions, secondary.PositionTitle)
	}
	var fields []*discordgo.MessageEmbedField
	if len(secondaryPositions) > 0 {
		fields = []*discordgo.MessageEmbedField{
			{
				Name:  "Username",
				Value: milpac.User.Username,
			},
			{
				Name:  "Roster",
				Value: milpac.GetRosterStatus(),
			},
			{
				Name:  "Primary Position",
				Value: milpac.Primary.PositionTitle,
			},
			{Name: "Secondary Positions", Value: strings.Join(secondaryPositions, "\n")},
			{
				Name:  "Rank",
				Value: fmt.Sprintf("%s (%s)\nPromoted: %s\nTime Since Promotion: %s", milpac.Rank.RankFull, milpac.Rank.RankShort, capitalizedPromotionDate, timeInGrade),
			},
			{
				Name:  "Time in Service",
				Value: fmt.Sprintf("%s\nTime Since Enlistment: %s", capitalizedJoinDate, timeInService),
			},
		}
	} else {
		fields = []*discordgo.MessageEmbedField{
			{
				Name:  "Username",
				Value: milpac.User.Username,
			},
			{
				Name:  "Roster",
				Value: milpac.GetRosterStatus(),
			},
			{
				Name:  "Primary Position",
				Value: milpac.Primary.PositionTitle,
			},
			{
				Name:  "Rank",
				Value: fmt.Sprintf("%s (%s)\nPromoted: %s\nTime Since Promotion: %s", milpac.Rank.RankFull, milpac.Rank.RankShort, capitalizedPromotionDate, timeInGrade),
			},
			{
				Name:  "Time in Service",
				Value: fmt.Sprintf("%s\nTime Since Enlistment: %s", capitalizedJoinDate, timeInService),
			},
		}
	}
	matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(milpac.UniformUrl)
	if len(matches) < 2 {
		utils.HandleError(s, i, "❌ Failed to parse uniform URL")
		return
	}
	id := matches[1]
	milpacUrl := fmt.Sprintf("https://7cav.us/rosters/profile/%s", id)

	embed := &discordgo.MessageEmbed{
		Title:  fmt.Sprintf("%v %v", milpac.Rank.RankFull, milpac.RealName),
		URL:    milpacUrl,
		Fields: fields,
		Color:  0xfbcc29, // Cav Yellow
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: milpac.Rank.RankImageUrl,
		},
		Image: &discordgo.MessageEmbedImage{
			URL: milpac.UniformUrl,
		},
	}
	utils.Info("Returning Milpac", "command", "Milpac", "milpac_name", embed.Title, "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	emptyContent := ""
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &emptyContent,
		Embeds:  &[]*discordgo.MessageEmbed{embed},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response with embed: %v", err))
	}
	utils.Info("✨ Done!", "command", "Milpac")
}
