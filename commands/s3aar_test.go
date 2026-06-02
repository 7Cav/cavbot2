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

// serveBattleMetricsWithProfiles stands up a fake BattleMetrics sessions API
// returning the given sessions AND a fake milpacs API that returns a successful
// ProfileResponse for /milpacs/profile/username/<name> when <name> is present in
// profilesByUsername (404 otherwise). This drives SUCCESSFUL enrichPlayer paths
// — the half of /s3aar (combat-roster filter, rank-ID sort, forum BBCode line,
// AAR file body) that serveBattleMetrics's all-404 stub never reaches.
//
// The milpac lookup key is the *cleaned* name (cleanName(session.Name)); keys in
// profilesByUsername must match that cleaned form.
func serveBattleMetricsWithProfiles(t *testing.T, sessions []bmSession, profilesByUsername map[string]utils.ProfileResponse) {
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
		t.Fatalf("serveBattleMetricsWithProfiles marshal: %v", err)
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

	encoded := make(map[string][]byte, len(profilesByUsername))
	for name, p := range profilesByUsername {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("serveBattleMetricsWithProfiles marshal profile %q: %v", name, err)
		}
		encoded[name] = b
	}
	milpacSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/milpacs/profile/username/") {
			name := strings.TrimPrefix(r.URL.Path, "/milpacs/profile/username/")
			if pb, ok := encoded[name]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(pb)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(milpacSrv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(milpacSrv.URL))
}

// combatProfile is a small constructor for a successful enrichment fixture. The
// UniformUrl matches the /\d+/(\d+)\.jpg regex enrichPlayer extracts the milpac
// ID from, so the forum link resolves to https://7cav.us/rosters/profile/<id>.
func combatProfile(username, rankFull, rankID, roster, id string) utils.ProfileResponse {
	return utils.ProfileResponse{
		User:       utils.User{Username: username},
		Rank:       utils.Rank{RankFull: rankFull, RankID: rankID},
		Roster:     roster,
		UniformUrl: fmt.Sprintf("https://7cav.us/data/roster_uniforms/0/%s.jpg", id),
	}
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

// readAARFile finds the aar_roster.txt file followup and returns its decoded
// body. Fails the test if no such followup exists.
func readAARFile(t *testing.T, fups []recordedCall) string {
	t.Helper()
	for idx := range fups {
		for _, f := range fups[idx].Params.Files {
			if f.Name == "aar_roster.txt" {
				raw, err := io.ReadAll(f.Reader)
				if err != nil {
					t.Fatalf("read aar_roster.txt: %v", err)
				}
				return string(raw)
			}
		}
	}
	t.Fatalf("no aar_roster.txt file followup found; got %+v", fups)
	return ""
}

// TestRunS3aar_CombatRosterOutput drives SUCCESSFUL milpac enrichment for three
// players: two combat (different ranks) and one reserve. It asserts the combat-
// only filter, ascending rank-ID ordering, the exact forum BBCode line format,
// and the AAR file body (read from the file reader, not just the caption).
func TestRunS3aar_CombatRosterOutput(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	// Raw BM names → cleaned keys: cleanName("ABC.Alpha.A") == "Alpha.A", etc.
	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Alpha.A", Start: start, Stop: stop},   // combat, rank 5
			{Name: "ABC.Bravo.B", Start: start, Stop: stop},    // combat, rank 2
			{Name: "ABC.Reserve.R", Start: start, Stop: stop},  // reserve → filtered out
		},
		map[string]utils.ProfileResponse{
			"Alpha.A":   combatProfile("AlphaUser", "Sergeant", "5", "ROSTER_TYPE_COMBAT", "101"),
			"Bravo.B":   combatProfile("BravoUser", "Major", "2", "ROSTER_TYPE_COMBAT", "202"),
			"Reserve.R": combatProfile("ReserveUser", "Private", "9", "ROSTER_TYPE_RESERVE", "303"),
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	body := readAARFile(t, followups(r.Calls()))

	// Combat-only filter: the reserve player must not appear.
	if strings.Contains(body, "ReserveUser") {
		t.Fatalf("reserve player must be filtered from combat roster; body:\n%s", body)
	}

	// Ascending rank-ID order: Bravo (rank 2) before Alpha (rank 5).
	bravoLine := "[URL='https://7cav.us/rosters/profile/202']Major BravoUser[/URL]"
	alphaLine := "[URL='https://7cav.us/rosters/profile/101']Sergeant AlphaUser[/URL]"
	want := bravoLine + "\n" + alphaLine
	if body != want {
		t.Fatalf("AAR roster body mismatch.\nwant:\n%s\ngot:\n%s", want, body)
	}

	// Exact forum BBCode line format assertions (redundant with the full-body
	// equality above, but pin the [URL='...']<rank> <user>[/URL] shape explicitly).
	if !strings.Contains(body, bravoLine) {
		t.Fatalf("missing exact BBCode line for Bravo; body:\n%s", body)
	}
	idxBravo := strings.Index(body, "BravoUser")
	idxAlpha := strings.Index(body, "AlphaUser")
	if idxBravo == -1 || idxAlpha == -1 || idxBravo > idxAlpha {
		t.Fatalf("expected ascending rank-ID order (Bravo before Alpha); body:\n%s", body)
	}
}

// TestRunS3aar_EnrichGamertagFallback covers enrichPlayer's second branch: the
// username milpac lookup 404s, but the gamertag lookup (/milpac/gamertag/<name>)
// succeeds, so enrichment still populates CavName/Roster/RankID and the player
// lands on the combat roster.
func TestRunS3aar_EnrichGamertagFallback(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)
	t.Setenv("BM_TOKEN", "test-token")

	payload := map[string]any{"data": []map[string]any{
		{"attributes": map[string]any{"start": start, "stop": stop, "name": "ABC.Gamer.G"}},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
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

	profile := combatProfile("GamerUser", "Corporal", "6", "ROSTER_TYPE_COMBAT", "404")
	profBody, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	milpacSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Username lookup 404s; gamertag lookup succeeds → exercises fallback.
		if strings.HasPrefix(r.URL.Path, "/milpac/gamertag/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(profBody)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(milpacSrv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(milpacSrv.URL))

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	wantLine := "[URL='https://7cav.us/rosters/profile/404']Corporal GamerUser[/URL]"
	if body := readAARFile(t, followups(r.Calls())); body != wantLine {
		t.Fatalf("gamertag-fallback enrichment did not populate combat roster.\nwant: %s\ngot:  %s", wantLine, body)
	}
}

// TestRunS3aar_EmbedPlaytimeAndCount asserts the attendance embed's playtime
// formatting (Xh Ym) and player-count title, not merely that a name substring
// survives. Two players with different playtimes; enrichment succeeds.
func TestRunS3aar_EmbedPlaytimeAndCount(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	// Long player: full 2h05m window. Short player: 45m.
	longStop := start.Add(2*time.Hour + 5*time.Minute)
	shortStop := start.Add(45 * time.Minute)

	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Long.L", Start: start, Stop: longStop},
			{Name: "ABC.Short.S", Start: start, Stop: shortStop},
		},
		map[string]utils.ProfileResponse{
			"Long.L":  combatProfile("LongUser", "Sergeant", "5", "ROSTER_TYPE_COMBAT", "111"),
			"Short.S": combatProfile("ShortUser", "Private", "9", "ROSTER_TYPE_COMBAT", "222"),
		},
	)

	r := &fakeResponder{}
	// Window is 2h05m; min attendance 30 keeps both players.
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2005", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}
	embed := fups[0].Params.Embeds[0]

	// Player count is rendered in the title.
	if embed.Title != "Attendance Summary — 2 Players" {
		t.Fatalf("unexpected embed title: %q", embed.Title)
	}
	// Playtime formatting: 125 min → "2h 5m"; 45 min → "0h 45m".
	if !strings.Contains(embed.Description, "**ABC.Long.L** — 2h 5m") {
		t.Fatalf("expected long player playtime '2h 5m'; got %q", embed.Description)
	}
	if !strings.Contains(embed.Description, "**ABC.Short.S** — 0h 45m") {
		t.Fatalf("expected short player playtime '0h 45m'; got %q", embed.Description)
	}
}

// TestRunS3aar_InvalidStartDate asserts the bad-start-date validation path emits
// the specific followup and no embed/file.
func TestRunS3aar_InvalidStartDate(t *testing.T) {
	r := &fakeResponder{}
	// "99XXX25" is the wrong-length-but-also-bad-month case; parseDateTime fails.
	i := s3aarOptions("Tac1", "BADDATE", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) != 1 {
		t.Fatalf("bad start date must produce exactly one followup; got %d: %+v", len(fups), fups)
	}
	if !strings.HasPrefix(fups[0].Params.Content, "Invalid start date/time:") {
		t.Fatalf("unexpected start-date error content: %q", fups[0].Params.Content)
	}
	assertNoRosterFollowup(t, fups)
}

// TestRunS3aar_InvalidEndDate asserts the bad-end-date path. Start date is valid
// so parseDateTime fails only on the second call.
func TestRunS3aar_InvalidEndDate(t *testing.T) {
	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10ZZZ25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) != 1 {
		t.Fatalf("bad end date must produce exactly one followup; got %d: %+v", len(fups), fups)
	}
	if !strings.HasPrefix(fups[0].Params.Content, "Invalid end date/time:") {
		t.Fatalf("unexpected end-date error content: %q", fups[0].Params.Content)
	}
	assertNoRosterFollowup(t, fups)
}

// TestRunS3aar_NotAvailableServer asserts the NotAvailable server choice routes
// to the invalid-server followup (getServerID's default branch) and emits no
// roster output.
func TestRunS3aar_NotAvailableServer(t *testing.T) {
	r := &fakeResponder{}
	i := s3aarOptions("NotAvailable", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) != 1 {
		t.Fatalf("NotAvailable server must produce exactly one followup; got %d: %+v", len(fups), fups)
	}
	if !strings.HasPrefix(fups[0].Params.Content, "Invalid server selection:") {
		t.Fatalf("unexpected server error content: %q", fups[0].Params.Content)
	}
	assertNoRosterFollowup(t, fups)
}

// TestRunS3aar_UnsetBMToken asserts that with a valid date+server but no
// BM_TOKEN, fetchBattleMetricsSessions fails and the specific followup is sent
// with no roster output.
func TestRunS3aar_UnsetBMToken(t *testing.T) {
	t.Setenv("BM_TOKEN", "")
	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) != 1 {
		t.Fatalf("unset BM_TOKEN must produce exactly one followup; got %d: %+v", len(fups), fups)
	}
	if !strings.Contains(fups[0].Params.Content, "Failed to fetch BattleMetrics data") ||
		!strings.Contains(fups[0].Params.Content, "BM_TOKEN not set") {
		t.Fatalf("unexpected BM_TOKEN error content: %q", fups[0].Params.Content)
	}
	assertNoRosterFollowup(t, fups)
}

// assertNoRosterFollowup fails if any followup carries an embed or a file —
// the validation/error paths must not emit roster output.
func assertNoRosterFollowup(t *testing.T, fups []recordedCall) {
	t.Helper()
	for _, f := range fups {
		if len(f.Params.Embeds) != 0 {
			t.Fatalf("no embed followup expected on this path; got %+v", f.Params)
		}
		if len(f.Params.Files) != 0 {
			t.Fatalf("no file followup expected on this path; got %+v", f.Params)
		}
	}
}

func TestGetServerID(t *testing.T) {
	cases := []struct {
		server  string
		want    string
		wantErr bool
	}{
		{"Tac1", "35142042", false},
		{"Tac2", "32703792", false},
		{"TS1", "32703836", false},
		{"TS2", "32708755", false},
		{"NotAvailable", "", true},
		{"bogus", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		t.Run(c.server, func(t *testing.T) {
			got, err := getServerID(c.server)
			if c.wantErr {
				if err == nil {
					t.Fatalf("getServerID(%q) expected error, got %q", c.server, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("getServerID(%q) unexpected error: %v", c.server, err)
			}
			if got != c.want {
				t.Fatalf("getServerID(%q) = %q, want %q", c.server, got, c.want)
			}
		})
	}
}

func TestParseDateTime(t *testing.T) {
	cases := []struct {
		name     string
		date     string
		time     string
		wantErr  bool
		wantISO  string // expected UTC time when no error, RFC3339
	}{
		{"valid", "10NOV25", "1830", false, "2025-11-10T18:30:00Z"},
		{"valid lowercase month", "10nov25", "0000", false, "2025-11-10T00:00:00Z"},
		{"bad date length short", "1NOV25", "1830", true, ""},
		{"bad date length long", "100NOV25", "1830", true, ""},
		{"bad time length", "10NOV25", "183", true, ""},
		{"bad month", "10ZZZ25", "1830", true, ""},
		{"non-numeric day yields parse error", "XXNOV25", "1830", true, ""},
		{"non-numeric time yields parse error", "10NOV25", "XX30", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseDateTime(c.date, c.time)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseDateTime(%q,%q) expected error, got %v", c.date, c.time, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDateTime(%q,%q) unexpected error: %v", c.date, c.time, err)
			}
			if got.UTC().Format(time.RFC3339) != c.wantISO {
				t.Fatalf("parseDateTime(%q,%q) = %s, want %s", c.date, c.time, got.UTC().Format(time.RFC3339), c.wantISO)
			}
		})
	}
}

func TestCleanName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"prefix extraction", "ABC.Smith.J", "Smith.J"},
		{"prefix extraction two-letter suffix", "7Cv.Doe.Jo", "Doe.Jo"},
		{"prefix extraction with surrounding space", "  XYZ.Brown.K  ", "Brown.K"},
		{"already cleaned passthrough", "Smith.J", "Smith.J"},
		{"plain gamertag passthrough", "RandomGamer", "RandomGamer"},
		{"no dotted suffix passthrough", "ABC123", "ABC123"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanName(c.in); got != c.want {
				t.Fatalf("cleanName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
