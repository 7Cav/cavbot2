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

func AFSM() Command {
	departments := []string{"S1", "S2", "S3", "S5", "S6", "S7", "WAG", "NCOA", "RTC", "RRD", "MP", "ODS"}

	choices := make([]*discordgo.ApplicationCommandOptionChoice, len(departments))
	for i, dept := range departments {
		choices[i] = &discordgo.ApplicationCommandOptionChoice{
			Name:  dept,
			Value: dept,
		}
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
	utils.Info("AFSM Check requested", "command", "AFSM", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	choice := i.ApplicationCommandData().Options[0].StringValue()
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AFSM data for %s...", choice),
		},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, choice)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}

	eligibleMembers := []AFSMMember{}
	currentDate := time.Now()

	for _, member := range Members.LiteProfiles {
		eligible := false
		for _, secondary := range member.Secondary {
			if strings.Contains(secondary.PositionTitle, choice) {
				eligible = true
			}
			if strings.Contains(member.Primary.PositionTitle, choice) {
				eligible = false
			}
		}
		if eligible {
			fullProfile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
			if err != nil {
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
				return
			}

			var latestAward time.Time
			for _, award := range fullProfile.Awards {
				if (award.AwardName == "Armed Forces Service Medal") && strings.Contains(award.AwardDetails, choice) {
					awardDate, err := time.Parse("2006-01-02", award.AwardDate)
					if err != nil {
						utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse award date: %v", err))
						return
					}
					if latestAward.IsZero() || awardDate.After(latestAward) {
						latestAward = awardDate
					}
				}
			}

			if latestAward.IsZero() {
				var Assignments []map[string]interface{}

				for _, record := range fullProfile.Records {
					if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" || record.RecordType == "RECORD_TYPE_ELOA" || record.RecordType == "RECORD_TYPE_DISCHARGE" {
						recordDate, err := time.Parse("2006-01-02", record.RecordDate)
						if err != nil {
							utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
							return
						}
						if strings.Contains(record.RecordDetails, choice) {
							event := map[string]interface{}{
								"record_date":    recordDate,
								"record_type":    determineRecordType(record.RecordDetails),
								"record_details": record.RecordDetails,
							}
							Assignments = append(Assignments, event)
						}
					}
				}

				sort.Slice(Assignments, func(i, j int) bool {
					return Assignments[i]["record_date"].(time.Time).Before(Assignments[j]["record_date"].(time.Time))
				})

				var startDate time.Time
				for _, assignment := range Assignments {
					if assignment["record_type"].(string) == "join" {
						if startDate.IsZero() {
							startDate = assignment["record_date"].(time.Time)
						}
					} else {
						startDate = time.Time{}
					}
				}

				if !startDate.IsZero() {
					if startDate.Before(currentDate.AddDate(-1, 0, 0)) {
						matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
						if len(matches) < 2 {
							utils.HandleError(s, i, "❌ Failed to parse uniform URL")
							return
						}
						eligibleMembers = append(eligibleMembers, AFSMMember{
							Username:  member.User.Username,
							MilpacUrl: fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
							TimeSince: utils.FormatTimeSinceDuration(startDate),
							Date:      startDate,
						})
					}
				}
			} else {
				if latestAward.Before(currentDate.AddDate(-1, 0, 0)) {
					matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
					if len(matches) < 2 {
						utils.HandleError(s, i, "❌ Failed to parse uniform URL")
						return
					}
					eligibleMembers = append(eligibleMembers, AFSMMember{
						Username:  member.User.Username,
						MilpacUrl: fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
						TimeSince: utils.FormatTimeSinceDuration(latestAward),
						Date:      latestAward,
					})
				}
			}
		}
	}
	sort.Slice(eligibleMembers, func(i, j int) bool {
		return eligibleMembers[i].Date.Before(eligibleMembers[j].Date)
	})
	AFSMUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		AFSMUserOutput = append(AFSMUserOutput, fmt.Sprintf("[%s](<%s>) (%s)", user.Username, user.MilpacUrl, user.TimeSince))
	}
	var response string
	if len(AFSMUserOutput) > 0 {
		response = fmt.Sprintf("The following %s members are eligible for AFSM:\n%s", choice, strings.Join(AFSMUserOutput, "\n"))
	} else {
		response = fmt.Sprintf("No %s members found eligible for AFSM", choice)
	}
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "AFSM")
}
