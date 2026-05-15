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
		utils.CaptureError("❌ Interaction response failed", err)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	// 5min accommodates the current serial-fetch shape (~1.4s per eligible member,
	// rosters of 50+); well under Discord's 15min interaction-token cliff.
	// Parallelizing the per-member fetches would let this drop back to 60s — see #86.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	utils.Debug("📊 Fetching roster data", "department", choice)
	Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, choice)
	if err != nil {
		utils.CaptureError("❌ Roster fetch failed", err)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch Members: %v", err))
		return
	}
	utils.Info("📋 Retrieved roster", "member_count", len(Members.LiteProfiles))

	if len(Members.LiteProfiles) == 0 {
		utils.CaptureError(
			"AFSM roster lookup returned zero members",
			fmt.Errorf("empty roster for department %q", choice),
			"department", choice,
		)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("⚠️ The %s roster came back empty — this shouldn't happen for a preset department. The issue has been reported.", choice))
		return
	}

	currentDate := time.Now()
	utils.Debug("⏰ Current date set", "date", currentDate)

	eligibleMembers := []AFSMMember{}
	skippedCount := 0
	for _, member := range Members.LiteProfiles {
		result, err := evaluateAFSMMember(ctx, member, choice, currentDate)
		if err != nil {
			utils.CaptureError(
				"AFSM member evaluation failed",
				err,
				"username", member.User.Username,
				"department", choice,
			)
			skippedCount++
			continue
		}
		if result != nil {
			eligibleMembers = append(eligibleMembers, *result)
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

	// Disclaimer always renders. The command parses user-entered milpac data, so
	// formatting drift on the roster side can silently skew results — the user
	// needs the warning regardless of whether the eligibles list is empty.
	const disclaimer = "⚠️ This command cannot be made completely accurate. Please check the output carefully."

	var response string
	if len(AFSMUserOutput) > 0 {
		response = fmt.Sprintf("%s\nThe following %s members are eligible for AFSM:\n%s", disclaimer, choice, strings.Join(AFSMUserOutput, "\n"))
		utils.Info("✅ Found eligible members", "department", choice, "count", len(AFSMUserOutput))
	} else {
		response = fmt.Sprintf("%s\nNo %s members found eligible for AFSM", disclaimer, choice)
		utils.Info("📭 No eligible members found", "department", choice)
	}

	if skippedCount > 0 {
		noun := "members"
		if skippedCount == 1 {
			noun = "member"
		}
		response += fmt.Sprintf("\n⚠️ %d %s skipped due to errors (reported)", skippedCount, noun)
		utils.Info("⚠️ Members skipped during AFSM evaluation", "department", choice, "count", skippedCount)
	}

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.CaptureError("❌ Response edit failed", err)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Command completed successfully", "command", "AFSM", "department", choice)
}

// evaluateAFSMMember returns (member, nil) when the roster entry is eligible
// for the AFSM, (nil, nil) when it is not, and (nil, err) when the entry's
// data could not be processed and should be skipped + reported.
func evaluateAFSMMember(
	ctx context.Context,
	member utils.LiteProfileResponse,
	dept string,
	currentDate time.Time,
) (*AFSMMember, error) {
	utils.Debug("👤 Processing member", "username", member.User.Username)

	eligible := false
	for _, secondary := range member.Secondary {
		utils.Debug("🔍 Checking secondary position", "position", secondary.PositionTitle, "department", dept)
		if strings.Contains(secondary.PositionTitle, dept) {
			eligible = true
			utils.Debug("✅ Member eligible through secondary", "position", secondary.PositionTitle)
		}
		if strings.Contains(member.Primary.PositionTitle, dept) {
			eligible = false
			utils.Debug("❌ Member ineligible due to primary position", "position", member.Primary.PositionTitle)
		}
	}
	if !eligible {
		return nil, nil
	}

	utils.Info("🎯 Found eligible member", "username", member.User.Username)
	fullProfile, err := utils.GetMilpacByUsername(ctx, member.User.Username)
	if err != nil {
		return nil, fmt.Errorf("milpac fetch failed: %w", err)
	}

	utils.Debug("📝 Checking assignments", "username", member.User.Username)
	var assignments []map[string]interface{}
	for _, record := range fullProfile.Records {
		if record.RecordType != "RECORD_TYPE_ASSIGNMENT" &&
			record.RecordType != "RECORD_TYPE_TRANSFER" &&
			record.RecordType != "RECORD_TYPE_ELOA" &&
			record.RecordType != "RECORD_TYPE_DISCHARGE" {
			continue
		}
		utils.Debug("📋 Processing record", "type", record.RecordType, "date", record.RecordDate)
		recordDate, err := time.Parse("2006-01-02", record.RecordDate)
		if err != nil {
			return nil, fmt.Errorf("record date parse failed for %q: %w", record.RecordDate, err)
		}
		if strings.Contains(record.RecordDetails, dept) ||
			strings.Contains(record.RecordDetails, "ELOA") ||
			strings.Contains(record.RecordDetails, "Discharge") ||
			strings.Contains(record.RecordDetails, "Retired") {
			event := map[string]interface{}{
				"record_date":    recordDate,
				"record_type":    determineRecordType(record.RecordDetails),
				"record_details": record.RecordDetails,
			}
			assignments = append(assignments, event)
			utils.Debug("✍️ Added assignment record", "type", event["record_type"], "date", recordDate)
		}
	}

	sort.Slice(assignments, func(i, j int) bool {
		return assignments[i]["record_date"].(time.Time).Before(assignments[j]["record_date"].(time.Time))
	})
	utils.Debug("📊 Sorted assignments", "count", len(assignments))

	var startDate time.Time
	for _, assignment := range assignments {
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

	if startDate.IsZero() || !startDate.Before(currentDate.AddDate(-1, 0, 0)) {
		utils.Debug("⏳ Member has not served long enough", "username", member.User.Username)
		return nil, nil
	}

	utils.Debug("✅ Member meets time requirement, checking awards", "username", member.User.Username)
	var latestAward time.Time
	for _, award := range fullProfile.Awards {
		if award.AwardName != "Armed Forces Service Medal" || !strings.Contains(award.AwardDetails, dept) {
			continue
		}
		utils.Debug("🎖️ Found AFSM award", "date", award.AwardDate, "details", award.AwardDetails)
		awardDate, err := time.Parse("2006-01-02", award.AwardDate)
		if err != nil {
			return nil, fmt.Errorf("award date parse failed for %q: %w", award.AwardDate, err)
		}
		if latestAward.IsZero() || awardDate.After(latestAward) {
			latestAward = awardDate
			utils.Debug("📅 Updated latest award date", "date", latestAward)
		}
	}

	if !latestAward.IsZero() && !latestAward.Before(currentDate.AddDate(-1, 0, 0)) {
		utils.Debug("⏳ Member not yet eligible since last award", "username", member.User.Username, "last_award", latestAward)
		return nil, nil
	}

	milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
	if err != nil {
		return nil, fmt.Errorf("uniform URL parse failed: %w", err)
	}

	utils.Info("✨ Member eligible", "username", member.User.Username, "start_date", startDate)
	refDate := startDate
	if !latestAward.IsZero() {
		refDate = latestAward
	}
	return &AFSMMember{
		Username:  member.User.Username,
		MilpacUrl: fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID),
		TimeSince: utils.FormatTimeSinceDuration(refDate),
		Date:      refDate,
	}, nil
}

func determineRecordType(details string) string {
	if strings.Contains(details, "Relieved") || strings.Contains(details, "ELOA") || strings.Contains(details, "Discharge") || strings.Contains(details, "Retired") {
		return "leave"
	}
	return "join"
}
