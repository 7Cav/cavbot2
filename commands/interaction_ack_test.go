package commands

import (
	"net/http"
	"strconv"
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

// slowAck holds the first response back for delay before Discord answers
// it, as a slow acknowledgement would.
type slowAck struct {
	*fakeResponder
	delay time.Duration
	once  sync.Once
}

func (s *slowAck) InteractionRespond(i *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	s.once.Do(func() { time.Sleep(s.delay) })
	return s.fakeResponder.InteractionRespond(i, resp)
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
// member. Sentry gets one event that names the command, and the /warden
// subcommand, and says how long the acknowledgement took and how old the
// interaction was when the bot sent it, so a late interaction can be told
// from a slow answer. The event reports Discord's own error, so it groups
// by Discord's error type. The bot sends nothing further on the
// interaction.
func TestMissedAcknowledgementIsReportedOnceWithItsTimings(t *testing.T) {
	const (
		ackDelay = 20 * time.Millisecond
		age      = 5 * time.Second
	)
	slash := func(name string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
		i := fakeAppCommandInteraction(opts...)
		i.ID = snowflakeAt(time.Now().Add(-age))
		i.GuildID = "guild-1"
		i.Data = discordgo.ApplicationCommandInteractionData{Name: name, Options: opts}
		return i
	}
	warden := func(subcommand string) *discordgo.InteractionCreate {
		return slash("warden",
			stringOption("command", subcommand), stringOption("flag", "internal"), stringOption("discordname", "someone"))
	}
	press := func(t *testing.T) *discordgo.InteractionCreate {
		customID, err := lockNoticeCustomID(lockNoticeUnlock, "chan-1")
		if err != nil {
			t.Fatal(err)
		}
		i := pressInteraction(customID, lockOwner)
		i.ID = snowflakeAt(time.Now().Add(-age))
		return i
	}

	cases := []struct {
		name       string
		command    string
		subcommand string
		run        func(t *testing.T, r *slowAck)
	}{
		{"/warden add", "warden", "add", func(_ *testing.T, r *slowAck) { runWarden(r, nil, warden("add")) }},
		{"/warden remove", "warden", "remove", func(_ *testing.T, r *slowAck) { runWarden(r, nil, warden("remove")) }},
		{"/warden bulkadd", "warden", "bulkadd", func(_ *testing.T, r *slowAck) { runWarden(r, nil, warden("bulkadd")) }},
		{"/warden purge", "warden", "purge", func(_ *testing.T, r *slowAck) { runWarden(r, nil, warden("purge")) }},
		{"/warden-bulkadd-internal", "warden-bulkadd-internal", "", func(_ *testing.T, r *slowAck) {
			runWardenBulkAddInternal(r, nil, slash("warden-bulkadd-internal", stringOption("unit", wardenInternalUnits[0].value)))
		}},
		{"/voice-rename", voiceRenameCommandName, "", func(_ *testing.T, r *slowAck) {
			runVoiceRename(r, nil, slash(voiceRenameCommandName, stringOption("name", "Alpha")))
		}},
		{"/voice-lock", voiceLockCommandName, "", func(_ *testing.T, r *slowAck) { runVoiceLock(r, nil, slash(voiceLockCommandName)) }},
		{"/voice-unlock", voiceUnlockCommandName, "", func(_ *testing.T, r *slowAck) { runVoiceUnlock(r, nil, slash(voiceUnlockCommandName)) }},
		{"/s3aar", "s3aar", "", func(_ *testing.T, r *slowAck) { runS3aar(r, slash("s3aar")) }},
		{"lock notice press", voiceLockCommandName, "", func(t *testing.T, r *slowAck) { runVoiceLock(r, nil, press(t)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := recordSentry(t)
			// Discord answers every call on a gone interaction with 10062.
			gone := func() []error {
				err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownInteraction, "Unknown interaction")
				return []error{err, err, err}
			}
			r := &slowAck{fakeResponder: &fakeResponder{RespondErrs: gone(), EditErrs: gone(), FollowupErrs: gone()}, delay: ackDelay}

			tc.run(t, r)

			if calls := r.Calls(); len(calls) != 1 {
				t.Errorf("responder calls = %+v, want only the refused acknowledgement", calls)
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
