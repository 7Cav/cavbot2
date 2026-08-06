package commands

import (
	"bytes"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// syncBuffer is a concurrency-safe sink for the package-wide test logger. The
// mutex is load-bearing: TestRunJoinerReportSchedulerLoop_PanicIsRecovered
// deliberately leaves its scheduler goroutine running past the end of the test
// (see the comment there), and that goroutine keeps calling utils.Info, so the
// sink has a live writer no test controls.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// testLogs is where every log line in this package's tests lands. Helpers that
// assert on log output (captureWarnLogs, captureTelemetryLines) read it rather
// than installing a logger of their own.
var testLogs = &syncBuffer{}

// TestMain initializes the utils package Logger so handlers don't panic on a
// nil *slog.Logger when they call utils.Info/Warn/Debug.
//
// This is the ONLY assignment to utils.Logger in the package, and it must stay
// that way. utils.Logger is an unsynchronized package global; the leaked
// scheduler goroutine described on syncBuffer reads it for the rest of the
// run, so a later write — even the restore half of a swap-and-restore helper —
// is a data race that -race will fail the build on. Assigning once here, before
// m.Run starts anything, happens-before every read.
//
// Level is INFO because that is the most verbose level any assertion needs
// (the telemetry line is INFO, the s3aar enrichment warnings are WARN).
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(testLogs, &slog.HandlerOptions{Level: slog.LevelInfo}))
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

func stringOption(name, value string) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{
		Name:  name,
		Type:  discordgo.ApplicationCommandOptionString,
		Value: value,
	}
}
