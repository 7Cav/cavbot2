package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/go-resty/resty/v2"
	"github.com/golang-jwt/jwt/v5"
	"log"
	"os"
	"regexp"
	"strconv"
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
			branch := i.ApplicationCommandData().Options[0].StringValue()
			if err := validateBranchName(branch); err != nil {
				log.Printf("Invalid branch name: %v", err)
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: fmt.Sprintf("❌ Invalid branch name: %v", err),
					},
				})
				return
			}

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

			token, err := githubAuth(clientID, privateKeyPEM)
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
			ref := "main"
			log.Printf("Deploying branch %s to %s/%s/%s", branch, owner, repo, workflow)
			err = triggerGithubDeployment(branch, token, owner, repo, workflow, ref)
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

func githubAuth(clientID string, privateKey []byte) (string, error) {
	now := time.Now()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(time.Minute * 9).Unix(),
		"iss": clientID,
	})

	jwtToken.Header["alg"] = "RS256"

	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}
	signedToken, err := jwtToken.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign jwtToken: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(1 * time.Second)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+signedToken).
		Get("https://api.github.com/app/installations")

	if err != nil {
		return "", fmt.Errorf("failed to get installations: %w", err)
	}
	if resp.StatusCode() != 200 {
		return "", fmt.Errorf("github API returned non-200 status code: %d %s", resp.StatusCode(), resp.Body())
	}
	var installations []struct {
		ID int `json:"id"`
	}
	err = json.Unmarshal(resp.Body(), &installations)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal installations: %w", err)
	}
	if len(installations) == 0 {
		return "", fmt.Errorf("no installations found")
	}
	installationID := strconv.Itoa(installations[0].ID)
	log.Printf("Found installation ID: %s", installationID)

	tokenResp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+signedToken).
		Post(fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", installationID))
	if err != nil {
		return "", fmt.Errorf("failed to get installation jwtToken: %w", err)
	}
	if tokenResp.StatusCode() != 201 {
		return "", fmt.Errorf("github API returned non-201 status code: %d %s", tokenResp.StatusCode(), tokenResp.Body())
	}
	var tokenData struct {
		Token string `json:"token"`
	}
	err = json.Unmarshal(tokenResp.Body(), &tokenData)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal jwtToken: %w", err)
	}

	return tokenData.Token, nil

}

func triggerGithubDeployment(branch string, token string, owner string, repo string, workflow string, ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(1 * time.Second)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		SetBody(map[string]interface{}{
			"ref": ref,
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

func validateBranchName(branch string) error {
	validPattern := regexp.MustCompile(`^(?!\.)[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

	if len(branch) == 0 || len(branch) > 255 {
		return fmt.Errorf("branch name must be between 1 and 255 characters")
	}

	if !validPattern.MatchString(branch) {
		return fmt.Errorf("invalid branch name: must start with alphanumeric and contain only alphanumeric, dots, hyphens, or underscores")
	}

	return nil
}
