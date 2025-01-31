package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"sort"
	"strings"
	"time"
)

func S6Afsm() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "s6-afsm",
			Description: "Return S6 Members eligible for AFSM",
		},
		Handler: handleS6AFSMCommand,
	}
}

func S6ITCheck() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "s6-it-check",
			Description: "Return S6 IT Members eligible for full status",
		},
		Handler: handleS6ITCheckCommand,
	}
}

func handleS6AFSMCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fmt.Println("🚀 Starting S6 AFSM check")

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Fetching S6 AFSM data...",
		},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s6Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}

	eligibleMembers := make([]string, 0)
	currentDate := time.Now()

	for _, member := range s6Members.LiteProfiles {

		fullProfile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
			return
		}

		var latestAward time.Time
		for _, award := range fullProfile.Awards {
			if (award.AwardName == "Armed Forces Service Medal") && strings.Contains(award.AwardDetails, "S6") {
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
			var s6Assignments []map[string]interface{}

			for _, record := range fullProfile.Records {
				if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
					recordDate, err := time.Parse("2006-01-02", record.RecordDate)
					if err != nil {
						utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
						return
					}
					if strings.Contains(record.RecordDetails, "S6") {
						event := map[string]interface{}{
							"recordDate":     recordDate,
							"record_type":    determineRecordType(record.RecordDetails),
							"record_details": record.RecordDetails,
						}
						s6Assignments = append(s6Assignments, event)
					}
				}
			}

			sort.Slice(s6Assignments, func(i, j int) bool {
				return s6Assignments[i]["record_date"].(time.Time).Before(s6Assignments[j]["record_date"].(time.Time))
			})

			var startDate time.Time
			for _, assignment := range s6Assignments {
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
					eligibleMembers = append(eligibleMembers, member.User.Username)
				}
			}
		} else {
			if latestAward.Before(currentDate.AddDate(-1, 0, 0)) {
				eligibleMembers = append(eligibleMembers, member.User.Username)
			}
		}
	}

	response := fmt.Sprintf("The following S6 members are eligible for AFSM:\n%s", strings.Join(eligibleMembers, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	fmt.Println("✨ Done!")
}

func handleS6ITCheckCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	fmt.Println("🚀 Starting S6 IT Check")

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Fetching S6 IT Check data...",
		},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s6Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}

	eligibleMembers := make([]string, 0)
	currentDate := time.Now()
	var errs []error

	for _, member := range s6Members.LiteProfiles {
		fullProfile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to fetch milpac for %s: %v", member.User.Username, err))
			continue
		}

		if strings.Contains(fullProfile.Primary.PositionTitle, "IT") && strings.Contains(fullProfile.Primary.PositionTitle, "S6") {
			position, timeInPosition, positionDate, err := determineITPositionTime(fullProfile.Primary.PositionTitle, fullProfile)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to determine time in position: %v", err))
				continue
			}
			if positionDate.Before(currentDate.AddDate(0, -6, 0)) {
				eligibleMembers = append(eligibleMembers, fmt.Sprintf("%s (%s) - %s", member.User.Username, position, timeInPosition))
			}
		}

		for _, secondary := range fullProfile.Secondary {
			if strings.Contains(secondary.PositionTitle, "IT") && strings.Contains(secondary.PositionTitle, "S6") {
				position, timeInPosition, positionDate, err := determineITPositionTime(secondary.PositionTitle, fullProfile)
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to determine time in position: %v", err))
					continue
				}
				if positionDate.Before(currentDate.AddDate(0, -6, 0)) {
					eligibleMembers = append(eligibleMembers, fmt.Sprintf("%s (%s) - %s", member.User.Username, position, timeInPosition))
				}
			}
		}
	}
	response := fmt.Sprintf("The following S6 members are eligible for Full Status:\n%s", strings.Join(eligibleMembers, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	fmt.Println("✨ Done!")
}

func determineRecordType(details string) string {
	if strings.Contains(details, "Relieved") {
		return "leave"
	}
	return "join"
}

func determineITPositionTime(positionName string, member *utils.ProfileResponse) (position string, timeInPosition string, positionDate time.Time, err error) {
	var date time.Time

	for _, record := range member.Records {
		if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
			recordDate, err := time.Parse("2006-01-02", record.RecordDate)
			if err != nil {
				return "", "", time.Time{}, fmt.Errorf("failed to parse record date: %v", err)
			}

			if strings.Contains(record.RecordDetails, positionName) {

				if date.IsZero() {
					date = recordDate
				} else if recordDate.After(date) {
					date = recordDate
				}
			}
		}
	}
	positionDate = date
	timeInPosition = utils.FormatTimeSinceDuration(date)
	return positionName, timeInPosition, positionDate, nil
}
