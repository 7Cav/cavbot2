package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"github.com/go-resty/resty/v2"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

var rosterMap = map[string]string{
	"ROSTER_TYPE_COMBAT":        "Active Duty",
	"ROSTER_TYPE_RESERVE":       "Reserves",
	"ROSTER_TYPE_ELOA":          "Extended Leave of Absence",
	"ROSTER_TYPE_WALL_OF_HONOR": "Wall of Honor",
	"ROSTER_TYPE_ARLINGTON":     "Arlington National Cemetery",
	"ROSTER_TYPE_PAST_MEMBERS":  "Past Members",
}

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

type Response struct {
	User struct {
		UserID   string `json:"userId"`
		Username string `json:"username"`
	} `json:"user"`
	Rank struct {
		RankShort    string `json:"rankShort"`
		RankFull     string `json:"rankFull"`
		RankImageUrl string `json:"rankImageUrl"`
		RankID       string `json:"rankId"`
	} `json:"rank"`
	RealName   string `json:"realName"`
	UniformUrl string `json:"uniformUrl"`
	Roster     string `json:"roster"`
	Primary    struct {
		PositionTitle string `json:"positionTitle"`
		PositionID    string `json:"positionId"`
	} `json:"primary"`
	JoinDate      string `json:"joinDate"`
	PromotionDate string `json:"promotionDate"`
}

func (r *Response) GetRosterStatus() string {
	if status, exists := rosterMap[r.Roster]; exists {
		return status
	}
	log.Printf("Roster status not found for: %s", r.Roster)
	return r.Roster
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
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	options := i.ApplicationCommandData().Options
	optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optionMap[opt.Name] = opt
	}

	log.Printf("Milpac requested by %s %s", i.Member.User.Username, i.Member.User.ID)
	user := optionMap["user"].UserValue(s)

	go processMilpacRequest(s, i, user)
}

func processMilpacRequest(s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	milpac, err := GetMilpac(ctx, user.ID)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch milpac: %v", err))
		return
	}
	joinDate, err := time.Parse("2006-01-02", milpac.JoinDate)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse join date: %v", err))
		return
	}
	formatJoinDate := joinDate.Format("02Jan2006")
	capitalizedJoinDate := strings.ToUpper(formatJoinDate)
	var promotionDate time.Time
	if milpac.PromotionDate != "" {
		promotionDate, err := time.Parse("2006-01-02", milpac.PromotionDate)
		if err != nil || promotionDate.IsZero() {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse promotion date: %v", err))
			return
		}
	} else {
		promotionDate = joinDate
	}
	formatPromotionDate := promotionDate.Format("02Jan2006")
	capitalizedPromotionDate := strings.ToUpper(formatPromotionDate)
	timeInService := utils.FormatTimeSinceDuration(joinDate)
	timeInGrade := utils.FormatTimeSinceDuration(promotionDate)

	fields := []*discordgo.MessageEmbedField{
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
			Value: fmt.Sprintf("%s\nTime Since Enlistment: %s", capitalizedJoinDate, timeInService),
		},
	}
	matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(milpac.UniformUrl)
	if len(matches) < 2 {
		utils.HandleError(s, i, "❌ Failed to parse uniform URL")
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
	log.Printf("Returning Milpac for: %s requested by %s %s",
		embed.Title,
		i.Member.User.Username,
		i.Member.User.ID)
	emptyContent := ""
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &emptyContent,
		Embeds:  &[]*discordgo.MessageEmbed{embed},
	})
	if err != nil {
		log.Printf("Failed to edit response with embed: %v", err)
	}
}

func GetMilpac(ctx context.Context, DiscordID string) (*Response, error) {
	start := time.Now()
	bearer := os.Getenv("BEARER")
	client := resty.New()

	var result Response
	response, err := client.R().
		SetContext(ctx).
		SetAuthToken(bearer).
		SetResult(&result).
		Get(fmt.Sprintf("https://api.7cav.us/api/v1/milpac/discord/%s", DiscordID))
	duration := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("failed to fetch milpac: %w", err)
	}
	if response != nil && response.StatusCode() == 404 {
		return nil, fmt.Errorf("no milpac associated with this user's Discord ID")
	}
	if response != nil {
		log.Printf("API call finished in %v - Status: %d, Discord ID: %s",
			duration,
			response.StatusCode(),
			DiscordID)
	}
	return &result, err
}
