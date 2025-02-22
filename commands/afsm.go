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

type AFSMMember struct {
	Username  string
	MilpacUrl string
	TimeSince string
	Date      time.Time
}

func AFSM() Command {
	departments := []string{"S1", "S2", "S3", "S5", "S6", "S7", "WAG", "RTC", "RRD", "MP", "ODS", "NCOA"}

	choices := make([]*discordgo.ApplicationCommandOptionChoice, len(departments))
	for i, dept := range departments {
		choices[i] = &discordgo.ApplicationCommandOptionChoice{
			Name:  dept,
			Value: dept,
		}
		utils.Debug("📋 Added department choice", "department", dept, "index", i)
	}

	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "afsm",
			Description: "Return any users eligible for AFSM for the chosen department",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "department",
					Description: "Department to check for AFSM eligibility",
					Required:    true,
					Choices:     choices,
				},
			},
		},
		Handler: handleAFSMCommand,
	}
}

func handleAFSMCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🎯 AFSM Check requested", "command", "AFSM", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	choice := i.ApplicationCommandData().Options[0].StringValue()
	utils.Debug("🔍 Processing department choice", "department", choice)

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AFSM data for %s...", choice),
		},
	})
	if err != nil {
		utils.Error("❌ Interaction response failed", "error", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	utils.Debug("📊 Fetching roster data", "department", choice)
	Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, choice)
	if err != nil {
		utils.Error("❌ Roster fetch failed", "error", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch Members: %v", err))
		return
	}
	utils.Info("📋 Retrieved roster", "member_count", len(Members.LiteProfiles))

	eligibleMembers := []AFSMMember{}
	currentDate := time.Now()
	utils.Debug("⏰ Current date set", "date", currentDate)

	for _, member := range Members.LiteProfiles {
		utils.Debug("👤 Processing member", "username", member.User.Username)
		eligible := false

		for _, secondary := range member.Secondary {
			utils.Debug("🔍 Checking secondary position", "position", secondary.PositionTitle, "department", choice)
			if strings.Contains(secondary.PositionTitle, choice) {
				eligible = true
				utils.Debug("✅ Member eligible through secondary", "position", secondary.PositionTitle)
			}
			if strings.Contains(member.Primary.PositionTitle, choice) {
				eligible = false
				utils.Debug("❌ Member ineligible due to primary position", "position", member.Primary.PositionTitle)
			}
		}

		if eligible {
			utils.Info("🎯 Found eligible member", "username", member.User.Username)
			fullProfile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
			if err != nil {
				utils.Error("❌ Milpac fetch failed", "error", err, "username", member.User.Username)
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
				return
			}

			utils.Debug("📝 Checking assignments", "username", member.User.Username)
			var Assignments []map[string]interface{}

			for _, record := range fullProfile.Records {
				if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" ||
					record.RecordType == "RECORD_TYPE_ELOA" || record.RecordType == "RECORD_TYPE_DISCHARGE" {
					utils.Debug("📋 Processing record", "type", record.RecordType, "date", record.RecordDate)
					recordDate, err := time.Parse("2006-01-02", record.RecordDate)
					if err != nil {
						utils.Error("❌ Record date parse failed", "error", err, "date", record.RecordDate)
						utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
						return
					}
					if strings.Contains(record.RecordDetails, choice) || strings.Contains(record.RecordDetails, "ELOA") ||
						strings.Contains(record.RecordDetails, "Discharge") || strings.Contains(record.RecordDetails, "Retired") {
						event := map[string]interface{}{
							"record_date":    recordDate,
							"record_type":    determineRecordType(record.RecordDetails),
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

			var startDate time.Time
			for _, assignment := range Assignments {
				if assignment["record_type"].(string) == "join" {
					if startDate.IsZero() {
						startDate = assignment["record_date"].(time.Time)
						utils.Debug("📅 Set start date", "date", startDate)
					}
				} else {
					startDate = time.Time{}
					utils.Debug("🔄 Reset start date due to interruption")
				}
			}

			if !startDate.IsZero() && startDate.Before(currentDate.AddDate(-1, 0, 0)) {
				utils.Debug("✅ Member meets time requirement, checking awards", "username", member.User.Username)

				var latestAward time.Time
				utils.Debug("🏅 Checking awards", "username", member.User.Username)
				for _, award := range fullProfile.Awards {
					if (award.AwardName == "Armed Forces Service Medal") && strings.Contains(award.AwardDetails, choice) {
						utils.Debug("🎖️ Found AFSM award", "date", award.AwardDate, "details", award.AwardDetails)
						awardDate, err := time.Parse("2006-01-02", award.AwardDate)
						if err != nil {
							utils.Error("❌ Award date parse failed", "error", err, "date", award.AwardDate)
							utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse award date: %v", err))
							return
						}
						if latestAward.IsZero() || awardDate.After(latestAward) {
							latestAward = awardDate
							utils.Debug("📅 Updated latest award date", "date", latestAward)
						}
					}
				}

				if latestAward.IsZero() || latestAward.Before(currentDate.AddDate(-1, 0, 0)) {
					utils.Info("✨ Member eligible", "username", member.User.Username, "start_date", startDate)
					matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
					if len(matches) < 2 {
						utils.Error("❌ Uniform URL parse failed", "url", member.UniformUrl)
						utils.HandleError(s, i, "❌ Failed to parse uniform URL")
						return
					}
					eligibleMembers = append(eligibleMembers, AFSMMember{
						Username:  member.User.Username,
						MilpacUrl: fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
						TimeSince: func() string {
							if latestAward.IsZero() {
								return utils.FormatTimeSinceDuration(startDate)
							}
							return utils.FormatTimeSinceDuration(latestAward)
						}(),
						Date: func() time.Time {
							if latestAward.IsZero() {
								return startDate
							}
							return latestAward
						}(),
					})
				} else {
					utils.Debug("⏳ Member not yet eligible since last award", "username", member.User.Username, "last_award", latestAward)
				}
			} else {
				utils.Debug("⏳ Member has not served long enough", "username", member.User.Username)
			}
		}
	}

	utils.Debug("📊 Sorting eligible members", "count", len(eligibleMembers))
	sort.Slice(eligibleMembers, func(i, j int) bool {
		return eligibleMembers[i].Date.Before(eligibleMembers[j].Date)
	})

	AFSMUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		utils.Debug("📝 Formatting output for user", "username", user.Username, "time_since", user.TimeSince)
		AFSMUserOutput = append(AFSMUserOutput, fmt.Sprintf("[%s](<%s>) (%s)", user.Username, user.MilpacUrl, user.TimeSince))
	}

	var response string
	if len(AFSMUserOutput) > 0 {
		response = fmt.Sprintf("⚠️ This command cannot be made completely accurate. Please check the output carefully.\nThe following %s members are eligible for AFSM:\n%s", choice, strings.Join(AFSMUserOutput, "\n"))
		utils.Info("✅ Found eligible members", "department", choice, "count", len(AFSMUserOutput))
	} else {
		response = fmt.Sprintf("No %s members found eligible for AFSM", choice)
		utils.Info("📭 No eligible members found", "department", choice)
	}

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.Error("❌ Response edit failed", "error", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Command completed successfully", "command", "AFSM", "department", choice)
}
func determineRecordType(details string) string {
	if strings.Contains(details, "Relieved") || strings.Contains(details, "ELOA") || strings.Contains(details, "Discharge") || strings.Contains(details, "Retired") {
		return "leave"
	}
	return "join"
}
