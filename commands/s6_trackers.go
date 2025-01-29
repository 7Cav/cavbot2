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
	fmt.Println("🚀 Starting S6 AFSM check")

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Fetching S6 AFSM data...",
		},
	})
	if err != nil {
		fmt.Printf("💥 Failed to send initial message: %v\n", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("🔍 Getting S6 roster...")
	s6_members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		fmt.Printf("💥 Couldn't get S6 members: %v\n", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}
	fmt.Printf("✅ Found %d S6 members\n", len(s6_members.LiteProfiles))

	eligible_members := make([]string, 0)
	current_date := time.Now()

	for _, member := range s6_members.LiteProfiles {
		fmt.Printf("👤 Checking %s\n", member.User.Username)

		full_profile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			fmt.Printf("💥 Couldn't get profile for %s: %v\n", member.User.Username, err)
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
			return
		}

		var latest_award time.Time
		fmt.Printf("🎖️ Checking AFSM history for %s\n", member.User.Username)
		for _, award := range full_profile.Awards {
			if (award.AwardName == "Armed Forces Service Medal") && strings.Contains(award.AwardDetails, "S6") {
				award_date, err := time.Parse("2006-01-02", award.AwardDate)
				if err != nil {
					fmt.Printf("💥 Bad date format %s: %v\n", award.AwardDate, err)
					utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse award date: %v", err))
					return
				}
				if latest_award.IsZero() || award_date.After(latest_award) {
					latest_award = award_date
					fmt.Printf("📅 Found AFSM from %s\n", award_date)
				}
			}
		}

		if latest_award.IsZero() {
			fmt.Printf("🔍 No AFSM found, checking service time for %s\n", member.User.Username)
			var s6_assignments []map[string]interface{}

			for _, record := range full_profile.Records {
				if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
					record_date, err := time.Parse("2006-01-02", record.RecordDate)
					if err != nil {
						fmt.Printf("💥 Bad record date %s: %v\n", record.RecordDate, err)
						utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse record date: %v", err))
						return
					}
					if strings.Contains(record.RecordDetails, "S6") {
						fmt.Printf("📝 Found S6 record on %s: %s\n", record_date, record.RecordDetails)
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
						fmt.Printf("📅 S6 start date: %s\n", start_Date)
					}
				} else {
					start_Date = time.Time{}
					fmt.Println("🔄 Service time reset - found transfer/departure")
				}
			}

			if !start_Date.IsZero() {
				if start_Date.Before(current_date.AddDate(-1, 0, 0)) {
					fmt.Printf("✅ %s eligible (served over 1 year)\n", member.User.Username)
					eligible_members = append(eligible_members, member.User.Username)
				}
			}
		} else {
			if latest_award.Before(current_date.AddDate(-1, 0, 0)) {
				fmt.Printf("✅ %s eligible (last AFSM over 1 year ago)\n", member.User.Username)
				eligible_members = append(eligible_members, member.User.Username)
			}
		}
	}

	fmt.Printf("📊 Found %d eligible members\n", len(eligible_members))
	response := fmt.Sprintf("The following S6 members are eligible for AFSM:\n%s", strings.Join(eligible_members, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		fmt.Printf("💥 Failed to send results: %v\n", err)
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
		fmt.Printf("💥 Failed to send initial message: %v\n", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("🔍 Getting S6 roster...")
	s6_members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		fmt.Printf("💥 Couldn't get S6 members: %v\n", err)
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}
	fmt.Printf("✅ Found %d S6 members\n", len(s6_members.LiteProfiles))

	eligible_members := make([]string, 0)
	current_date := time.Now()
	var errs []error

	fmt.Println("🔎 Checking each member for IT positions...")
	for _, member := range s6_members.LiteProfiles {
		fmt.Printf("👤 Checking %s\n", member.User.Username)

		full_profile, err := utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)
		if err != nil {
			fmt.Printf("💥 Couldn't get profile for %s: %v\n", member.User.Username, err)
			errs = append(errs, fmt.Errorf("failed to fetch milpac for %s: %v", member.User.Username, err))
			continue
		}

		// Check primary position
		fmt.Printf("🔍 Checking primary position for %s %s\n", member.User.Username, member.Primary.PositionTitle)
		if strings.Contains(full_profile.Primary.PositionTitle, "IT") && strings.Contains(full_profile.Primary.PositionTitle, "S6") {
			fmt.Printf("💻 Found IT primary position: %s\n", full_profile.Primary.PositionTitle)
			position, timeInPosition, positionDate, err := determineITPositionTime(full_profile.Primary.PositionTitle, full_profile)
			if err != nil {
				fmt.Printf("💥 Failed to determine time in position: %v\n", err)
				errs = append(errs, fmt.Errorf("failed to determine time in position: %v", err))
				continue
			}
			fmt.Printf("📅 Time in position: %s\n", timeInPosition)
			if positionDate.Before(current_date.AddDate(0, -6, 0)) {
				fmt.Printf("✅ %s eligible (IT over 6 months ago)\n", member.User.Username)
				fmt.Printf("%s %s %s\n", member.User.Username, positionDate, current_date.AddDate(0, -6, 0))
				eligible_members = append(eligible_members, fmt.Sprintf("%s (%s) - %s", member.User.Username, position, timeInPosition))
			}
		}

		// Check secondary positions
		fmt.Printf("🔍 Checking secondary positions for %s %s\n", member.User.Username, full_profile.Secondary)
		for _, secondary := range full_profile.Secondary {
			fmt.Printf("🔍 Checking secondary position for %s %s\n", member.User.Username, secondary.PositionTitle)
			if strings.Contains(secondary.PositionTitle, "IT") && strings.Contains(secondary.PositionTitle, "S6") {
				fmt.Printf("💻 Found IT secondary position: %s\n", secondary.PositionTitle)
				position, timeInPosition, positionDate, err := determineITPositionTime(secondary.PositionTitle, full_profile)
				if err != nil {
					fmt.Printf("💥 Failed to determine time in position: %v\n", err)
					errs = append(errs, fmt.Errorf("failed to determine time in position: %v", err))
					continue
				}
				fmt.Printf("📅 Time in position: %s\n", timeInPosition)
				if positionDate.Before(current_date.AddDate(0, -6, 0)) {
					fmt.Printf("✅ %s eligible (IT over 6 months ago)\n", member.User.Username)
					fmt.Printf("%s %s %s\n", member.User.Username, positionDate, current_date.AddDate(0, -6, 0))
					eligible_members = append(eligible_members, fmt.Sprintf("%s (%s) - %s", member.User.Username, position, timeInPosition))
				}
			}
		}
	}
	response := fmt.Sprintf("The following S6 members are eligible for Full Status:\n%s", strings.Join(eligible_members, "\n"))
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		fmt.Printf("💥 Failed to send results: %v\n", err)
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
	fmt.Printf("🔍 Starting search for %s - needed to track career progression for %s\n", positionName, member.User.Username)

	var date time.Time
	fmt.Printf("⏰ Initializing date tracker to zero - will be updated with earliest valid position record\n")

	for _, record := range member.Records {
		if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
			fmt.Printf("📝 Found record: %s\n", record.RecordDetails)
			record_date, err := time.Parse("2006-01-02", record.RecordDate)
			if err != nil {
				fmt.Printf("💥 Invalid date format in record (%s) - skipping to maintain data integrity\n", record.RecordDate)
				return "", "", time.Time{}, fmt.Errorf("failed to parse record date: %v", err)
			}

			if strings.Contains(record.RecordDetails, positionName) {
				fmt.Printf("🎯 Position match found in record: %s - evaluating if this is the earliest occurrence\n", record.RecordDetails)

				if date.IsZero() {
					date = record_date
					fmt.Printf("📍 First position record found - establishing baseline date: %v\n", date)
				} else if record_date.After(date) {
					fmt.Printf("⬅️ Found earlier record - updating from %v to %v to track first occurrence\n", date, record_date)
					date = record_date
				} else {
					fmt.Printf("⏭️ Record from %v is more recent than current date %v - keeping earlier date to track origin\n",
						record_date, date)
				}
			} else {
				fmt.Printf("🔄 Record does not match position - skipping\n")
			}
		}
	}
	positionDate = date
	timeInPosition = utils.FormatTimeSinceDuration(date)
	fmt.Printf("✅ Analysis complete - %s has been in position for %s\n", member.User.Username, timeInPosition)

	return positionName, timeInPosition, positionDate, nil
}
