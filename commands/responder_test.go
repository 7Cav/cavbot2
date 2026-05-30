package commands

import (
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// TestMain initializes the utils package Logger so handlers don't panic on a
// nil *slog.Logger when they call utils.Info/Warn/Debug.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	os.Exit(m.Run())
}

type recordedCall struct {
	Method   string
	Response *discordgo.InteractionResponse
	Edit     *discordgo.WebhookEdit
	Wait     bool
	Params   *discordgo.WebhookParams
}

// fakeResponder mirrors the in-utils fake; duplicated because Go test
// helpers can't be shared across packages. See utils/setup_test.go.
type fakeResponder struct {
	mu    sync.Mutex
	calls []recordedCall

	RespondErrs  []error
	EditErrs     []error
	FollowupErrs []error
}

func (f *fakeResponder) InteractionRespond(_ *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Method: "Respond", Response: resp})
	return popErr(&f.RespondErrs)
}

func (f *fakeResponder) InteractionResponseEdit(_ *discordgo.Interaction, edit *discordgo.WebhookEdit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Method: "Edit", Edit: edit})
	return popErr(&f.EditErrs)
}

func (f *fakeResponder) FollowupMessageCreate(_ *discordgo.Interaction, wait bool, params *discordgo.WebhookParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Method: "Followup", Wait: wait, Params: params})
	return popErr(&f.FollowupErrs)
}

func (f *fakeResponder) Calls() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCall(nil), f.calls...)
}

func popErr(queue *[]error) error {
	if len(*queue) == 0 {
		return nil
	}
	err := (*queue)[0]
	*queue = (*queue)[1:]
	return err
}

// fakeAppCommandInteraction builds the minimum *InteractionCreate that a
// slash-command handler reads: type, options, and a Member with a User.
func fakeAppCommandInteraction(opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Options: opts,
			},
			Member: &discordgo.Member{
				User: &discordgo.User{ID: "999", Username: "tester"},
			},
		},
	}
}

// fakeMessageComponentInteraction builds the minimum *InteractionCreate that a
// component (button/select) handler reads: type, a MessageComponentInteractionData
// carrying the CustomID, and a Member with a User. Mirrors
// fakeAppCommandInteraction for the component-interaction (Pattern D) path.
func fakeMessageComponentInteraction(customID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent,
			Data: discordgo.MessageComponentInteractionData{
				CustomID:      customID,
				ComponentType: discordgo.ButtonComponent,
			},
			Member: &discordgo.Member{
				User: &discordgo.User{ID: "999", Username: "tester"},
			},
		},
	}
}

func stringOption(name, value string) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{
		Name:  name,
		Type:  discordgo.ApplicationCommandOptionString,
		Value: value,
	}
}
