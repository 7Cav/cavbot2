package commands

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
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

		Handler: handleAppsBetaDeploy,
	}
}

func handleAppsBetaDeploy(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runAppsBetaDeploy(utils.NewSessionResponder(s), i)
}

func runAppsBetaDeploy(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("Apps Beta Deployer called", "command", "AppsBetaDeploy", "username", username, "discord_id", discordID)
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		runAppsBetaInitialCommand(r, i)
	case discordgo.InteractionMessageComponent:
		runAppsBetaComponentInteraction(r, i)
	}
}

func runAppsBetaInitialCommand(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	utils.Info("Initial command handler called", "command", "AppsBetaDeploy")
	branch := i.ApplicationCommandData().Options[0].StringValue()
	if err := utils.HandleValidateBranchName(branch); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Invalid branch name: %v", err))
		return
	}
	if len(fmt.Sprintf("apps_beta_deploy::confirm::%s", branch)) > 100 {
		utils.HandleError(r, i, "❌ branch name exceeds Discord's limit")
		return
	}
	confirmButton := discordgo.Button{
		CustomID: fmt.Sprintf("apps_beta_deploy::confirm::%s", branch),
		Label:    "Confirm Deploy",
		Style:    discordgo.SuccessButton,
	}
	cancelButton := discordgo.Button{
		CustomID: fmt.Sprintf("apps_beta_deploy::cancel::%s", branch),
		Label:    "Cancel Deploy",
		Style:    discordgo.DangerButton,
	}
	actionRow := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			confirmButton,
			cancelButton,
		},
	}
	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
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
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to initial command: %v", err))
		return
	}
}

func runAppsBetaComponentInteraction(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	utils.Info("Component interaction received")
	customID := i.MessageComponentData().CustomID
	parts := strings.Split(customID, "::")
	if len(parts) != 3 {
		utils.HandleError(r, i, fmt.Sprintf("❌ Invalid custom ID format: expected 3 parts, got %d parts: %v", len(parts), parts))
		return
	}
	action, branch := parts[1], parts[2]

	if action == "cancel" {
		err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    fmt.Sprintf("❌ Deployment cancelled for branch `%s`", branch),
				Components: []discordgo.MessageComponent{},
			},
		})
		if err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Error responding to cancel interaction: %v", err))
			return
		}
		return
	}
	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    "✅ Deployment initiated, please wait...",
			Components: []discordgo.MessageComponent{},
		},
	})
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to update initial message: %v", err))
		return
	}
	encodedPrivateKey := os.Getenv("GITHUB_APP_KEY")
	if encodedPrivateKey == "" {
		utils.HandleError(r, i, "❌ GitHub App key not configured")
		return
	}
	privateKeyPEM, err := base64.StdEncoding.DecodeString(encodedPrivateKey)
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to decode private key: %v", err))
		return
	}
	clientID := os.Getenv("GITHUB_APP_CLIENT_ID")
	if clientID == "" {
		utils.HandleError(r, i, "❌ GitHub App client ID not configured")
		return
	}
	token, err := utils.GithubAuth(clientID, privateKeyPEM)
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to authenticate with GitHub: %v", err))
		return
	}
	owner := "7cav"
	repo := "adr"
	workflow := "dev_deploy.yml"
	ref := "main"
	err = utils.CheckGithubBranchExists(branch, token, owner, repo)
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Branch does not exist: %v", err))
		return
	}
	err = utils.TriggerGithubDeployment(branch, token, owner, repo, workflow, ref)
	var response string
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to trigger Apps Beta deployment: %v", err))
		return
	} else {
		utils.Info("Deployment triggered successfully", "command", "AppsBetaDeploy")
		_, invokerID := interactionUsernameAndID(i)
		response = fmt.Sprintf("✅ Apps Beta deployment started for branch `%s` by <@%s> \nCheck status at: https://github.com/7cav/adr/actions/workflows/dev_deploy.yml", branch, invokerID)
	}

	if err = r.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: response,
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Error sending interaction response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "AppsBetaDeploy")
}
