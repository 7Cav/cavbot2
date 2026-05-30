package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// bmSession is one BattleMetrics session record as the fake API serializes it.
// Fields mirror the subset s3aar.go decodes (BMResponse).
type bmSession struct {
	Name  string
	Start time.Time
	Stop  time.Time
}

// serveBattleMetrics stands up a fake BattleMetrics sessions API returning the
// given sessions, and a fake milpacs API that 404s every enrichment lookup so
// enrichPlayer fails deterministically (its error is intentionally ignored in
// s3aar.go, leaving CavName/Roster empty — no live network, no flake). It swaps
// both bmBaseURL and utils apiBaseURL via t.Cleanup-registered restorers.
func serveBattleMetrics(t *testing.T, sessions []bmSession) {
	t.Helper()
	t.Setenv("BM_TOKEN", "test-token")

	payload := map[string]any{"data": make([]map[string]any, 0, len(sessions))}
	for _, s := range sessions {
		payload["data"] = append(payload["data"].([]map[string]any), map[string]any{
			"attributes": map[string]any{
				"start": s.Start,
				"stop":  s.Stop,
				"name":  s.Name,
			},
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("serveBattleMetrics marshal: %v", err)
	}

	bmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/relationships/sessions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(bmSrv.Close)

	prevBM := bmBaseURL
	bmBaseURL = bmSrv.URL
	t.Cleanup(func() { bmBaseURL = prevBM })

	// All milpacs enrichment lookups 404 → enrichPlayer returns an error that
	// s3aar.go discards, so sessions still build with empty enrichment fields.
	milpacSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(milpacSrv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(milpacSrv.URL))
}

// serveBattleMetricsError stands up a BattleMetrics API that returns a non-200,
// driving the upstream-error path in fetchBattleMetricsSessions.
func serveBattleMetricsError(t *testing.T, status int) {
	t.Helper()
	t.Setenv("BM_TOKEN", "test-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	prevBM := bmBaseURL
	bmBaseURL = srv.URL
	t.Cleanup(func() { bmBaseURL = prevBM })
}

// s3aarOptions builds the required /s3aar option set, with an optional debug
// value appended when non-empty.
func s3aarOptions(server, startDate, endDate, startTime, endTime string, minAttendance int, debug string) *discordgo.InteractionCreate {
	opts := []*discordgo.ApplicationCommandInteractionDataOption{
		stringOption("game", "Arma"),
		stringOption("server", server),
		stringOption("start_date", startDate),
		stringOption("end_date", endDate),
		stringOption("start_time", startTime),
		stringOption("end_time", endTime),
		{Name: "minimum_attendance", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(minAttendance)},
	}
	if debug != "" {
		opts = append(opts, stringOption("debug", debug))
	}
	return fakeAppCommandInteraction(opts...)
}

// followups returns every recorded Followup call in order.
func followups(calls []recordedCall) []recordedCall {
	var out []recordedCall
	for _, c := range calls {
		if c.Method == "Followup" {
			out = append(out, c)
		}
	}
	return out
}

func TestRunS3aar_SuccessEmbedOutput(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)
	serveBattleMetrics(t, []bmSession{
		{Name: "ABC.Smith.J", Start: start, Stop: stop},
	})

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	calls := r.Calls()
	// Defer must be the first call.
	if len(calls) == 0 || calls[0].Method != "Respond" ||
		calls[0].Response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource {
		t.Fatalf("expected deferred Respond first; got %+v", calls)
	}
	fups := followups(calls)
	if len(fups) < 2 {
		t.Fatalf("expected at least embed + file followups; got %d", len(fups))
	}
	// First followup carries the attendance embed.
	if len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("first followup must carry an embed; got %+v", fups[0].Params)
	}
	if !strings.Contains(fups[0].Params.Embeds[0].Description, "Smith.J") {
		t.Fatalf("embed should list player; got %q", fups[0].Params.Embeds[0].Description)
	}
}

func TestRunS3aar_SuccessFileOutput(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(3 * time.Hour)
	// Many distinct players → large attendance result; the AAR roster file
	// followup is always emitted regardless.
	var sessions []bmSession
	for n := 0; n < 60; n++ {
		sessions = append(sessions, bmSession{
			Name:  fmt.Sprintf("ABC.Player%02d.X", n),
			Start: start,
			Stop:  stop,
		})
	}
	serveBattleMetrics(t, sessions)

	r := &fakeResponder{}
	i := s3aarOptions("Tac2", "10NOV25", "10NOV25", "1800", "2100", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	var fileFup *recordedCall
	for idx := range fups {
		for _, f := range fups[idx].Params.Files {
			if f.Name == "aar_roster.txt" {
				fileFup = &fups[idx]
			}
		}
	}
	if fileFup == nil {
		t.Fatalf("expected an aar_roster.txt file followup; got %+v", fups)
	}
	if fileFup.Params.Content != "AAR Roster (Copy Text to Forum Post)" {
		t.Fatalf("unexpected AAR file content prefix: %q", fileFup.Params.Content)
	}
}

func TestRunS3aar_DebugJSONOutput(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)
	serveBattleMetrics(t, []bmSession{
		{Name: "ABC.Jones.K", Start: start, Stop: stop},
	})

	r := &fakeResponder{}
	i := s3aarOptions("TS1", "10NOV25", "10NOV25", "1800", "2000", 30, "Yes")
	runS3aar(r, i)

	fups := followups(r.Calls())
	var jsonFup *recordedCall
	for idx := range fups {
		for _, f := range fups[idx].Params.Files {
			if f.Name == "attendance.json" {
				jsonFup = &fups[idx]
			}
		}
	}
	if jsonFup == nil {
		t.Fatalf("debug=Yes must emit attendance.json followup; got %+v", fups)
	}
	// The JSON file must contain decodable PlayerSession data.
	var f *discordgo.File
	for _, file := range jsonFup.Params.Files {
		if file.Name == "attendance.json" {
			f = file
		}
	}
	raw, err := io.ReadAll(f.Reader)
	if err != nil {
		t.Fatalf("read json file: %v", err)
	}
	var decoded []PlayerSession
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("attendance.json not valid PlayerSession JSON: %v\n%s", err, raw)
	}
	if len(decoded) != 1 || decoded[0].Name != "ABC.Jones.K" {
		t.Fatalf("unexpected debug payload: %+v", decoded)
	}
}

func TestRunS3aar_BattleMetricsUpstreamError(t *testing.T) {
	serveBattleMetricsError(t, http.StatusInternalServerError)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	calls := r.Calls()
	// Defer succeeds, then the upstream failure is reported via a followup.
	fups := followups(calls)
	if len(fups) != 1 {
		t.Fatalf("upstream error must produce exactly one (error) followup; got %d: %+v", len(fups), fups)
	}
	if !strings.Contains(fups[0].Params.Content, "Failed to fetch BattleMetrics data") {
		t.Fatalf("unexpected upstream-error content: %q", fups[0].Params.Content)
	}
	// No embed/file followup on the error path.
	if len(fups[0].Params.Embeds) != 0 || len(fups[0].Params.Files) != 0 {
		t.Fatalf("error followup must be plain content, got %+v", fups[0].Params)
	}
}
