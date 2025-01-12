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
	"strings"
	"time"
)

func AppsBetaDeploy() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "apps_beta_deploy",
			Description: "Deploy Apps Beta Version",
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
			log.Println("Apps Beta Deployer called")
			switch i.Type {
			case discordgo.InteractionApplicationCommand:
				branch := i.ApplicationCommandData().Options[0].StringValue()
				if err := validateBranchName(branch); err != nil {
					log.Printf("Invalid branch name: %v", err)
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: fmt.Sprintf("❌ Invalid branch name: %v", err),
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
					return
				}
				handleInitialCommand(s, i)
			case discordgo.InteractionMessageComponent:
				handleComponentInteraction(s, i)
			}

		},
	}
}

func handleInitialCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("Initial command handler called")
	branch := i.ApplicationCommandData().Options[0].StringValue()
	log.Printf("Branch name received: %s", branch)
	if len(fmt.Sprintf("apps_beta_deploy::confirm::%s", branch)) > 100 {
		log.Printf("Warning: Button CustomID exceeds Discord's limit")
	}
	confirmButtonID := fmt.Sprintf("apps_beta_deploy::confirm::%s", branch)
	cancelButtonID := fmt.Sprintf("apps_beta_deploy::cancel::%s", branch)

	log.Printf("Creating buttons with IDs - Confirm: %s, Cancel: %s", confirmButtonID, cancelButtonID)

	confirmButton := discordgo.Button{
		CustomID: confirmButtonID,
		Label:    "Confirm Deploy",
		Style:    discordgo.SuccessButton,
	}

	cancelButton := discordgo.Button{
		CustomID: cancelButtonID,
		Label:    "Cancel Deploy",
		Style:    discordgo.DangerButton,
	}

	actionRow := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			confirmButton,
			cancelButton,
		},
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("⚠️ Are you sure you want to deploy branch `%s` to apps-beta?", branch),
			Components: []discordgo.MessageComponent{
				actionRow,
			},
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		log.Printf("Error responding to initial command: %v", err)
	}
}

func handleComponentInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	log.Printf("Component interaction received")
	customID := i.MessageComponentData().CustomID
	log.Printf("CustomID received: %s", customID)

	parts := strings.Split(customID, "::")
	log.Printf("Split CustomID parts: %v", parts)

	if len(parts) != 3 {
		log.Printf("Invalid custom ID format: expected 3 parts, got %d parts: %v", len(parts), parts)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Invalid interaction format",
			},
		})
		return
	}

	command := parts[0]
	action := parts[1]
	branch := parts[2]

	log.Printf("Parsed command: %s, action: %s, branch: %s", command, action, branch)

	if action == "cancel" {
		log.Printf("Processing cancel action for branch: %s", branch)
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    fmt.Sprintf("❌ Deployment cancelled for branch `%s`", branch),
				Components: []discordgo.MessageComponent{},
			},
		})
		if err != nil {
			log.Printf("Error responding to cancel interaction: %v", err)
		}
		return
	}
	log.Printf("Sending please wait response")
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    fmt.Sprintf("✅ Deployment initiated, please wait..."),
			Components: []discordgo.MessageComponent{},
		},
	})

	log.Printf("Starting deployment process for branch: %s", branch)
	encodedPrivateKey := os.Getenv("GITHUB_APP_KEY")
	if encodedPrivateKey == "" {
		log.Printf("GITHUB_APP_KEY environment variable is empty")
		handleError(s, i, "❌ GitHub App key not configured")
		return
	}

	privateKeyPEM, err := base64.StdEncoding.DecodeString(encodedPrivateKey)
	if err != nil {
		log.Printf("Failed to decode private key: %v", err)
		handleError(s, i, fmt.Sprintf("❌ Failed to decode private key: %v", err))
		return
	}

	clientID := os.Getenv("GITHUB_APP_CLIENT_ID")
	if clientID == "" {
		log.Printf("GITHUB_APP_CLIENT_ID environment variable is empty")
		handleError(s, i, "❌ GitHub App client ID not configured")
		return
	}

	log.Printf("Starting GitHub authentication process")
	token, err := githubAuth(clientID, privateKeyPEM)
	if err != nil {
		log.Printf("GitHub authentication failed: %v", err)
		handleError(s, i, fmt.Sprintf("❌ Failed to authenticate with GitHub: %v", err))
		return
	}
	log.Printf("GitHub authentication successful")

	owner := "7cav"
	repo := "adr"
	workflow := "dev_deploy.yml"
	ref := "main"
	log.Printf("Triggering deployment - Owner: %s, Repo: %s, Workflow: %s, Branch: %s", owner, repo, workflow, branch)
	err = checkBranchExists(branch, token, owner, repo)
	if err != nil {
		log.Printf("Branch does not exist: %v", err)
		handleError(s, i, fmt.Sprintf("❌ Branch does not exist: %v", err))
		return
	}
	err = triggerGithubDeployment(branch, token, owner, repo, workflow, ref)
	var response string
	if err != nil {
		log.Printf("Deployment failed: %v", err)
		response = fmt.Sprintf("❌ Failed to trigger Apps Beta deployment: %v", err)
	} else {
		log.Printf("Deployment triggered successfully")
		response = fmt.Sprintf("✅ Apps Beta deployment started for branch `%s` by <@%s> \nCheck status at: https://github.com/7cav/adr/actions/workflows/dev_deploy.yml", branch, i.Member.User.ID)
	}

	log.Printf("Sending response: %s", response)
	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: response,
	})
	if err != nil {
		log.Printf("Error sending interaction response: %v", err)
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
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

	if len(branch) == 0 || len(branch) > 255 {
		return fmt.Errorf("branch name must be between 1 and 255 characters")
	}

	if branch[0] == '.' {
		return fmt.Errorf("invalid branch name: must not start with a dot")
	}

	if !validPattern.MatchString(branch) {
		return fmt.Errorf("invalid branch name: must start with alphanumeric and contain only alphanumeric, dots, hyphens, or underscores")
	}

	return nil
}

func handleError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	log.Printf("Handling error: %s", message)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: message,
		},
	})
	if err != nil {
		if !strings.Contains(err.Error(), "already been acknowledged") {
			log.Printf("Error sending initial response: %v", err)
		}

		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &message,
		})
		if err != nil {
			log.Printf("Error editing response: %v", err)
		}
	}
}

func checkBranchExists(branch, token, owner, repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(1 * time.Second)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		Get(fmt.Sprintf("https://api.github.com/repos/%s/%s/branches/%s", owner, repo, branch))

	if err != nil {
		return fmt.Errorf("failed to check branch: %w", err)
	}

	if resp.StatusCode() == 404 {
		return fmt.Errorf("branch '%s' does not exist in repository %s/%s", branch, owner, repo)
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("github API returned unexpected status code: %d %s", resp.StatusCode(), resp.Body())
	}

	return nil
}
