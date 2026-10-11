package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
)

// The recording status endpoint (#394), which the deploy workflow polls
// before it deploys, with no panel session. The address is the literal the
// workflow polls, not the production constant, so renaming the route turns
// this red until the workflow follows.
const deployStatusAddress = "/recording-status"

// recordingFlag is the flag the recording status endpoint answers, and the
// raw body it came in. Anything but a 200 whose body decodes with the flag
// fails the test.
func recordingFlag(t *testing.T, b *browser) (bool, string) {
	t.Helper()
	res := b.get(deployStatusAddress)
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", deployStatusAddress, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d, want 200", deployStatusAddress, res.StatusCode)
	}
	var answer struct {
		Recording *bool `json:"recording"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Recording == nil {
		t.Fatalf("%s answered %q, want a recording flag", deployStatusAddress, body)
	}
	return *answer.Recording, string(body)
}

// With no panel session, the status endpoint says no recording runs before
// the first one, that one runs while it does, and that none runs once it
// stops. While it runs, the answer names neither its channel nor its
// starter.
func TestRecordingStatusFollowsWhetherARecordingRuns(t *testing.T) {
	w := newTestWorld(t)
	starter := commands.ListedMember{ID: "discord-a", Username: "able", Nick: "Sgt Able"}
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember, starter}})

	if running, _ := recordingFlag(t, w.b); running {
		t.Errorf("before any recording the flag is true, want false")
	}

	w.startRecording(t, starter.ID)
	running, body := recordingFlag(t, w.b)
	if !running {
		t.Errorf("while a recording runs the flag is false, want true")
	}
	for _, leak := range []string{"Squad Join", testRecordChannel, starter.ID, starter.DisplayName()} {
		if strings.Contains(body, leak) {
			t.Errorf("while a recording runs the answer %q holds %q", body, leak)
		}
	}

	w.stopRecording(t, starter.ID)
	if running, _ := recordingFlag(t, w.b); running {
		t.Errorf("after the recording stops the flag is true, want false")
	}
}
