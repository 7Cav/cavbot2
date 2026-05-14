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
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
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
	defer utils.RecoverPanic("milpac-bg")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	milpac, err := utils.GetMilpacByDiscordID(ctx, user.ID)
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
		return
	}
	joinDate, err := time.Parse("2006-01-02", milpac.JoinDate)
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to parse join date: %v", err))
		return
	}
	formatJoinDate := joinDate.Format("02Jan2006")
	capitalizedJoinDate := strings.ToUpper(formatJoinDate)
	var promotionDate time.Time
	if milpac.PromotionDate != "" {
		var err error
		promotionDate, err = time.Parse("2006-01-02", milpac.PromotionDate)
		if err != nil || promotionDate.IsZero() {
			utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to parse promotion date: %v", err))
			return
		}
	} else {
		promotionDate = joinDate
	}
	formatPromotionDate := promotionDate.Format("02Jan2006")
	capitalizedPromotionDate := strings.ToUpper(formatPromotionDate)

	timeInGrade := utils.FormatTimeSinceDuration(promotionDate)
	var Assignments []map[string]interface{}
	utils.Debug("📝 Checking assignments", "username", milpac.User.Username)
	for _, record := range milpac.Records {
		utils.Debug("📋 Processing record", "type", record.RecordType, "date", record.RecordDate)
		if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" ||
			record.RecordType == "RECORD_TYPE_ELOA" || record.RecordType == "RECORD_TYPE_DISCHARGE" {
			utils.Debug("📋 Processing record", "type", record.RecordType, "date", record.RecordDate)
			recordDate, err := time.Parse("2006-01-02", record.RecordDate)
			if err != nil {
				utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
				return
			}
			if strings.Contains(record.RecordDetails, "Retired") || strings.Contains(record.RecordDetails, "ELOA") ||
				strings.Contains(record.RecordDetails, "Discharge") || strings.Contains(record.RecordDetails, "Returned") ||
				strings.Contains(record.RecordDetails, "Enlisted") || strings.Contains(record.RecordDetails, "Reinstated") {
				event := map[string]interface{}{
					"record_date":    recordDate,
					"record_type":    determineEnlistmentRecordType(record.RecordDetails),
					"record_details": record.RecordDetails,
				}
				Assignments = append(Assignments, event)
				utils.Debug("✍️ Added assignment record", "type", event["record_type"], "date", recordDate)
			}
		}
	}
	sort.Slice(Assignments, func(i, j int) bool {
		return Assignments[i]["record_date"].(time.Time).Before(Assignments[j]["record_date"].(time.Time))
	})
	utils.Debug("📊 Sorted assignments", "count", len(Assignments))
	totalTimeInService := calculateTotalService(Assignments)
	timeInService := utils.FormatTimeSinceDuration(time.Now().Add(-totalTimeInService))

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
				Name:  "Gamertag",
				Value: milpac.Gamertag,
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
				Value: fmt.Sprintf("Initial Enlist Date: %s\nTime Spent Active: %s", capitalizedJoinDate, timeInService),
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
				Value: fmt.Sprintf("%s\nTime Spent Active: %s", capitalizedJoinDate, timeInService),
			},
		}
	}
	matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(milpac.UniformUrl)
	if len(matches) < 2 {
		utils.HandleError(utils.NewSessionResponder(s), i, "❌ Failed to parse uniform URL")
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
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response with embed: %v", err))
	}
	utils.Info("✨ Done!", "command", "Milpac")
}

func determineEnlistmentRecordType(details string) string {
	if strings.Contains(details, "Retired") || strings.Contains(details, "Placed on ELOA") || strings.Contains(details, "Discharge") {
		return "leave"
	} else {
		return "join"
	}
}

func calculateTotalService(assignments []map[string]interface{}) time.Duration {
	var totalTime time.Duration
	var currentPeriodStart time.Time

	utils.Debug("🕒 Starting total service calculation", "assignments_count", len(assignments))

	for i, assignment := range assignments {
		recordDate := assignment["record_date"].(time.Time)
		recordType := assignment["record_type"].(string)
		details := assignment["record_details"].(string)

		utils.Debug("📊 Processing assignment record",
			"index", i,
			"date", recordDate.Format("2006-01-02"),
			"type", recordType,
			"details", details)

		if recordType == "join" {
			if currentPeriodStart.IsZero() {
				currentPeriodStart = recordDate
				utils.Debug("✅ Started new service period",
					"start_date", currentPeriodStart.Format("2006-01-02"))
			} else {
				utils.Debug("🔄 Found 2 consecutive joins, continuing service period")
			}
		} else if recordType == "leave" && !currentPeriodStart.IsZero() {
			periodDuration := recordDate.Sub(currentPeriodStart)
			totalTime += periodDuration

			utils.Debug("⏸️ Ended service period",
				"start_date", currentPeriodStart.Format("2006-01-02"),
				"end_date", recordDate.Format("2006-01-02"),
				"period_duration", periodDuration.String(),
				"running_total", totalTime.String())

			currentPeriodStart = time.Time{} // Reset start time
		}
	}

	if !currentPeriodStart.IsZero() {
		currentPeriod := time.Since(currentPeriodStart)
		totalTime += currentPeriod

		utils.Debug("🏃 Adding current active period",
			"start_date", currentPeriodStart.Format("2006-01-02"),
			"duration_so_far", currentPeriod.String(),
			"final_total", totalTime.String())
	} else {
		utils.Debug("✋ No active service period",
			"final_total", totalTime.String())
	}

	return totalTime
}
