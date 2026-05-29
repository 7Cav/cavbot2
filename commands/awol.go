package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
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

// loaCacheReader is the minimal LOA-cache surface /awol (and /loa) consume.
// Production wires *utils.LOACache (GlobalLOACache); tests substitute a fake
// with canned entries so the handler stays deterministic without touching the
// process-global singleton.
type loaCacheReader interface {
	IsOnLOA(username string) bool
	GetEntry(username string) (utils.LOAEntry, bool)
}

type AwolUser struct {
	Username          string
	MilpacUrl         string
	TimeSinceLastPost string
	LastPostDate      time.Time
	OnLOA             bool
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
					Description: "Position to check for awols. (EX: 2/B/1-7 | Reservist | S1)",
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
	runAwol(utils.NewSessionResponder(s), utils.GlobalLOACache, time.Now(), i)
}

func runAwol(r utils.InteractionResponder, cache loaCacheReader, now time.Time, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting AWOL check", "command", "Awol", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()
	forceFile := false
	if len(i.ApplicationCommandData().Options) > 1 {
		forceFile = i.ApplicationCommandData().Options[1].BoolValue()
	}

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AWOL data for %s...", position),
		},
	})
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	if len(roster.LiteProfiles) == 0 {
		utils.HandleError(r, i, emptyRosterSearchMessage(position))
		return
	}

	awolThreshold := now.AddDate(0, 0, -awolThresholdDays)
	awolUsers := []AwolUser{}
	for _, member := range roster.LiteProfiles {
		if member.User.Username == "Tester.B" || strings.Contains(member.Rank.RankFull, "General") {
			continue
		}
		lastPostDate, err := time.Parse("2006-01-02 15:04:05", member.LastForumPostDate)
		if err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to parse last forum post date: %v", err))
			return
		}
		if lastPostDate.Before(awolThreshold) {
			matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
			if len(matches) < 2 {
				utils.HandleError(r, i, "❌ Failed to parse uniform URL")
				return
			}
			awolUsers = append(awolUsers, AwolUser{
				Username:          member.User.Username,
				MilpacUrl:         fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
				TimeSinceLastPost: utils.FormatTimeSinceDuration(lastPostDate),
				LastPostDate:      lastPostDate,
				OnLOA:             cache.IsOnLOA(member.User.Username),
			})
		}
	}

	sort.Slice(awolUsers, func(i, j int) bool {
		return awolUsers[i].LastPostDate.Before(awolUsers[j].LastPostDate)
	})

	if len(awolUsers) == 0 {
		response := fmt.Sprintf("Search completed successfully: no users matching \"%s\" are AWOL.", position)
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		}); err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		}
		return
	}

	loaCount := 0
	for _, u := range awolUsers {
		if u.OnLOA {
			loaCount++
		}
	}

	var chunks []string
	currentChunk := ""
	for _, user := range awolUsers {
		loaTag := ""
		if user.OnLOA {
			// NOTE: #96 — when the LOA cache is empty/unhealthy, every
			// IsOnLOA returns false and this branch silently never fires,
			// so the "On LOA" column reports incorrect (all-clear) status.
			// Tracked separately; not in scope for #112.
			if entry, ok := cache.GetEntry(user.Username); ok && entry.ThreadID != 0 {
				loaTag = fmt.Sprintf("**[[LOA]](https://7cav.us/threads/%d/)** ", entry.ThreadID)
			} else {
				loaTag = "**[LOA]** "
			}
		}
		userLine := fmt.Sprintf("%s[%s](%s) (%s)\n",
			loaTag,
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
		sendAwolFile(r, i, awolUsers, position, forceFile, now)
		utils.Info("✨ Done!", "command", "Awol")
		return
	} else if forceFile {
		utils.Info("⚠️ Force file output enabled, falling back to embeds", "count", len(awolUsers))
		sendAwolFile(r, i, awolUsers, position, forceFile, now)
		utils.Info("✨ Done!", "command", "Awol")
		return
	}

	var embeds []*discordgo.MessageEmbed
	for idx, chunk := range chunks {
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("AWOL Users for %s (Page %d/%d)", position, idx+1, len(chunks)),
			Description: chunk,
			Color:       0xfbcc29,
			Footer: &discordgo.MessageEmbedFooter{
				Text: fmt.Sprintf("Total AWOL: %d (%d on LOA)", len(awolUsers), loaCount),
			},
			Timestamp: now.Format(time.RFC3339),
		}
		embeds = append(embeds, embed)
	}

	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: nil,
		Embeds:  &embeds,
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "Awol")
}

func sendAwolFile(r utils.InteractionResponder, i *discordgo.InteractionCreate, awolUsers []AwolUser, position string, forceFile bool, now time.Time) {
	var content strings.Builder
	_, _ = fmt.Fprintf(&content, "AWOL Report for %s\nGenerated: %s\n\n",
		position,
		now.Format("2006-01-02 15:04:05"))

	for _, user := range awolUsers {
		loaTag := ""
		if user.OnLOA {
			loaTag = " [LOA]"
		}
		_, _ = fmt.Fprintf(&content, "%s%s - %s\nMilpac: %s\n\n",
			user.Username,
			loaTag,
			user.TimeSinceLastPost,
			user.MilpacUrl)
	}

	file := &discordgo.File{
		Name:        fmt.Sprintf("awol_report_%s.txt", strings.ReplaceAll(position, "/", "-")),
		ContentType: "text/plain",
		Reader:      strings.NewReader(content.String()),
	}
	var prefix string
	if forceFile {
		prefix = fmt.Sprintf("Force File Set True, AWOL report for %s generated as file:", position)
	} else {
		prefix = fmt.Sprintf("Large AWOL report for %s generated as file:", position)
	}
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(prefix),
		Files:   []*discordgo.File{file},
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to send file: %v", err))
	}
}
