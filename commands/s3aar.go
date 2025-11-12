package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

type PlayerSession struct {
	Name         string `json:"name"`
	Playtime     int    `json:"playtime"`
	CavName      string `json:"cav_name"`
	MilpacsLink  string `json:"milpacs_link"`
	SearchString string `json:"search_string"`
	Roster       string `json:"roster"`
	RankID       string `json:"rank_id"`
}

type BMResponse struct {
	Data []struct {
		Attributes struct {
			Start time.Time `json:"start"`
			Stop  time.Time `json:"stop"`
			Name  string    `json:"name"`
		} `json:"attributes"`
	} `json:"data"`
}

func parseDateTime(dateStr, timeStr string) (time.Time, error) {
	months := map[string]string{
		"JAN": "01", "FEB": "02", "MAR": "03", "APR": "04",
		"MAY": "05", "JUN": "06", "JUL": "07", "AUG": "08",
		"SEP": "09", "OCT": "10", "NOV": "11", "DEC": "12",
	}

	if len(dateStr) != 7 || len(timeStr) != 4 {
		return time.Time{}, fmt.Errorf("invalid date or time format")
	}

	day := dateStr[:2]
	monAbbr := strings.ToUpper(dateStr[2:5])
	year := dateStr[5:]

	month, ok := months[monAbbr]
	if !ok {
		return time.Time{}, fmt.Errorf("invalid month abbreviation: %s", monAbbr)
	}

	hour := timeStr[:2]
	min := timeStr[2:]

	iso := fmt.Sprintf("20%s-%s-%sT%s:%s:00Z", year, month, day, hour, min)
	return time.Parse(time.RFC3339, iso)
}

func getServerID(server string) (string, error) {
	switch server {
	case "Tac1":
		return "35142042", nil
	case "Tac2":
		return "32703792", nil
	case "TS1":
		return "32703836", nil
	case "TS2":
		return "32708755", nil
	default:
		return "", fmt.Errorf("invalid server selection: %s", server)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func cleanName(name string) string {
	name = strings.TrimSpace(name)

	pattern := regexp.MustCompile(`(?i)[A-Za-z0-9]{3}\.([A-Za-z]+\.[A-Za-z]{1,2})`)
	match := pattern.FindStringSubmatch(name)
	if len(match) == 2 {
		return match[1]
	}
	return name
}

func enrichPlayer(ctx context.Context, rawName string) (string, string, string, string, string, error) {
	cleaned := cleanName(rawName)

	profile, err := utils.GetMilpacByUsername(ctx, cleaned)
	if err == nil {
		matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(profile.UniformUrl)
		if len(matches) != 2 {
			return "", "", "", "", "", fmt.Errorf("failed to extract ID from UniformUrl: %s", profile.UniformUrl)
		}
		id := matches[1]
		cavName := fmt.Sprintf("%s %s", profile.Rank.RankFull, profile.User.Username)
		link := fmt.Sprintf("https://7cav.us/rosters/profile/%s", id)
		roster := profile.Roster
		rankID := profile.Rank.RankID
		return cavName, link, cleaned, roster, rankID, nil
	}

	profile, err = utils.GetUserByGamertag(ctx, rawName)
	if err == nil {
		matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(profile.UniformUrl)
		if len(matches) != 2 {
			return "", "", "", "", "", fmt.Errorf("failed to extract ID from UniformUrl: %s", profile.UniformUrl)
		}
		id := matches[1]
		cavName := fmt.Sprintf("%s %s", profile.Rank.RankFull, profile.User.Username)
		link := fmt.Sprintf("https://7cav.us/rosters/profile/%s", id)
		roster := profile.Roster
		rankID := profile.Rank.RankID
		return cavName, link, rawName, roster, rankID, nil
	}

	return "", "", "", "", "", fmt.Errorf("no match found for %s", rawName)
}

func fetchBattleMetricsSessions(serverID string, start, stop time.Time, minAttendance int) ([]PlayerSession, error) {
	token := os.Getenv("BM_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("BM_TOKEN not set")
	}

	endpoint := fmt.Sprintf("https://api.battlemetrics.com/servers/%s/relationships/sessions", serverID)
	params := url.Values{}
	params.Set("start", start.Format(time.RFC3339))
	params.Set("stop", stop.Format(time.RFC3339))

	reqURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BattleMetrics API error: %s", resp.Status)
	}

	var bmResp BMResponse
	if err := json.NewDecoder(resp.Body).Decode(&bmResp); err != nil {
		return nil, err
	}

	playtimeMap := map[string]time.Duration{}
	for _, session := range bmResp.Data {
		name := session.Attributes.Name
		sStart := session.Attributes.Start
		sStop := session.Attributes.Stop

		clippedStart := maxTime(sStart, start)
		clippedStop := minTime(sStop, stop)

		if clippedStop.After(clippedStart) {
			duration := clippedStop.Sub(clippedStart)
			playtimeMap[name] += duration
		}
	}

	var sessions []PlayerSession
	ctx := context.Background()

	for name, duration := range playtimeMap {
		minutes := int(duration.Minutes())
		if minutes >= minAttendance {
			cleaned := cleanName(name)
			cavName, link, searchString, roster, rankID, _ := enrichPlayer(ctx, cleaned)
			sessions = append(sessions, PlayerSession{
				Name:         name,
				Playtime:     minutes,
				CavName:      cavName,
				MilpacsLink:  link,
				SearchString: searchString,
				Roster:       roster,
				RankID:       rankID,
			})
		}
	}

	return sessions, nil
}

func buildAttendanceEmbed(sessions []PlayerSession) *discordgo.MessageEmbed {
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].Playtime > sessions[j].Playtime
	})

	var lines []string
	for _, s := range sessions {
		hours := s.Playtime / 60
		minutes := s.Playtime % 60
		lines = append(lines, fmt.Sprintf("**%s** — %dh %dm", s.Name, hours, minutes))
	}

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Attendance Summary — %d Players", len(sessions)),
		Description: strings.Join(lines, "\n"),
		Color:       0x00aaff,
	}
}

func S3AAR() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "s3aar",
			Description: "Generates attendance list for events and operations.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "game",
					Description: "Select game.",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Arma", Value: "Arma"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "server",
					Description: "Select the server (Tac1, Tac2, TS1, TS2, NotAvailable)",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Tac1", Value: "Tac1"},
						{Name: "Tac2", Value: "Tac2"},
						{Name: "TS1", Value: "TS1"},
						{Name: "TS2", Value: "TS2"},
						{Name: "NotAvailable", Value: "NotAvailable"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "start_date",
					Description: "Start Date (DDMMMYY, e.g. 10NOV25)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "end_date",
					Description: "End Date (DDMMMYY, e.g. 10NOV25)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "start_time",
					Description: "Start Time (HHMM, 24hr UTC, e.g. 1830)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "end_time",
					Description: "End Time (HHMM, 24hr UTC, e.g. 1945)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "minimum_attendance",
					Description: "Minimum attendance time for credit (in minutes. e.g. 60)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "debug",
					Description: "Attach JSON debug output? (Yes/No)",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Yes", Value: "Yes"},
						{Name: "No", Value: "No"},
					},
				},
			},
		},
		Handler: handleS3AARCommand,
	}
}

func handleS3AARCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("Failed to defer interaction: %v", err))
		return
	}

	startDate := optionMap["start_date"].StringValue()
	endDate := optionMap["end_date"].StringValue()
	startTime := optionMap["start_time"].StringValue()
	endTime := optionMap["end_time"].StringValue()
	server := optionMap["server"].StringValue()
	minAttendance := int(optionMap["minimum_attendance"].IntValue())
	debug := "No"
	if opt, ok := optionMap["debug"]; ok {
		debug = opt.StringValue()
	}

	start, err := parseDateTime(startDate, startTime)
	if err != nil {
		_, sendErr := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Invalid start date/time: %v", err),
		})
		if sendErr != nil {
			utils.HandleError(s, i, fmt.Sprintf("Failed to send error message: %v", sendErr))
		}
		return
	}

	stop, err := parseDateTime(endDate, endTime)
	if err != nil {
		_, sendErr := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Invalid end date/time: %v", err),
		})
		if sendErr != nil {
			utils.HandleError(s, i, fmt.Sprintf("Failed to send error message: %v", sendErr))
		}
		return
	}

	serverID, err := getServerID(server)
	if err != nil {
		_, sendErr := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Invalid server selection: %v", err),
		})
		if sendErr != nil {
			utils.HandleError(s, i, fmt.Sprintf("Failed to send error message: %v", sendErr))
		}
		return
	}

	sessions, err := fetchBattleMetricsSessions(serverID, start, stop, minAttendance)
	if err != nil {
		_, sendErr := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: fmt.Sprintf("Failed to fetch BattleMetrics data: %v", err),
		})
		if sendErr != nil {
			utils.HandleError(s, i, fmt.Sprintf("Failed to send error message: %v", sendErr))
		}
		return
	}

	embed1 := buildAttendanceEmbed(sessions)
	var combatRoster []PlayerSession
	for _, s := range sessions {
		if s.Roster == "ROSTER_TYPE_COMBAT" {
			combatRoster = append(combatRoster, s)
		}
	}

	sort.SliceStable(combatRoster, func(i, j int) bool {
		rankI, errI := strconv.Atoi(combatRoster[i].RankID)
		rankJ, errJ := strconv.Atoi(combatRoster[j].RankID)

		if errI != nil || errJ != nil {
			return combatRoster[i].RankID < combatRoster[j].RankID
		}
		return rankI < rankJ
	})

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed1},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("Failed to send embeds: %v", err))
		return
	}

	sort.SliceStable(combatRoster, func(i, j int) bool {
		rankI, errI := strconv.Atoi(combatRoster[i].RankID)
		rankJ, errJ := strconv.Atoi(combatRoster[j].RankID)
		if errI != nil || errJ != nil {
			return combatRoster[i].RankID < combatRoster[j].RankID
		}
		return rankI < rankJ
	})

	var forumLines []string
	for _, s := range combatRoster {
		forumLines = append(forumLines, fmt.Sprintf("[URL='%s']%s[/URL]", s.MilpacsLink, s.CavName))
	}
	forumText := strings.Join(forumLines, "\n")

	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: "AAR Roster (Copy Text to Forum Post)",
		Files: []*discordgo.File{
			{
				Name:        "aar_roster.txt",
				ContentType: "text/plain",
				Reader:      strings.NewReader(forumText),
			},
		},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("Failed to send AAR roster file: %v", err))
	}

	if debug == "Yes" {
		jsonData, err := json.MarshalIndent(sessions, "", "  ")
		if err != nil {
			_, sendErr := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Content: fmt.Sprintf("Failed to format session data: %v", err),
			})
			if sendErr != nil {
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to send error message: %v", sendErr))
			}
			return
		}

		_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Files: []*discordgo.File{
				{
					Name:        "attendance.json",
					ContentType: "application/json",
					Reader:      strings.NewReader(string(jsonData)),
				},
			},
		})
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("Failed to send JSON file: %v", err))
		}
	}
}
