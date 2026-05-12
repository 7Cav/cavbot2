package utils

import "github.com/bwmarrin/discordgo"

// InteractionResponder is the subset of *discordgo.Session that command
// handlers use to respond to interactions. Tests substitute a fake that
// records calls and lets the caller inject errors per-call.
//
// The two SDK methods that return (*Message, error) return only error here;
// no caller in this repo uses the *Message value (verified during the Phase 2
// audit: all are `_, err = ...`).
type InteractionResponder interface {
	InteractionRespond(i *discordgo.Interaction, resp *discordgo.InteractionResponse) error
	InteractionResponseEdit(i *discordgo.Interaction, edit *discordgo.WebhookEdit) error
	FollowupMessageCreate(i *discordgo.Interaction, wait bool, params *discordgo.WebhookParams) error
}

// sessionResponder adapts *discordgo.Session to InteractionResponder, hiding
// the unused *Message return values from the SDK.
type sessionResponder struct {
	s *discordgo.Session
}

// NewSessionResponder wraps a real Discord session for production use.
func NewSessionResponder(s *discordgo.Session) InteractionResponder {
	return &sessionResponder{s: s}
}

func (a *sessionResponder) InteractionRespond(i *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	return a.s.InteractionRespond(i, resp)
}

func (a *sessionResponder) InteractionResponseEdit(i *discordgo.Interaction, edit *discordgo.WebhookEdit) error {
	_, err := a.s.InteractionResponseEdit(i, edit)
	return err
}

func (a *sessionResponder) FollowupMessageCreate(i *discordgo.Interaction, wait bool, params *discordgo.WebhookParams) error {
	_, err := a.s.FollowupMessageCreate(i, wait, params)
	return err
}
