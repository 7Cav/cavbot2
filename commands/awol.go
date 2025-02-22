package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"sort"
	"strings"
	"time"
)

func stringPtr(s string) *string {
	return &s
}

const (
	maxEmbedLength    = 6000
	maxFieldsPerEmbed = 25
	maxEmbedsPerMsg   = 10
	awolThresholdDays = 8
)

type AwolUser struct {
	Username          string
	MilpacUrl         string
	TimeSinceLastPost string
	LastPostDate      time.Time
}

func Awol() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "awol",
			Description: "Return the AWOL users for a position.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "position",
					Description: "Position to check for awols. (EX: 2/B/1-7 | Reserve | S1)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "force_file_output",
					Description: "Forces the output to be a file instead of an embed. This is useful for large AWOL lists.",
					Required:    false,
				},
			},
		},
		Handler: handleAwolCommand,
	}
}

func handleAwolCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting AWOL check", "command", "Awol", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AWOL data for %s...", i.ApplicationCommandData().Options[0].StringValue()),
		},
	})
	forceFile := false
	if len(i.ApplicationCommandData().Options) > 1 {
		forceFile = i.ApplicationCommandData().Options[1].BoolValue()
	}
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	position := i.ApplicationCommandData().Options[0].StringValue()
	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	if len(roster.LiteProfiles) == 0 {
		utils.HandleError(s, i, fmt.Sprintf("❌ The search for \"%s\" returned no troopers. Please check your search for accuracy.", position))
		return
	}

	awolUsers := []AwolUser{}
	for _, member := range roster.LiteProfiles {
		if member.User.Username == "Tester.B" || strings.Contains(member.Rank.RankFull, "General") {
			continue
		}
		lastPostDate, err := time.Parse("2006-01-02 15:04:05", member.LastForumPostDate)
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse last forum post date: %v", err))
			return
		}
		if lastPostDate.Before(time.Now().AddDate(0, 0, -awolThresholdDays)) {
			matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
			if len(matches) < 2 {
				utils.HandleError(s, i, "❌ Failed to parse uniform URL")
				return
			}
			awolUsers = append(awolUsers, AwolUser{
				Username:          member.User.Username,
				MilpacUrl:         fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
				TimeSinceLastPost: utils.FormatTimeSinceDuration(lastPostDate),
				LastPostDate:      lastPostDate,
			})
		}
	}

	sort.Slice(awolUsers, func(i, j int) bool {
		return awolUsers[i].LastPostDate.Before(awolUsers[j].LastPostDate)
	})

	if len(awolUsers) == 0 {
		response := fmt.Sprintf("Search completed successfully: no users matching \"%s\" are AWOL.", position)
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		})
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		}
		return
	}

	var chunks []string
	currentChunk := ""
	for _, user := range awolUsers {
		userLine := fmt.Sprintf("[%s](%s) (%s)\n",
			user.Username,
			user.MilpacUrl,
			user.TimeSinceLastPost)

		if len(currentChunk)+len(userLine) > 4096 {
			chunks = append(chunks, currentChunk)
			currentChunk = userLine
		} else {
			currentChunk += userLine
		}
	}
	if currentChunk != "" {
		chunks = append(chunks, currentChunk)
	}
	utils.Info("Debug chunks info", "chunks_length", len(chunks), "max_embeds", maxEmbedsPerMsg)
	if len(chunks) > maxEmbedsPerMsg {
		utils.Info("⚠️ Too many AWOL users for embeds, falling back to file upload", "count", len(awolUsers))
		sendAwolFile(s, i, awolUsers, position, forceFile)
		utils.Info("✨ Done!", "command", "Awol")
		return
	} else if forceFile {
		utils.Info("⚠️ Force file output enabled, falling back to embeds", "count", len(awolUsers))
		sendAwolFile(s, i, awolUsers, position, forceFile)
		utils.Info("✨ Done!", "command", "Awol")
		return
	}

	var embeds []*discordgo.MessageEmbed
	for i, chunk := range chunks {
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("AWOL Users for %s (Page %d/%d)", position, i+1, len(chunks)),
			Description: chunk,
			Color:       0xfbcc29,
			Footer: &discordgo.MessageEmbedFooter{
				Text: fmt.Sprintf("Total AWOL: %d", len(awolUsers)),
			},
			Timestamp: time.Now().Format(time.RFC3339),
		}
		embeds = append(embeds, embed)
	}

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: nil,
		Embeds:  &embeds,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "Awol")
}

func sendAwolFile(s *discordgo.Session, i *discordgo.InteractionCreate, awolUsers []AwolUser, position string, forceFile bool) {
	var content strings.Builder
	content.WriteString(fmt.Sprintf("AWOL Report for %s\nGenerated: %s\n\n",
		position,
		time.Now().Format("2006-01-02 15:04:05")))

	for _, user := range awolUsers {
		content.WriteString(fmt.Sprintf("%s - %s\nMilpac: %s\n\n",
			user.Username,
			user.TimeSinceLastPost,
			user.MilpacUrl))
	}

	file := &discordgo.File{
		Name:        fmt.Sprintf("awol_report_%s.txt", strings.ReplaceAll(position, "/", "-")),
		ContentType: "text/plain",
		Reader:      strings.NewReader(content.String()),
	}
	if forceFile {
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: stringPtr(fmt.Sprintf("Force File Set True, AWOL report for %s generated as file:", position)),
			Files:   []*discordgo.File{file},
		})
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to send file: %v", err))
			return
		}
	} else {
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: stringPtr(fmt.Sprintf("Large AWOL report for %s generated as file:", position)),
			Files:   []*discordgo.File{file},
		})
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to send file: %v", err))
			return
		}
	}

}
