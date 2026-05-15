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

type ITMember struct {
	Username     string
	MilpacUrl    string
	Position     string
	TimeSince    string
	PositionDate time.Time
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

func handleS6ITCheckCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting S6 IT Check", "command", "S6ITCheck", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Fetching S6 IT Check data...",
		},
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s6Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}

	if len(s6Members.LiteProfiles) == 0 {
		utils.CaptureError(
			"S6 IT roster lookup returned zero members",
			fmt.Errorf("empty roster for S6 fuzzy search"),
		)
		utils.HandleError(utils.NewSessionResponder(s), i, "⚠️ The S6 roster came back empty — this shouldn't happen. The issue has been reported.")
		return
	}

	currentDate := time.Now()
	eligibleMembers := []ITMember{}
	skippedCount := 0
	for _, member := range s6Members.LiteProfiles {
		rows, err := evaluateS6Member(ctx, member, currentDate)
		if err != nil {
			utils.CaptureError(
				"S6 IT member evaluation failed",
				err,
				"username", member.User.Username,
			)
			skippedCount++
			continue
		}
		eligibleMembers = append(eligibleMembers, rows...)
	}

	sort.Slice(eligibleMembers, func(i, j int) bool {
		if eligibleMembers[i].PositionDate.IsZero() {
			return false
		}
		if eligibleMembers[j].PositionDate.IsZero() {
			return true
		}
		return eligibleMembers[i].PositionDate.Before(eligibleMembers[j].PositionDate)
	})

	ITUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		ITUserOutput = append(ITUserOutput, fmt.Sprintf("[%s](<%s>) (%s) - %s", user.Username, user.MilpacUrl, user.Position, user.TimeSince))
	}
	response := fmt.Sprintf("The following S6 members are eligible for Full Status:\n%s", strings.Join(ITUserOutput, "\n"))

	if skippedCount > 0 {
		noun := "members"
		if skippedCount == 1 {
			noun = "member"
		}
		response += fmt.Sprintf("\n⚠️ %d %s skipped due to errors (reported)", skippedCount, noun)
		utils.Info("⚠️ Members skipped during S6 IT evaluation", "count", skippedCount)
	}

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "S6ITCheck")
}

// evaluateS6Member returns the zero-to-N ITMember rows produced by a single
// roster entry (each matching IT/S6 position yields its own row, preserving
// the previous primary-then-secondary behavior). Returns an error if any
// internal step fails — caller should skip the member and report to Sentry.
func evaluateS6Member(
	ctx context.Context,
	member utils.LiteProfileResponse,
	currentDate time.Time,
) ([]ITMember, error) {
	fullProfile, err := utils.GetMilpacByUsername(ctx, member.User.Username)
	if err != nil {
		return nil, fmt.Errorf("milpac fetch failed: %w", err)
	}

	var rows []ITMember

	appendIfMatch := func(positionTitle string) error {
		position, timeInPosition, positionDate, err := determineITPositionTime(positionTitle, fullProfile)
		if err != nil {
			return fmt.Errorf("position time computation failed for %q: %w", positionTitle, err)
		}
		milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
		if err != nil {
			return fmt.Errorf("uniform URL parse failed: %w", err)
		}
		milpacUrl := fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID)
		if positionDate.IsZero() {
			rows = append(rows, ITMember{
				Username:     member.User.Username,
				MilpacUrl:    milpacUrl,
				Position:     position,
				TimeSince:    "⚠️ No matching assignment record found",
				PositionDate: positionDate,
			})
		} else if positionDate.Before(currentDate.AddDate(0, -6, 0)) {
			rows = append(rows, ITMember{
				Username:     member.User.Username,
				MilpacUrl:    milpacUrl,
				Position:     position,
				TimeSince:    timeInPosition,
				PositionDate: positionDate,
			})
		}
		return nil
	}

	if strings.Contains(fullProfile.Primary.PositionTitle, "IT") && strings.Contains(fullProfile.Primary.PositionTitle, "S6") {
		if err := appendIfMatch(fullProfile.Primary.PositionTitle); err != nil {
			return nil, err
		}
	}

	for _, secondary := range fullProfile.Secondary {
		if strings.Contains(secondary.PositionTitle, "IT") && strings.Contains(secondary.PositionTitle, "S6") {
			if err := appendIfMatch(secondary.PositionTitle); err != nil {
				return nil, err
			}
		}
	}

	return rows, nil
}

func normalizePositionWords(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.TrimSuffix(w, "s")
	}
	return strings.Join(words, " ")
}

func determineITPositionTime(positionName string, member *utils.ProfileResponse) (position string, timeInPosition string, positionDate time.Time, err error) {
	var date time.Time
	normalizedPosition := normalizePositionWords(positionName)

	for _, record := range member.Records {
		if record.RecordType == "RECORD_TYPE_ASSIGNMENT" || record.RecordType == "RECORD_TYPE_TRANSFER" {
			recordDate, err := time.Parse("2006-01-02", record.RecordDate)
			if err != nil {
				return "", "", time.Time{}, fmt.Errorf("failed to parse record date: %v", err)
			}

			if strings.Contains(normalizePositionWords(record.RecordDetails), normalizedPosition) {

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
