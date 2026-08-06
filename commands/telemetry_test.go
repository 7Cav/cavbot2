package commands

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"github.com/getsentry/sentry-go"
	"github.com/go-logfmt/logfmt"
)

// The assertions below spell the marker and the field keys as string literals on
// purpose, never as the package constants. The whole point of these tests is to
// catch a rename of the wire contract; a test that referenced the constants
// would rename in lockstep with the emitter and stay green while the metrics
// host silently stopped matching. See docs/command-telemetry.md.

// captureTelemetryLines redirects utils.Logger at a buffer for the duration of
// the test and returns a reader that decodes emitted lines with a real logfmt
// decoder — the same encoding family the metrics host parses. Decoding rather
// than string-searching keeps the expected values independent of the emitter:
// a hand-rolled splitter would be this test's own re-reading of slog's encoder,
// so a mistake shared between the two would pass here and still fail on the host.
func captureTelemetryLines(t *testing.T) func() []map[string]string {
	t.Helper()

	var buf bytes.Buffer
	prev := utils.Logger
	utils.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t.Cleanup(func() { utils.Logger = prev })

	return func() []map[string]string {
		var records []map[string]string
		for _, raw := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if raw == "" {
				continue
			}
			record := map[string]string{}
			dec := logfmt.NewDecoder(strings.NewReader(raw))
			for dec.ScanRecord() {
				for dec.ScanKeyval() {
					record[string(dec.Key())] = string(dec.Value())
				}
			}
			if err := dec.Err(); err != nil {
				t.Fatalf("emitted line is not decodable as logfmt: %v\nline: %s", err, raw)
			}
			// Only telemetry lines are of interest; handlers emit their own
			// "🚀 Starting ..." / "✨ Done!" lines by house convention and must
			// not perturb any count or absence assertion here.
			if record["msg"] == "command_invoked" {
				records = append(records, record)
			}
		}
		return records
	}
}

// registerStub builds a one-command registry around the supplied handler and
// returns the handler as the dispatcher would fetch it, so every test exercises
// the same registration path production uses.
func registerStub(t *testing.T, name string, h CommandHandler) CommandHandler {
	t.Helper()

	reg := &Registry{}
	reg.RegisterCommands(Command{
		Definition: &discordgo.ApplicationCommand{Name: name},
		Handler:    h,
	})

	got, ok := reg.GetHandler(name)
	if !ok {
		t.Fatalf("GetHandler(%q) = _, false; want the registered handler", name)
	}
	return got
}

func slashInteraction(name string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:   discordgo.InteractionApplicationCommand,
		Member: &discordgo.Member{User: &discordgo.User{ID: "246813579", Username: "trooper.j"}},
		Data: discordgo.ApplicationCommandInteractionData{
			Name:    name,
			Options: opts,
		},
	}}
}

func TestInstrumentedHandler_EmitsContractLineForSlashCommand(t *testing.T) {
	lines := captureTelemetryLines(t)

	h := registerStub(t, "milpac", func(*discordgo.Session, *discordgo.InteractionCreate) {})
	h(nil, slashInteraction("milpac"))

	records := lines()
	if len(records) != 1 {
		t.Fatalf("got %d command_invoked lines, want 1", len(records))
	}
	rec := records[0]

	for key, want := range map[string]string{
		"command":    "milpac",
		"discord_id": "246813579",
		"username":   "trooper.j",
	} {
		if got := rec[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if _, ok := rec["latency_ms"]; !ok {
		t.Error("line carries no latency_ms key")
	}
}

// withFakeClock pins telemetryNow to a single mutable instant that the test's
// handler advances. Every read before the handler runs yields the base instant
// and every read after yields the advanced one, at any number of calls — so the
// assertion below pins the elapsed value and its unit, not how many times the
// decorator happens to read the clock.
func withFakeClock(t *testing.T, base time.Time) *time.Time {
	t.Helper()

	now := base
	prev := telemetryNow
	telemetryNow = func() time.Time { return now }
	t.Cleanup(func() { telemetryNow = prev })
	return &now
}

// The unit is contractual, not incidental: the collector divides this field by
// 1000 to feed a histogram named cavbot2_command_latency_seconds, so emitting
// seconds or microseconds here would misreport every latency panel by three
// orders of magnitude while every other assertion stayed green.
func TestInstrumentedHandler_LatencyIsElapsedWholeMilliseconds(t *testing.T) {
	lines := captureTelemetryLines(t)
	now := withFakeClock(t, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))

	h := registerStub(t, "awol", func(*discordgo.Session, *discordgo.InteractionCreate) {
		*now = now.Add(1500 * time.Millisecond)
	})
	h(nil, slashInteraction("awol"))

	records := lines()
	if len(records) != 1 {
		t.Fatalf("got %d command_invoked lines, want 1", len(records))
	}
	if got := records[0]["latency_ms"]; got != "1500" {
		t.Errorf("latency_ms = %q, want %q", got, "1500")
	}
}

// Option detail rides the line as its own keys rather than a rendered blob, so
// a LogQL drill-down can filter on `opt_flag="internal"` directly. The opt_
// prefix also keeps an option that happens to be named "command" — warden has
// one — from colliding with the contract key of the same name.
func TestInstrumentedHandler_CarriesSubcommandAndOptionDetail(t *testing.T) {
	tests := []struct {
		name    string
		command string
		options []*discordgo.ApplicationCommandInteractionDataOption
		want    map[string]string
	}{
		{
			name:    "nested subcommand carries its own options",
			command: "warden",
			options: []*discordgo.ApplicationCommandInteractionDataOption{{
				Type: discordgo.ApplicationCommandOptionSubCommand,
				Name: "add",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "flag", Value: "internal"},
				},
			}},
			want: map[string]string{
				"command":    "warden",
				"subcommand": "add",
				"opt_flag":   "internal",
			},
		},
		{
			name:    "subcommand group reaches the leaf subcommand's options",
			command: "warden",
			options: []*discordgo.ApplicationCommandInteractionDataOption{{
				Type: discordgo.ApplicationCommandOptionSubCommandGroup,
				Name: "roles",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{{
					Type: discordgo.ApplicationCommandOptionSubCommand,
					Name: "add",
					Options: []*discordgo.ApplicationCommandInteractionDataOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "flag", Value: "external"},
					},
				}},
			}},
			want: map[string]string{
				"subcommand": "roles.add",
				"opt_flag":   "external",
			},
		},
		{
			name:    "flat options with no subcommand",
			command: "zulu",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Type: discordgo.ApplicationCommandOptionString, Name: "time", Value: "2300"},
				{Type: discordgo.ApplicationCommandOptionString, Name: "date", Value: "01MAY26"},
			},
			want: map[string]string{
				"command":  "zulu",
				"opt_time": "2300",
				"opt_date": "01MAY26",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := captureTelemetryLines(t)

			h := registerStub(t, tc.command, func(*discordgo.Session, *discordgo.InteractionCreate) {})
			h(nil, slashInteraction(tc.command, tc.options...))

			records := lines()
			if len(records) != 1 {
				t.Fatalf("got %d command_invoked lines, want 1", len(records))
			}
			for key, want := range tc.want {
				if got := records[0][key]; got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// The dispatcher routes button CustomIDs back through GetHandler by their
// command-name prefix (main.go), so the decorator sees component interactions
// too. A button press continues a command that was already counted; counting it
// again would inflate every invocation total for the commands that use buttons.
func TestInstrumentedHandler_ComponentInteractionRunsButIsNotCounted(t *testing.T) {
	lines := captureTelemetryLines(t)

	ran := false
	h := registerStub(t, "warden", func(*discordgo.Session, *discordgo.InteractionCreate) { ran = true })

	h(nil, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:   discordgo.InteractionMessageComponent,
		Member: &discordgo.Member{User: &discordgo.User{ID: "246813579", Username: "trooper.j"}},
		Data:   discordgo.MessageComponentInteractionData{CustomID: "warden::purge::confirm"},
	}})

	if !ran {
		t.Error("component interaction did not reach the wrapped handler")
	}
	if records := lines(); len(records) != 0 {
		t.Errorf("got %d command_invoked lines for a component interaction, want 0", len(records))
	}
}

// A panicking command must still land in the invocation count, or the
// denominator drifts and error-prone commands look quieter than healthy ones.
// The decorator is also where the panic stops: it recovers so a bad handler
// cannot take the gateway listener down, which is why this test can assert on
// the line at all rather than dying with the handler.
func TestInstrumentedHandler_PanickingHandlerIsStillCounted(t *testing.T) {
	lines := captureTelemetryLines(t)

	h := registerStub(t, "afsm", func(*discordgo.Session, *discordgo.InteractionCreate) {
		panic("milpac parse blew up")
	})
	h(nil, slashInteraction("afsm"))

	records := lines()
	if len(records) != 1 {
		t.Fatalf("got %d command_invoked lines, want 1", len(records))
	}
	if got := records[0]["command"]; got != "afsm" {
		t.Errorf("command = %q, want %q", got, "afsm")
	}
}

// Free-text options can be enormous — /warden bulkadd takes a comma-separated
// list of up to 50 names in a single string option — and an unbounded copy on
// every telemetry line would bloat the log stream the metrics host ingests.
// The bound asserted here is deliberately far looser than the cap itself, so
// retuning the cap stays a behavior-preserving change.
func TestInstrumentedHandler_OversizedOptionValueIsBounded(t *testing.T) {
	lines := captureTelemetryLines(t)

	oversized := strings.Repeat("trooper.name,", 400)

	h := registerStub(t, "warden", func(*discordgo.Session, *discordgo.InteractionCreate) {})
	h(nil, slashInteraction("warden", &discordgo.ApplicationCommandInteractionDataOption{
		Type:  discordgo.ApplicationCommandOptionString,
		Name:  "discordname",
		Value: oversized,
	}))

	records := lines()
	if len(records) != 1 {
		t.Fatalf("got %d command_invoked lines, want 1", len(records))
	}

	got := records[0]["opt_discordname"]
	if got == "" {
		t.Fatal("opt_discordname was dropped entirely; want a bounded prefix")
	}
	if len(got) > 256 {
		t.Errorf("opt_discordname is %d bytes, want it bounded well under the %d-byte input", len(got), len(oversized))
	}
	if !strings.HasPrefix(oversized, got) {
		t.Errorf("opt_discordname = %q, want a leading portion of the submitted value", got)
	}
}

// telemetrySentryTransport collects events in place of a real Sentry
// connection. It duplicates the shape used in utils/sentry_test.go because Go
// keeps test helpers package-local — the same reason fakeResponder is defined
// twice (see utils/setup_test.go).
type telemetrySentryTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *telemetrySentryTransport) Configure(sentry.ClientOptions)        {}
func (t *telemetrySentryTransport) Flush(time.Duration) bool              { return true }
func (t *telemetrySentryTransport) FlushWithContext(context.Context) bool { return true }
func (t *telemetrySentryTransport) Close()                                {}
func (t *telemetrySentryTransport) SendEvent(e *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}
func (t *telemetrySentryTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

// Failures are Sentry's half of the split (ADR 0011), and this is the wiring
// that makes "which command is failing" answerable there: the decorator knows
// the command name, and a panic recovered without it groups every handler's
// crash under one dispatcher-level context.
func TestInstrumentedHandler_PanicReachesSentryTaggedWithCommand(t *testing.T) {
	captureTelemetryLines(t)

	tr := &telemetrySentryTransport{}
	if err := sentry.Init(sentry.ClientOptions{Dsn: "https://test@example.com/1", Transport: tr}); err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	h := registerStub(t, "s6-it-check", func(*discordgo.Session, *discordgo.InteractionCreate) {
		panic("roster index out of range")
	})
	h(nil, slashInteraction("s6-it-check"))

	events := tr.Events()
	if len(events) != 1 {
		t.Fatalf("got %d Sentry events, want 1", len(events))
	}
	if got := events[0].Tags["command"]; got != "s6-it-check" {
		t.Errorf("tag command = %q, want %q", got, "s6-it-check")
	}
}
