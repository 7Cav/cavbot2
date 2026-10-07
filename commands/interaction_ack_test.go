package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/getsentry/sentry-go"
)

// recordSentry binds a Sentry client that records its events, and unbinds
// it after the test.
func recordSentry(t *testing.T) *telemetrySentryTransport {
	t.Helper()
	rec := &telemetrySentryTransport{}
	if err := sentry.Init(sentry.ClientOptions{Dsn: "https://test@example.com/1", Transport: rec}); err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return rec
}

// snowflakeAt builds an interaction ID that Discord would have minted at t.
func snowflakeAt(t time.Time) string {
	const discordEpochMs = 1420070400000
	return strconv.FormatInt((t.UnixMilli()-discordEpochMs)<<22, 10)
}

// extraMs reads a millisecond count off an event's extra context.
func extraMs(e *sentry.Event, key string) (int64, bool) {
	switch v := e.Contexts["extra"][key].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// When Discord answers a command's acknowledgement with 10062 Unknown
// interaction, the interaction is gone and nothing more can reach the
// member. Sentry gets one event that names the command, and the /foxhole
// subcommand, and says how long the acknowledgement took and how old the
// interaction was when the bot sent it, so a late interaction can be told
// from a slow answer. The event reports Discord's own error, so it groups
// by Discord's error type. The bot sends nothing further on the
// interaction. Every registered command keeps to this, and so does a press
// on a lock notice button.
func TestMissedAcknowledgementIsReportedOnceWithItsTimings(t *testing.T) {
	const (
		ackDelay = 20 * time.Millisecond
		age      = 5 * time.Second
	)
	type missedRun struct {
		label, command, subcommand string
		handler                    CommandHandler
		interaction                func(t *testing.T) *discordgo.InteractionCreate
	}
	var runs []missedRun
	for _, run := range registeredRuns(t) {
		runs = append(runs, missedRun{run.label, run.command, run.subcommand, run.handler,
			func(*testing.T) *discordgo.InteractionCreate { return run.interaction(time.Now().Add(-age)) }})
	}
	// main.go's dispatcher routes a press to /voice-lock's handler by its
	// CustomID (ADR 0007).
	lockHandler, _ := NewRegistry(nil, nil).GetHandler(voiceLockCommandName)
	runs = append(runs, missedRun{"lock notice press", voiceLockCommandName, "", lockHandler,
		func(t *testing.T) *discordgo.InteractionCreate {
			customID, err := lockNoticeCustomID(lockNoticeUnlock, "chan-1")
			if err != nil {
				t.Fatal(err)
			}
			i := pressInteraction(customID, lockOwner)
			i.ID, i.AppID, i.Token = snowflakeAt(time.Now().Add(-age)), "app-1", "token-1"
			return i
		}})

	for _, tc := range runs {
		t.Run(tc.label, func(t *testing.T) {
			rec := recordSentry(t)
			// Discord answers every call on a gone interaction with 10062.
			var slow sync.Once
			api := &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
				slow.Do(func() { time.Sleep(ackDelay) })
				return http.StatusNotFound, []byte(fmt.Sprintf(`{"message": "Unknown interaction", "code": %d}`, discordgo.ErrCodeUnknownInteraction))
			}}
			i := tc.interaction(t)

			tc.handler(stateSession(t, api), i)

			if reqs := onInteraction(api, i); len(reqs) != 1 {
				t.Errorf("requests on the interaction = %+v, want only the refused acknowledgement", reqs)
			}
			events := rec.Events()
			if len(events) != 1 {
				var got []string
				for _, e := range events {
					got = append(got, e.Tags["message"])
				}
				t.Fatalf("Sentry events = %q, want one", got)
			}
			e := events[0]
			if e.Tags["command"] != tc.command {
				t.Errorf("event command = %q, want %q", e.Tags["command"], tc.command)
			}
			if tc.subcommand != "" && e.Contexts["extra"]["subcommand"] != tc.subcommand {
				t.Errorf("event subcommand = %v, want %q", e.Contexts["extra"]["subcommand"], tc.subcommand)
			}
			if n := len(e.Exception); n == 0 || e.Exception[n-1].Type != "*discordgo.RESTError" {
				t.Errorf("event exceptions = %+v, want Discord's own *discordgo.RESTError outermost", e.Exception)
			}
			if took, ok := extraMs(e, "ack_ms"); !ok || took < ackDelay.Milliseconds() || took >= age.Milliseconds() {
				t.Errorf("ack_ms = %v, want how long the acknowledgement took (>= %d)", e.Contexts["extra"]["ack_ms"], ackDelay.Milliseconds())
			}
			if old, ok := extraMs(e, "interaction_age_ms"); !ok || old < age.Milliseconds() || old >= age.Milliseconds()+60_000 {
				t.Errorf("interaction_age_ms = %v, want the interaction's age when acknowledged (>= %d)", e.Contexts["extra"]["interaction_age_ms"], age.Milliseconds())
			}
		})
	}
}

// ackRunOptions gives each registered command the options that carry a
// run of it to its acknowledgement. A command that spells its subcommands
// as the choices of a "command" option runs once per choice, and
// registeredRuns adds that option itself.
func ackRunOptions() map[string][]*discordgo.ApplicationCommandInteractionDataOption {
	foxhole := []*discordgo.ApplicationCommandInteractionDataOption{
		stringOption("flag", "internal"), stringOption("discordname", "someone"),
	}
	roster := []*discordgo.ApplicationCommandInteractionDataOption{
		stringOption("unit", validatedInternalUnits[0].Value),
	}
	return map[string][]*discordgo.ApplicationCommandInteractionDataOption{
		"milpac":                   {userOption("user", "123456789012345678")},
		"foxhole":                  foxhole,
		"warden":                   foxhole,
		"enlist":                   nil,
		"foxhole-bulkadd-internal": roster,
		"warden-bulkadd-internal":  roster,
		"zulu":                     nil,
		"s6-it-check":              nil,
		"awol":                     {stringOption("position", "S1")},
		"loa":                      {stringOption("position", "S1")},
		"afsm":                     {stringOption("department", "S1")},
		"gamertag_search":          {stringOption("gamertag", "someone")},
		"s3aar":                    nil,
		"helpline":                 nil,
		voiceRenameCommandName:     {stringOption("name", "Alpha")},
		voiceLockCommandName:       nil,
		voiceUnlockCommandName:     nil,
	}
}

// registeredRun is one run of a registered command, through the handler
// main.go's dispatcher reaches by the command's name.
type registeredRun struct {
	label, command, subcommand string
	handler                    CommandHandler
	options                    []*discordgo.ApplicationCommandInteractionDataOption
}

// registeredRuns walks NewRegistry: every registered command, once per
// subcommand for a command that has them. A command with no entry in
// ackRunOptions fails t, so no command joins the registry without a case.
func registeredRuns(t *testing.T) []registeredRun {
	t.Helper()
	reg := NewRegistry(nil, nil)
	cases := ackRunOptions()
	var runs []registeredRun
	for _, def := range reg.GetCommands() {
		opts, ok := cases[def.Name]
		if !ok {
			t.Errorf("no case for /%s: give ackRunOptions the options that carry a run of it to its acknowledgement", def.Name)
			continue
		}
		handler, _ := reg.GetHandler(def.Name)
		subcommands := typedSubcommands(def)
		if len(subcommands) == 0 {
			runs = append(runs, registeredRun{label: "/" + def.Name, command: def.Name, handler: handler, options: opts})
			continue
		}
		for _, sub := range subcommands {
			runs = append(runs, registeredRun{
				label: "/" + def.Name + " " + sub, command: def.Name, subcommand: sub, handler: handler,
				options: append([]*discordgo.ApplicationCommandInteractionDataOption{stringOption("command", sub)}, opts...),
			})
		}
	}
	return runs
}

// typedSubcommands returns the choices of a command's "command" option,
// the way /foxhole spells its subcommands.
func typedSubcommands(def *discordgo.ApplicationCommand) []string {
	for _, opt := range def.Options {
		if opt.Name != "command" {
			continue
		}
		var subcommands []string
		for _, choice := range opt.Choices {
			subcommands = append(subcommands, fmt.Sprint(choice.Value))
		}
		return subcommands
	}
	return nil
}

// interaction is the run's slash command as Discord delivers it, minted at
// created.
func (run registeredRun) interaction(created time.Time) *discordgo.InteractionCreate {
	i := sessionSlash(run.command, run.options...)
	i.ID = snowflakeAt(created)
	return i
}

// onInteraction returns the requests that reached Discord on i: its
// responses, edits and follow-ups, which all carry its token.
func onInteraction(api *fakeDiscordAPI, i *discordgo.InteractionCreate) []apiRequest {
	var reqs []apiRequest
	for _, req := range api.received() {
		if strings.Contains(req.path, i.Token) {
			reqs = append(reqs, req)
		}
	}
	return reqs
}

// isResponse reports whether req is an interaction response, the call
// that acknowledges an interaction or replies to it first.
func isResponse(req apiRequest) bool {
	return req.method == http.MethodPost && strings.HasSuffix(req.path, "/callback")
}

// When Discord refuses a command's acknowledgement with anything but 10062
// Unknown interaction, the member gets ackFailedReply, the answer every
// command gives then. None of Discord's response body reaches the member;
// it stays in the log.
func TestRefusedAcknowledgementGetsTheFixedReply(t *testing.T) {
	for _, run := range registeredRuns(t) {
		t.Run(run.label, func(t *testing.T) {
			logs := captureLogs(t)
			var refuse sync.Once
			api := &fakeDiscordAPI{answer: func(r *http.Request, _ []byte) (int, []byte) {
				if !strings.HasSuffix(r.URL.Path, "/callback") {
					return http.StatusOK, []byte(`{"id":"message-1"}`)
				}
				status := http.StatusNoContent
				refuse.Do(func() { status = http.StatusInternalServerError })
				if status == http.StatusNoContent {
					return status, nil
				}
				return status, []byte(fmt.Sprintf(`{"message": %q, "code": 0}`, rawBodyMarker))
			}}
			i := run.interaction(time.Now())

			run.handler(stateSession(t, api), i)

			reqs := onInteraction(api, i)
			if len(reqs) < 2 || !isResponse(reqs[1]) {
				t.Fatalf("requests on the interaction = %+v, want the refused acknowledgement, then a reply", reqs)
			}
			var reply struct {
				Data struct {
					Content string `json:"content"`
				} `json:"data"`
			}
			if err := json.Unmarshal(reqs[1].body, &reply); err != nil {
				t.Fatalf("reply body %s: %v", reqs[1].body, err)
			}
			if reply.Data.Content != ackFailedReply {
				t.Errorf("reply = %q, want %q", reply.Data.Content, ackFailedReply)
			}
			for _, req := range reqs {
				if strings.Contains(string(req.body), rawBodyMarker) {
					t.Errorf("%s %s carries Discord's response body: %s", req.method, req.path, req.body)
				}
			}
			if !strings.Contains(logs.String(), rawBodyMarker) {
				t.Error("the log lacks Discord's response body")
			}
		})
	}
}
