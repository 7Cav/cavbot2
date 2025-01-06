package commands

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/go-resty/resty/v2"
	"log"
	"os"
)

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
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			options := i.ApplicationCommandData().Options
			optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
			for _, opt := range options {
				optionMap[opt.Name] = opt
			}

			user := optionMap["user"].UserValue(s)
			milpac, err := GetMilpac(user.ID)
			if err != nil {
				errResponse := &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: err.Error(),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				}
				if respErr := s.InteractionRespond(i.Interaction, errResponse); respErr != nil {
					log.Printf("Failed to send error response: %v", respErr)
				}
				return
			}
			fields := []*discordgo.MessageEmbedField{
				{
					Value: fmt.Sprintf("**Username:** %s", milpac.User.Username),
				},
				{
					Value: fmt.Sprintf("**Rank:** %s (%s)\n**Promotion Date:** %s", milpac.Rank.RankFull, milpac.Rank.RankShort, milpac.PromotionDate),
				},
				{
					Value: fmt.Sprintf("**Primary Position:** %s", milpac.Primary.PositionTitle),
				},
			}
			log.Printf("Returning Milpac for: %v", milpac.User.Username)
			response := fmt.Sprintf("%v %v", milpac.Rank.RankFull, milpac.RealName)

			respErr := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{
						{
							Title:  response,
							Fields: fields,
							Thumbnail: &discordgo.MessageEmbedThumbnail{
								URL: milpac.Rank.RankImageUrl,
							},
							Image: &discordgo.MessageEmbedImage{
								URL: milpac.UniformUrl,
							},
						},
					},
				},
			})
			if respErr != nil {
				log.Printf("Failed to respond to interaction %v\n", err)
			}

		},
	}
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

func GetMilpac(DiscordID string) (*Response, error) {
	bearer := os.Getenv("BEARER")
	client := resty.New()

	var result Response
	response, err := client.R().
		SetAuthToken(bearer).
		SetResult(&result).
		Get(fmt.Sprintf("https://api.7cav.us/api/v1/milpac/discord/%s", DiscordID))

	if err != nil {
		return nil, fmt.Errorf("failed to fetch milpac: %w", err)
	}
	if response != nil && response.StatusCode() == 404 {
		return nil, fmt.Errorf("no milpac found for this user")
	}
	return &result, err
}
