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

	"github.com/7cav/cavbot2/utils"
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
// interaction. Every registered command keeps to this when its first
// response is its normal reply, and so do the refusals refusalRuns lists,
// an error reply sent through replyError, and a press on a lock notice
// button.
func TestMissedAcknowledgementIsReportedOnceWithItsTimings(t *testing.T) {
	const (
		ackDelay = 20 * time.Millisecond
		age      = 5 * time.Second
	)
	customID, err := lockNoticeCustomID(lockNoticeUnlock, "chan-1")
	if err != nil {
		t.Fatal(err)
	}
	// main.go's dispatcher routes a press to /voice-lock's handler by its
	// CustomID (ADR 0007).
	lockHandler, _ := NewRegistry(nil, nil, nil).GetHandler(voiceLockCommandName)
	runs := append(registeredRuns(t), registeredRun{
		label: "lock notice press", command: voiceLockCommandName, handler: lockHandler,
		interaction: func(created time.Time) *discordgo.InteractionCreate {
			i := pressInteraction(customID, lockOwner)
			i.ID, i.AppID, i.Token = snowflakeAt(created), "app-1", "token-1"
			return i
		},
	})
	runs = append(runs, refusalRuns(t)...)
	// No command sends replyError as its first response today. The run
	// holds the helper to the same report for one that does.
	runs = append(runs, registeredRun{
		label: "replyError as the first response", command: "milpac",
		handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			replyError(utils.NewSessionResponder(s), i, "❌ That didn't work.")
		},
		interaction: slashAt("milpac"),
	})

	for _, tc := range runs {
		t.Run(tc.label, func(t *testing.T) {
			rec := recordSentry(t)
			// Discord answers every call on a gone interaction with 10062.
			var slow sync.Once
			api := &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
				slow.Do(func() { time.Sleep(ackDelay) })
				return http.StatusNotFound, []byte(fmt.Sprintf(`{"message": "Unknown interaction", "code": %d}`, discordgo.ErrCodeUnknownInteraction))
			}}
			i := tc.interaction(time.Now().Add(-age))

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
			// Every missed slash command groups as one issue. A lock notice
			// press has an event of its own.
			if i.Type == discordgo.InteractionApplicationCommand && e.Tags["message"] != missedAckMessage {
				t.Errorf("event message = %q, want %q, so every missed slash command groups as one issue", e.Tags["message"], missedAckMessage)
			}
			if e.Tags["command"] != tc.command {
				t.Errorf("event command = %q, want %q", e.Tags["command"], tc.command)
			}
			if tc.subcommand != "" && e.Contexts["extra"]["subcommand"] != tc.subcommand {
				t.Errorf("event subcommand = %v, want %q", e.Contexts["extra"]["subcommand"], tc.subcommand)
			}
			reportsDiscordError(t, e)
			if took, ok := extraMs(e, "ack_ms"); !ok || took < ackDelay.Milliseconds() || took >= age.Milliseconds() {
				t.Errorf("ack_ms = %v, want how long the acknowledgement took (>= %d)", e.Contexts["extra"]["ack_ms"], ackDelay.Milliseconds())
			}
			if old, ok := extraMs(e, "interaction_age_ms"); !ok || old < age.Milliseconds() || old >= age.Milliseconds()+60_000 {
				t.Errorf("interaction_age_ms = %v, want the interaction's age when acknowledged (>= %d)", e.Contexts["extra"]["interaction_age_ms"], age.Milliseconds())
			}
		})
	}
}

// reportsDiscordError fails t unless e reports Discord's own error, so it
// groups in Sentry by Discord's error type.
func reportsDiscordError(t *testing.T, e *sentry.Event) {
	t.Helper()
	if n := len(e.Exception); n == 0 || e.Exception[n-1].Type != "*discordgo.RESTError" {
		t.Errorf("event exceptions = %+v, want Discord's own *discordgo.RESTError outermost", e.Exception)
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
		recordCommandName:          {{Name: "stop", Type: discordgo.ApplicationCommandOptionSubCommand}},
	}
}

// refusalRuns are the runs of registered commands whose first response is
// a refusal, one per refusal. The commands run beside a Foxhole page action
// held mid-run for the rest of t.
func refusalRuns(t *testing.T) []registeredRun {
	t.Helper()
	fx, hold := newPageRuntime(t)
	holdPagePurge(t, fx, hold)
	reg := NewRegistry(nil, fx, nil)
	// run is a refusal of command's run with opts, which got as far as
	// subcommand when the command has one.
	run := func(label, command, subcommand string, opts ...*discordgo.ApplicationCommandInteractionDataOption) registeredRun {
		handler, _ := reg.GetHandler(command)
		return registeredRun{label: "/" + command + " " + label, command: command, subcommand: subcommand,
			handler: handler, interaction: slashAt(command, opts...)}
	}
	runs := []registeredRun{
		run("date without a time", "zulu", "", stringOption("date", "01MAY26")),
		run("bad date", "zulu", "", stringOption("time", "2300"), stringOption("date", "BADDATE")),
		run("bad time", "zulu", "", stringOption("time", "9999")),
	}
	for _, name := range []string{"foxhole", "warden"} {
		dm := run("outside a guild", name, "",
			stringOption("command", "add"), stringOption("flag", "internal"), stringOption("discordname", "someone"))
		dm.interaction = outsideGuild(dm.interaction)
		runs = append(runs, dm,
			run("bad command", name, "",
				stringOption("command", "nonsense"), stringOption("flag", "internal"), stringOption("discordname", "someone")),
			run("bad flag", name, "add",
				stringOption("command", "add"), stringOption("flag", "nonsense"), stringOption("discordname", "someone")),
			run("no discordname", name, "add",
				stringOption("command", "add"), stringOption("flag", "internal")),
			run("during a page action", name, "add",
				stringOption("command", "add"), stringOption("flag", "internal"), stringOption("discordname", "someone")),
		)
	}
	for _, name := range []string{"foxhole-bulkadd-internal", "warden-bulkadd-internal"} {
		dm := run("outside a guild", name, "", stringOption("unit", validatedInternalUnits[0].Value))
		dm.interaction = outsideGuild(dm.interaction)
		runs = append(runs, dm,
			run("no unit", name, ""),
			run("unknown unit", name, "", stringOption("unit", "not-a-unit")),
			run("during a page action", name, "", stringOption("unit", validatedInternalUnits[0].Value)),
		)
	}
	return runs
}

// outsideGuild is build's interaction sent outside a guild, from a DM.
func outsideGuild(build func(time.Time) *discordgo.InteractionCreate) func(time.Time) *discordgo.InteractionCreate {
	return func(created time.Time) *discordgo.InteractionCreate {
		i := build(created)
		i.GuildID, i.User, i.Member = "", i.Member.User, nil
		return i
	}
}

// registeredRun is one run of a registered command, through the handler
// main.go's dispatcher reaches by the command's name.
type registeredRun struct {
	label, command, subcommand string
	handler                    CommandHandler
	// interaction builds the run's interaction as Discord delivers it,
	// minted at created.
	interaction func(created time.Time) *discordgo.InteractionCreate
}

// registeredRuns walks NewRegistry: every registered command, once per
// subcommand for a command that has them. A command with no entry in
// ackRunOptions fails t, so no command joins the registry without a case.
func registeredRuns(t *testing.T) []registeredRun {
	t.Helper()
	reg := NewRegistry(nil, nil, nil)
	optionsByCommand := ackRunOptions()
	var runs []registeredRun
	for _, def := range reg.GetCommands() {
		opts, ok := optionsByCommand[def.Name]
		if !ok {
			t.Errorf("no case for /%s: give ackRunOptions the options that carry a run of it to its acknowledgement", def.Name)
			continue
		}
		handler, _ := reg.GetHandler(def.Name)
		subcommands := typedSubcommands(def)
		if len(subcommands) == 0 {
			runs = append(runs, registeredRun{label: "/" + def.Name, command: def.Name, handler: handler,
				interaction: slashAt(def.Name, opts...)})
			continue
		}
		for _, sub := range subcommands {
			runs = append(runs, registeredRun{
				label: "/" + def.Name + " " + sub, command: def.Name, subcommand: sub, handler: handler,
				interaction: slashAt(def.Name, append([]*discordgo.ApplicationCommandInteractionDataOption{stringOption("command", sub)}, opts...)...),
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

// slashAt builds name's slash command with opts as Discord delivers it,
// minted at created.
func slashAt(name string, opts ...*discordgo.ApplicationCommandInteractionDataOption) func(created time.Time) *discordgo.InteractionCreate {
	return func(created time.Time) *discordgo.InteractionCreate {
		i := sessionSlash(name, opts...)
		i.ID = snowflakeAt(created)
		return i
	}
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

// When Discord rejects a refusal sent as a command's first reply with
// anything but 10062 Unknown interaction, Sentry gets the event for an
// error reply that never arrived, not a missed acknowledgement, and it
// carries Discord's own error. No second reply follows, so ackFailedReply
// never takes the place of the refusal.
func TestRejectedRefusalIsReportedAsALostErrorReply(t *testing.T) {
	rec := recordSentry(t)
	api := &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
		return http.StatusInternalServerError, []byte(`{"message": "Internal Server Error", "code": 0}`)
	}}
	handler, _ := NewRegistry(nil, nil, nil).GetHandler("zulu")
	i := slashAt("zulu", stringOption("time", "9999"))(time.Now())

	handler(stateSession(t, api), i)

	var responses []apiRequest
	for _, req := range onInteraction(api, i) {
		if isResponse(req) {
			responses = append(responses, req)
		}
	}
	if len(responses) != 1 {
		t.Errorf("interaction responses = %+v, want only the rejected refusal", responses)
	}
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("Sentry events = %d, want one", len(events))
	}
	e := events[0]
	if e.Tags["message"] == missedAckMessage {
		t.Errorf("event message = %q, want the lost error reply's, not a missed acknowledgement", e.Tags["message"])
	}
	reportsDiscordError(t, e)
}
