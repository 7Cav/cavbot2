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

type ITMember struct {
	Username     string
	MilpacUrl    string
	Position     string
	TimeSince    string
	PositionDate time.Time
}

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
	utils.Info("S6 AFSM Check requested", "command", "S6AFSM", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)

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

	eligibleMembers := []AFSMMember{}
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
							"record_date":    recordDate,
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
	sort.Slice(eligibleMembers, func(i, j int) bool {
		return eligibleMembers[i].Date.Before(eligibleMembers[j].Date)
	})
	AFSMUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		AFSMUserOutput = append(AFSMUserOutput, fmt.Sprintf("[%s](<%s>) (%s)", user.Username, user.MilpacUrl, user.TimeSince))
	}
	var response string
	if len(AFSMUserOutput) > 0 {
		response = fmt.Sprintf("The following S6 members are eligible for AFSM:\n%s", strings.Join(AFSMUserOutput, "\n"))
	} else {
		response = fmt.Sprintf("No S6 members found eligible for AFSM")
	}
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "S6AFSM")
}

func handleS6ITCheckCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting S6 IT Check", "command", "S6ITCheck", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)

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

	eligibleMembers := []ITMember{}
	currentDate := time.Now()
	var matches []string
	for _, member := range s6Members.LiteProfiles {
		fullProfile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
			return
		}

		if strings.Contains(fullProfile.Primary.PositionTitle, "IT") && strings.Contains(fullProfile.Primary.PositionTitle, "S6") {
			position, timeInPosition, positionDate, err := determineITPositionTime(fullProfile.Primary.PositionTitle, fullProfile)
			if err != nil {
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to determine time in position: %v", err))
				return
			}

			if positionDate.Before(currentDate.AddDate(0, -6, 0)) {
				matches = regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
				if len(matches) < 2 {
					utils.HandleError(s, i, "❌ Failed to parse uniform URL")
					return
				}
				eligibleMembers = append(eligibleMembers, ITMember{
					Username:     member.User.Username,
					MilpacUrl:    fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
					Position:     position,
					TimeSince:    timeInPosition,
					PositionDate: positionDate,
				})
			}
		}

		for _, secondary := range fullProfile.Secondary {
			if strings.Contains(secondary.PositionTitle, "IT") && strings.Contains(secondary.PositionTitle, "S6") {
				position, timeInPosition, positionDate, err := determineITPositionTime(secondary.PositionTitle, fullProfile)
				if err != nil {
					utils.HandleError(s, i, fmt.Sprintf("❌ Failed to determine time in position: %v", err))
					return
				}
				if positionDate.Before(currentDate.AddDate(0, -6, 0)) {
					matches = regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
					if len(matches) < 2 {
						utils.HandleError(s, i, "❌ Failed to parse uniform URL")
						return
					}
					eligibleMembers = append(eligibleMembers, ITMember{
						Username:     member.User.Username,
						MilpacUrl:    fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
						Position:     position,
						TimeSince:    timeInPosition,
						PositionDate: positionDate,
					})
				}
			}
		}
	}
	sort.Slice(eligibleMembers, func(i, j int) bool {
		return eligibleMembers[i].PositionDate.Before(eligibleMembers[j].PositionDate)
	})
	ITUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		ITUserOutput = append(ITUserOutput, fmt.Sprintf("[%s](<%s>) (%s) - %s", user.Username, user.MilpacUrl, user.Position, user.TimeSince))
	}
	response := fmt.Sprintf("The following S6 members are eligible for Full Status:\n%s", strings.Join(ITUserOutput, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "S6ITCheck")
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
