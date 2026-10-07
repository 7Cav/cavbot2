package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// A Foxhole action started on the Foxhole page never runs alongside a
// role-changing /foxhole command (spec #434, "Running a Foxhole action").
// These tests run a real /foxhole add through the command registry over the
// panel's Foxhole runtime, the way main.go wires it, on a Discord session
// whose REST API is faked, and hold it inside its role grant.

// commandInvoker is the Discord member who runs the held command.
var commandInvoker = &discordgo.User{ID: "200000000000000009", Username: "invoker.k"}

// commandAPI is Discord's REST API as a /foxhole add run over a real
// session reaches it: the guild holds the Foxhole roles and Doe, the member
// the command names. It holds each member role grant inside the request
// until the test lets it answer.
type commandAPI struct {
	t       *testing.T
	entered chan struct{}
	release chan struct{}
}

func (a *commandAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	status, reply := http.StatusNoContent, []byte(nil)
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/roles"):
		status, reply = http.StatusOK, a.encode(foxholeGuildRoles)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/members/"):
		status, reply = http.StatusOK, a.encode(&discordgo.Member{User: &discordgo.User{ID: memberDoe.ID, Username: memberDoe.Username}})
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/roles/"):
		a.entered <- struct{}{}
		<-a.release
	case r.Method == http.MethodPatch:
		status, reply = http.StatusOK, []byte(`{"id":"message-1"}`)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(reply)), Request: r}, nil
}

func (a *commandAPI) encode(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

// heldCommand is a /foxhole add held inside its role grant, and when it was
// dispatched: between before and after.
type heldCommand struct {
	before, after time.Time
	release       func()
}

// holdFoxholeAdd dispatches /foxhole add of Doe to Internal, run by
// commandInvoker, through the command registry over the panel's Foxhole
// runtime, and returns once its role grant reaches Discord, held there. The
// test's end lets it answer.
func holdFoxholeAdd(t *testing.T, w *testWorld) heldCommand {
	t.Helper()
	api := &commandAPI{t: t, entered: make(chan struct{}, 1), release: make(chan struct{})}
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client = &http.Client{Transport: api}
	handler, ok := commands.NewRegistry(nil, w.foxhole).GetHandler("foxhole")
	if !ok {
		t.Fatal("no /foxhole handler registered")
	}
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "1", AppID: "app-1", Token: "token-1", Type: discordgo.InteractionApplicationCommand, GuildID: testGuildID,
		Member: &discordgo.Member{User: commandInvoker},
		Data: discordgo.ApplicationCommandInteractionData{Name: "foxhole", Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{Name: "command", Type: discordgo.ApplicationCommandOptionString, Value: "add"},
			{Name: "flag", Type: discordgo.ApplicationCommandOptionString, Value: "internal"},
			{Name: "discordname", Type: discordgo.ApplicationCommandOptionString, Value: memberDoe.ID},
		}},
	}}
	held := heldCommand{before: time.Now()}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		handler(session, interaction)
	}()
	select {
	case <-api.entered:
	case <-time.After(hangLimit):
		t.Fatal("the /foxhole add never reached its role grant")
	}
	held.after = time.Now()
	var once bool
	held.release = func() {
		if once {
			return
		}
		once = true
		close(api.release)
		<-ended
	}
	t.Cleanup(held.release)
	return held
}

// commandBlock returns the block naming the command running, and fails the
// test when the page shows none.
func commandBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", "command-running")
	if block == nil {
		t.Fatal("the page shows no block for the command running")
	}
	return block
}

// While a role-changing command runs, the Foxhole page names it as typed,
// the Discord member who ran it and when it started, with no count, since a
// command reports no progress, and disables the controls that would start a
// page action.
func TestFoxholePageNamesTheCommandRunning(t *testing.T) {
	w := newFoxholeWorld(t)
	held := holdFoxholeAdd(t, w)

	doc := parseHTML(t, w.b.get(foxholePath))

	block := commandBlock(t, doc)
	if got := fieldText(t, block, "command"); got != "/foxhole add" {
		t.Errorf("the block names the command %q, want /foxhole add", got)
	}
	if by := findElement(block, "", "data-member", commandInvoker.ID); by == nil {
		t.Errorf("the block doesn't name %s, the member who ran the command", commandInvoker.ID)
	}
	started := findElement(block, "time", "data-field", "started")
	if started == nil {
		t.Fatal("the block doesn't say when the command started")
	}
	raw, _ := attrValue(started, "datetime")
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || at.Before(held.before.Truncate(time.Second)) || at.After(held.after) {
		t.Errorf("the block says the command started at %q, want between %s and %s", raw, held.before, held.after)
	}
	for _, count := range []string{"done", "total"} {
		if findElement(block, "", "data-field", count) != nil {
			t.Errorf("the block shows a %s count, want none", count)
		}
	}
	if findElement(doc, "", "data-field", "progress") != nil {
		t.Error("the page shows a progress block while only a command runs")
	}
	if button := findElement(purgeForm(t, doc), "button", "", ""); button == nil || !disabled(button) {
		t.Error("the purge form's button is enabled while a command runs")
	}
}

// The server refuses a page action posted while a role-changing command
// runs, so a page without script stays safe: a purge confirmed from a page
// loaded before the command started is refused, and changes nothing.
func TestPurgeConfirmedWhileACommandRunsIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both")))
	holdFoxholeAdd(t, w)

	refused := parseHTML(t, confirmPurge(t, w.b, context.Background(), confirm))

	if findLive(refused, "", "data-error", "action-running") == nil {
		t.Error("the page doesn't say the purge was refused for the command running")
	}
	if entries := changeEntries(t, refused, "purge"); len(entries) != 0 {
		t.Errorf("the change log holds %d purge entries, want none", len(entries))
	}
	if writes := w.discord.roleChanges(); len(writes) != 0 {
		t.Errorf("Discord got role changes %+v, want none", writes)
	}
}
