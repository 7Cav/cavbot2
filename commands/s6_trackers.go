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

func S6_AFSM() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "s6-afsm",
			Description: "Return S6 Members eligible for AFSM",
		},
		Handler: handleS6AFSMCommand,
	}
}

func S6_IT_Check() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "s6-it-check",
			Description: "Return S6 IT Members eligible for full status",
		},
		Handler: handleS6ITCheckCommand,
	}
}

func handleS6AFSMCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	s6_members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}
	eligible_members := make([]string, 0)
	current_date := time.Now()
	for _, member := range s6_members.LiteProfiles {
		full_profile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
			return
		}
		var latest_award time.Time
		for _, award := range full_profile.Awards {
			if (award.AwardName == "Armed Forces Service Medal") && strings.Contains(award.AwardDetails, "S6") {
				award_date, err := time.Parse("2006-01-02", award.AwardDate)
				if err != nil {
					utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse award date: %v", err))
					return
				}
				if latest_award.IsZero() || award_date.After(latest_award) {
					latest_award = award_date
				}
			}
		}
		if latest_award.IsZero() {
			var s6_assignments []map[string]interface{}
			for _, record := range full_profile.Records {
				if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
					record_date, err := time.Parse("2006-01-02", record.RecordDate)
					if err != nil {
						utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
						return
					}
					if strings.Contains(record.RecordDetails, "S6") {
						event := map[string]interface{}{
							"record_date":    record_date,
							"record_type":    determineRecordType(record.RecordDetails),
							"record_details": record.RecordDetails,
						}
						s6_assignments = append(s6_assignments, event)
					}
				}
			}

			sort.Slice(s6_assignments, func(i, j int) bool {
				return s6_assignments[i]["record_date"].(time.Time).Before(s6_assignments[j]["record_date"].(time.Time))
			})
			var start_Date time.Time
			for _, assignment := range s6_assignments {
				if assignment["record_type"].(string) == "join" {
					if start_Date.IsZero() {
						start_Date = assignment["record_date"].(time.Time)
					}
				} else {
					start_Date = time.Time{}
				}

			}
			if !start_Date.IsZero() {
				if start_Date.Before(current_date.AddDate(-1, 0, 0)) {
					eligible_members = append(eligible_members, member.User.Username)
				}
			}
		} else {
			if latest_award.Before(current_date.AddDate(-1, 0, 0)) {
				eligible_members = append(eligible_members, member.User.Username)
			}
		}
	}
	response := fmt.Sprintf("The following S6 members are eligible for AFSM:\n%s", strings.Join(eligible_members, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
}

func handleS6ITCheckCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	s6_members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}
	eligible_members := make([]string, 0)
	current_date := time.Now()
	it_members := make(map[string]*utils.ProfileResponse)
	var errs []error
	for _, member := range s6_members.LiteProfiles {
		full_profile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to fetch milpac for %s: %v", member.User.Username, err))
			continue
		}

		if regexp.MustCompile(`\bIT\b`).MatchString(full_profile.Primary.PositionTitle) {
			it_members[member.User.Username] = full_profile
			continue
		}

		for _, secondary := range full_profile.Secondary {
			if regexp.MustCompile(`\bIT\b`).MatchString(secondary.PositionTitle) {
				it_members[member.User.Username] = full_profile
				break
			}
		}
	}
	var s6_assignments []map[string]interface{}
	for _, member := range it_members {
		for _, record := range member.Records {
			if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
				record_date, err := time.Parse("2006-01-02", record.RecordDate)
				if err != nil {
					utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
					return
				}
				if strings.Contains(record.RecordDetails, "S6") {
					event := map[string]interface{}{
						"record_date":    record_date,
						"record_type":    determineRecordType(record.RecordDetails),
						"record_details": record.RecordDetails,
					}
					s6_assignments = append(s6_assignments, event)
				}
			}
		}

		sort.Slice(s6_assignments, func(i, j int) bool {
			return s6_assignments[i]["record_date"].(time.Time).Before(s6_assignments[j]["record_date"].(time.Time))
		})
		var start_Date time.Time
		for _, assignment := range s6_assignments {
			if assignment["record_type"].(string) == "join" {
				if start_Date.IsZero() {
					start_Date = assignment["record_date"].(time.Time)
				}
			} else {
				start_Date = time.Time{}
			}

		}
		if !start_Date.IsZero() {
			if start_Date.Before(current_date.AddDate(0, -6, 0)) {
				eligible_members = append(eligible_members, member.User.Username)
			}
		}

	}
	response := fmt.Sprintf("The following S6 IT members are eligible for full status:\n%s", strings.Join(eligible_members, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
}

func determineRecordType(details string) string {
	if strings.Contains(details, "Relieved") {
		return "leave"
	}
	return "join"
}
