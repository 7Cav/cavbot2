package commands

import (
	"encoding/base64"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/go-resty/resty/v2"
	"github.com/golang-jwt/jwt/v5"
	"log"
	"os"
	"time"
)

func AdrDeploy() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "adr_deploy",
			Description: "Deploy ADR Beta Version",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "branch",
					Description: "Branch to deploy",
					Required:    true,
				},
			},
		},

		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			log.Println("ADR Deployer called")
			encodedPrivateKey := os.Getenv("GITHUB_APP_KEY")
			privateKeyPEM, err := base64.StdEncoding.DecodeString(encodedPrivateKey)
			if err != nil {
				log.Printf("Failed to decode private key: %v", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: fmt.Sprintf("❌ Failed to decode private key: %v", err),
					},
				})
				return
			}
			clientID := os.Getenv("GITHUB_APP_ClIENT_ID")

			token, err := generateJWT(clientID, privateKeyPEM)
			if err != nil {
				log.Printf("Failed to generate JWT: %v", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: fmt.Sprintf("❌ Failed to authenticate with GitHub: %v", err),
					},
				})
				return
			}

			branch := i.ApplicationCommandData().Options[0].StringValue()
			owner := "7cav"
			repo := "adr"
			workflow := "dev_deploy.yml"
			log.Printf("Deploying branch %s to %s/%s/%s", branch, owner, repo, workflow)
			err = triggerGithubDeployment(branch, token, owner, repo, workflow)
			log.Printf("Triggered ADR deployment for branch %s", branch)
			var response string
			if err != nil {
				response = fmt.Sprintf("❌ Failed to trigger ADR deployment: %v", err)
			} else {
				response = fmt.Sprintf("✅ ADR deployment started for branch `%s` \n Check status at: https://github.com/7cav/adr/actions/workflows/dev_deploy.yml", branch)
			}
			log.Printf(response)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: response,
				},
			})
		},
	}
}

func generateJWT(clientID string, privateKey []byte) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(time.Minute * 9).Unix(),
		"iss": clientID,
	})

	token.Header["alg"] = "RS256"

	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}
	signedToken, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}
	return signedToken, nil
}

func triggerGithubDeployment(branch string, token string, owner string, repo string, workflow string) error {
	client := resty.New()
	resp, err := client.R().
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		SetBody(map[string]interface{}{
			"ref": "main",
			"inputs": map[string]interface{}{
				"branch": branch,
			},
		}).
		Post(fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, workflow))

	if err != nil {
		return fmt.Errorf("failed to trigger github workflow: %w", err)
	}
	if resp.StatusCode() != 204 {
		return fmt.Errorf("github API returned non-204 status code: %d %s", resp.StatusCode(), resp.Body())
	}
	return nil
}
