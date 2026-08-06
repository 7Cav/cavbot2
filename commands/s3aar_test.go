package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// captureWarnLogs clears the package-wide log sink and returns it, so callers
// can assert on the lines the code under test emits.
//
// It deliberately does NOT install a logger of its own. utils.Logger is written
// exactly once, in TestMain, because a leaked goroutine reads it for the rest
// of the run and any later write is a data race — see the comment on TestMain.
// The sink therefore carries INFO as well as WARN lines; every assertion here
// is a presence check, so the extra lines are inert.
func captureWarnLogs(t *testing.T) *syncBuffer {
	t.Helper()
	testLogs.Reset()
	return testLogs
}

// warningField returns the attendance embed's enrichment-failure warning field,
// or nil if no such field is present. The warning field is identified by its
// Name containing "enrich" (case-insensitive).
func warningField(embed *discordgo.MessageEmbed) *discordgo.MessageEmbedField {
	if embed == nil {
		return nil
	}
	for _, f := range embed.Fields {
		if strings.Contains(strings.ToLower(f.Name), "enrich") {
			return f
		}
	}
	return nil
}

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
func serveBattleMetrics(t *testing.T, sessions []bmSession) *bmCapture {
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

	capture := &bmCapture{}
	bmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/relationships/sessions") {
			capture.record(r.URL.Query())
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
	return capture
}

// bmCapture records the query string of the BattleMetrics session request so a
// test can assert which window /s3aar actually asked for. It only records —
// asserting inside the shared handler would couple every caller to the query
// shape. The mutex is load-bearing: the handler goroutine writes and the test
// goroutine reads, and CI runs the suite under -race.
type bmCapture struct {
	mu    sync.Mutex
	query url.Values
}

func (c *bmCapture) record(q url.Values) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.query = q
}

func (c *bmCapture) Query() url.Values {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.query
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
	// One player enriches successfully (Jones.K) and one fails (absent from the
	// profile map → 404). This lets #153 assert EnrichFailed round-trips through
	// the debug JSON for BOTH values, locking the json:"enrich_failed" tag too.
	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Jones.K", Start: start, Stop: stop}, // enriches OK
			{Name: "ABC.Ghost.G", Start: start, Stop: stop}, // 404 → EnrichFailed
		},
		map[string]utils.ProfileResponse{
			"Jones.K": combatProfile("JonesUser", "Sergeant", "5", "ROSTER_TYPE_COMBAT", "101"),
		},
	)

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
	if len(decoded) != 2 {
		t.Fatalf("expected 2 sessions in debug payload; got %+v", decoded)
	}

	// #153: EnrichFailed must round-trip through the debug JSON under the
	// json:"enrich_failed" tag — true for the 404 player, false for the enriched
	// one. Index by name since map iteration order isn't stable.
	byName := make(map[string]PlayerSession, len(decoded))
	for _, d := range decoded {
		byName[d.Name] = d
	}
	jones, ok := byName["ABC.Jones.K"]
	if !ok {
		t.Fatalf("expected enriched player ABC.Jones.K in payload; got %+v", decoded)
	}
	if jones.EnrichFailed {
		t.Fatalf("successfully-enriched player must have EnrichFailed==false; got %+v", jones)
	}
	ghost, ok := byName["ABC.Ghost.G"]
	if !ok {
		t.Fatalf("expected failed player ABC.Ghost.G in payload; got %+v", decoded)
	}
	if !ghost.EnrichFailed {
		t.Fatalf("404 player must have EnrichFailed==true; got %+v", ghost)
	}

	// Lock the wire tag explicitly: the raw JSON must carry "enrich_failed":true
	// for the failed player, proving the json:"enrich_failed" tag (not the Go
	// field name) is what serializes.
	if !strings.Contains(string(raw), `"enrich_failed": true`) {
		t.Fatalf("debug JSON must serialize the enrich_failed tag as true for a failure; got:\n%s", raw)
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
			{Name: "ABC.Bravo.B", Start: start, Stop: stop},   // combat, rank 2
			{Name: "ABC.Reserve.R", Start: start, Stop: stop}, // reserve → filtered out
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
	// #151: the gamertag fallback must receive the UNCLEANED raw BattleMetrics
	// tag, not the cleaned name. Pin that explicitly: the gamertag lookup only
	// succeeds at the RAW path (/milpac/gamertag/ABC.Gamer.G). If enrichPlayer
	// were to clean the tag before the gamertag lookup (the #151 bug), it would
	// request /milpac/gamertag/Gamer.G, this server would 404 it, and the player
	// would never land on the combat roster — failing the assertion below.
	const rawTag = "ABC.Gamer.G"
	var gamertagPaths []string
	milpacSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Username lookup 404s; gamertag lookup succeeds ONLY for the raw tag.
		if r.URL.Path == "/milpac/gamertag/"+rawTag {
			gamertagPaths = append(gamertagPaths, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(profBody)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/milpac/gamertag/") {
			// Record any non-raw gamertag lookup so we can assert it never happens.
			gamertagPaths = append(gamertagPaths, r.URL.Path)
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

	// The gamertag fallback must have queried the RAW tag and never the cleaned one.
	sawRaw := false
	for _, p := range gamertagPaths {
		if p != "/milpac/gamertag/"+rawTag {
			t.Fatalf("gamertag fallback must query the RAW tag only; saw %q", p)
		}
		sawRaw = true
	}
	if !sawRaw {
		t.Fatalf("expected a gamertag lookup against the raw tag %q; got none", rawTag)
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
	// "BADDATE" has no leading digits, so it fails the date pattern outright.
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
// so parsing fails only on the second call.
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

// TestRunS3aar_EnrichmentFailureWarningFooter drives the all-404 milpacs seam
// so enrichPlayer fails for the attending player. It asserts BOTH:
//   - a utils.Warn is emitted carrying the raw BattleMetrics name AND the cleaned
//     name, so the silent drop is observable in logs; and
//   - the attendance embed gains a warning field listing the failed player and
//     stating they were excluded from the combat roster.
func TestRunS3aar_EnrichmentFailureWarningFooter(t *testing.T) {
	logs := captureWarnLogs(t)

	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)
	// cleanName("ABC.Ghost.G") == "Ghost.G"; all milpacs lookups 404 → failure.
	serveBattleMetrics(t, []bmSession{
		{Name: "ABC.Ghost.G", Start: start, Stop: stop},
	})

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	// Warn emission: must carry both the raw and cleaned names.
	logged := logs.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("expected a WARN log on enrichment failure; got:\n%s", logged)
	}
	// Assert the structured keys, not bare substrings: "Ghost.G" is a substring of
	// "ABC.Ghost.G", so a contains-check on the cleaned name alone would pass even
	// if cleaned_name were dropped. Pinning key=value proves both fields are emitted
	// distinctly (slog TextHandler renders attrs as key=value, unquoted when safe).
	if !strings.Contains(logged, "raw_name=ABC.Ghost.G") {
		t.Fatalf("WARN log must carry the raw BattleMetrics name as raw_name; got:\n%s", logged)
	}
	if !strings.Contains(logged, "cleaned_name=Ghost.G") {
		t.Fatalf("WARN log must carry the cleaned name as cleaned_name; got:\n%s", logged)
	}

	// Warning footer: the attendance embed must carry an enrichment-failure field
	// naming the excluded player and stating they were excluded from the combat
	// roster.
	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}
	field := warningField(fups[0].Params.Embeds[0])
	if field == nil {
		t.Fatalf("expected an enrichment-failure warning field on the embed; got %+v", fups[0].Params.Embeds[0])
	}
	if !strings.Contains(field.Value, "ABC.Ghost.G") {
		t.Fatalf("warning field must list the failed player; got %q", field.Value)
	}
	if !strings.Contains(strings.ToLower(field.Value+field.Name), "combat") {
		t.Fatalf("warning field must state exclusion from the combat roster; got name=%q value=%q", field.Name, field.Value)
	}
}

// TestRunS3aar_NonCombatNotInWarningFooter pins the empty-vs-failure invariant:
// a player who enriches SUCCESSFULLY but is on a non-combat roster is filtered
// from the combat roster (expected) yet must NOT be reported as an enrichment
// failure — so no warning field appears at all when every player enriches.
func TestRunS3aar_NonCombatNotInWarningFooter(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Combat.C", Start: start, Stop: stop},  // combat
			{Name: "ABC.Reserve.R", Start: start, Stop: stop}, // reserve (non-combat)
		},
		map[string]utils.ProfileResponse{
			"Combat.C":  combatProfile("CombatUser", "Sergeant", "5", "ROSTER_TYPE_COMBAT", "101"),
			"Reserve.R": combatProfile("ReserveUser", "Private", "9", "ROSTER_TYPE_RESERVE", "303"),
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}
	embed := fups[0].Params.Embeds[0]

	// No enrichment-failure field: every player enriched fine.
	if field := warningField(embed); field != nil {
		t.Fatalf("non-combat-but-enriched player must NOT appear in a warning field; got %+v", field)
	}
	// The reserve player must still be filtered from the combat roster file.
	body := readAARFile(t, fups)
	if strings.Contains(body, "ReserveUser") {
		t.Fatalf("reserve player must be filtered from combat roster; body:\n%s", body)
	}
}

// TestRunS3aar_HappyPathNoWarningField guards the byte-for-byte invariant: when
// zero players fail enrichment, the attendance embed carries no warning field at
// all (no Fields), matching today's happy-path output.
func TestRunS3aar_HappyPathNoWarningField(t *testing.T) {
	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Solo.S", Start: start, Stop: stop},
		},
		map[string]utils.ProfileResponse{
			"Solo.S": combatProfile("SoloUser", "Sergeant", "5", "ROSTER_TYPE_COMBAT", "101"),
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}
	if len(fups[0].Params.Embeds[0].Fields) != 0 {
		t.Fatalf("happy path must carry no embed fields; got %+v", fups[0].Params.Embeds[0].Fields)
	}
}

// TestRunS3aar_EmptyFieldsTreatedAsFailure pins #152: a milpacs 200 carrying
// empty RankFull/Username (and/or empty Roster) is NOT a usable enrichment. The
// player must surface in the ⚠️ warning field and must NOT emit a blank/ghost
// BBCode line ([URL='...'] [/URL]) on the combat roster.
func TestRunS3aar_EmptyFieldsTreatedAsFailure(t *testing.T) {
	logs := captureWarnLogs(t)

	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	// A profile that returns HTTP 200 but with empty enrichment fields: no
	// rank/username/roster. UniformUrl still matches the ID regex so the ONLY
	// thing that makes this a failure is the empty fields (not a regex miss).
	emptyProfile := utils.ProfileResponse{
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/999.jpg",
	}
	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Ghost.G", Start: start, Stop: stop},
		},
		map[string]utils.ProfileResponse{
			"Ghost.G": emptyProfile,
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}

	// The empty-fields player must surface as an enrichment failure.
	field := warningField(fups[0].Params.Embeds[0])
	if field == nil {
		t.Fatalf("empty-fields 200 must surface as an enrichment failure; got no warning field")
	}
	if !strings.Contains(field.Value, "ABC.Ghost.G") {
		t.Fatalf("warning field must list the empty-fields player; got %q", field.Value)
	}

	// The combat roster file must NOT carry a ghost BBCode line. With the only
	// player excluded, the body is empty — and must never contain a blank
	// [URL='...'] [/URL] entry.
	body := readAARFile(t, fups)
	if strings.Contains(body, "[URL=") {
		t.Fatalf("empty-fields player must not emit a ghost BBCode line; body:\n%q", body)
	}

	// The failure must be observable in logs. Asserting the "unusable enrichment"
	// text (not just level=WARN) pins the validateProfile branch vs an accidental
	// 404, and "Ghost.G" pins the name folded into the error (the cleaned form the
	// username lookup used).
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("expected a WARN log on empty-fields enrichment failure; got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "unusable enrichment") {
		t.Fatalf("WARN log must carry the validateProfile error text; got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "Ghost.G") {
		t.Fatalf("validateProfile error must attribute the player name; got:\n%s", logs.String())
	}
}

// TestRunS3aar_EmptyRosterOnlyTreatedAsFailure pins validateProfile's SECOND
// clause (#152): a 200 with a populated rank/username but an empty Roster string.
// The empty-fields test above trips the first clause (rank/username), so without
// this case dropping the `Roster == ""` check would pass CI. The player must still
// surface in the ⚠️ warning field and emit no ghost BBCode line.
func TestRunS3aar_EmptyRosterOnlyTreatedAsFailure(t *testing.T) {
	logs := captureWarnLogs(t)

	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	// Populated rank/username and a regex-matching UniformUrl, but an empty Roster
	// — so the ONLY thing that makes this a failure is the empty roster string.
	emptyRosterProfile := combatProfile("GhostUser", "Private", "9", "", "999")
	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Ghost.G", Start: start, Stop: stop},
		},
		map[string]utils.ProfileResponse{
			"Ghost.G": emptyRosterProfile,
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}

	field := warningField(fups[0].Params.Embeds[0])
	if field == nil {
		t.Fatalf("empty-roster 200 must surface as an enrichment failure; got no warning field")
	}
	if !strings.Contains(field.Value, "ABC.Ghost.G") {
		t.Fatalf("warning field must list the empty-roster player; got %q", field.Value)
	}

	body := readAARFile(t, fups)
	if strings.Contains(body, "[URL=") {
		t.Fatalf("empty-roster player must not emit a ghost BBCode line; body:\n%q", body)
	}

	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("expected a WARN log on empty-roster enrichment failure; got:\n%s", logs.String())
	}
	// Pin the empty-roster clause specifically (vs the empty-rank/username clause
	// or a 404), and the attributed name.
	if !strings.Contains(logs.String(), "empty roster") {
		t.Fatalf("WARN log must carry the empty-roster error text; got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "Ghost.G") {
		t.Fatalf("validateProfile error must attribute the player name; got:\n%s", logs.String())
	}
}

// TestRunS3aar_GamertagFallbackEmptyFieldsTreatedAsFailure pins the validateProfile
// guard on the GAMERTAG fallback branch (s3aar.go:159) — a copy of the username
// branch's guard that the other #152 tests never reach (they fail at the username
// lookup). Here the username lookup 404s, the gamertag fallback returns a 200 with
// empty fields, and the player must still surface as an enrichment failure rather
// than emitting a ghost BBCode line. Dropping the gamertag-branch guard would let
// this slip through, failing the warning-field assertion.
func TestRunS3aar_GamertagFallbackEmptyFieldsTreatedAsFailure(t *testing.T) {
	logs := captureWarnLogs(t)

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

	// Empty enrichment fields but a regex-matching UniformUrl, so the ONLY cause of
	// failure is validateProfile on the gamertag branch (not a regex miss). If that
	// guard were removed, this would reach link construction and emit a ghost line.
	const rawTag = "ABC.Gamer.G"
	emptyProfile := utils.ProfileResponse{
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/999.jpg",
	}
	profBody, err := json.Marshal(emptyProfile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	milpacSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Username lookup 404s; gamertag lookup returns a 200-with-empty-fields.
		if r.URL.Path == "/milpac/gamertag/"+rawTag {
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

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}

	field := warningField(fups[0].Params.Embeds[0])
	if field == nil {
		t.Fatalf("gamertag-branch empty 200 must surface as an enrichment failure; got no warning field")
	}
	if !strings.Contains(field.Value, rawTag) {
		t.Fatalf("warning field must list the gamertag-fallback player; got %q", field.Value)
	}

	body2 := readAARFile(t, fups)
	if strings.Contains(body2, "[URL=") {
		t.Fatalf("gamertag-branch empty player must not emit a ghost BBCode line; body:\n%q", body2)
	}

	// The gamertag-branch validateProfile error carries the RAW tag (the gamertag
	// lookup key) and the "unusable enrichment" text.
	if !strings.Contains(logs.String(), "unusable enrichment") {
		t.Fatalf("WARN log must carry the validateProfile error text; got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), rawTag) {
		t.Fatalf("gamertag-branch error must attribute the raw tag; got:\n%s", logs.String())
	}
}

// TestRunS3aar_MixedRosterFailuresWarningField exercises the realistic case all
// three player classes appear in ONE run: a combat player, a non-combat (reserve)
// player, and two enrichment failures. It guards the loop's `continue` (a failed
// player must be skipped for the combat-roster check, not fall through to it) plus
// the multi-failure rendering (`(N)` count + sort.Strings ordering) that the
// single-failure test leaves uncovered. Two failures with names that sort in a
// known order pin the alphabetical ordering.
func TestRunS3aar_MixedRosterFailuresWarningField(t *testing.T) {
	logs := captureWarnLogs(t)

	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	// Zulu.Z / Alpha.A are absent from the profile map → both 404 → enrichment
	// fails for them; Combat.C and Reserve.R enrich successfully.
	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Combat.C", Start: start, Stop: stop},  // combat → roster
			{Name: "ABC.Reserve.R", Start: start, Stop: stop}, // reserve → filtered, NOT a failure
			{Name: "ABC.Zulu.Z", Start: start, Stop: stop},    // enrichment failure
			{Name: "ABC.Alpha.A", Start: start, Stop: stop},   // enrichment failure
		},
		map[string]utils.ProfileResponse{
			"Combat.C":  combatProfile("CombatUser", "Sergeant", "5", "ROSTER_TYPE_COMBAT", "101"),
			"Reserve.R": combatProfile("ReserveUser", "Private", "9", "ROSTER_TYPE_RESERVE", "303"),
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}
	field := warningField(fups[0].Params.Embeds[0])
	if field == nil {
		t.Fatalf("expected an enrichment-failure warning field; got %+v", fups[0].Params.Embeds[0])
	}

	// Count: exactly the two failures, not the reserve player.
	if !strings.Contains(field.Name, "(2)") {
		t.Fatalf("warning field must report a count of 2 failures; got name=%q", field.Name)
	}
	// Both failed raw names listed; reserve and combat players are NOT.
	if !strings.Contains(field.Value, "ABC.Alpha.A") || !strings.Contains(field.Value, "ABC.Zulu.Z") {
		t.Fatalf("warning field must list both failed players; got %q", field.Value)
	}
	if strings.Contains(field.Value, "Reserve") || strings.Contains(field.Value, "Combat") {
		t.Fatalf("warning field must list ONLY enrichment failures; got %q", field.Value)
	}
	// sort.Strings ordering: "ABC.Alpha.A" precedes "ABC.Zulu.Z".
	if strings.Index(field.Value, "ABC.Alpha.A") > strings.Index(field.Value, "ABC.Zulu.Z") {
		t.Fatalf("warning field must list failures in sorted order (Alpha before Zulu); got %q", field.Value)
	}

	// Combat roster: only the combat player survives — reserve filtered, failures
	// skipped by the `continue` rather than mis-included.
	body := readAARFile(t, fups)
	wantLine := "[URL='https://7cav.us/rosters/profile/101']Sergeant CombatUser[/URL]"
	if body != wantLine {
		t.Fatalf("combat roster must contain only the combat player.\nwant: %s\ngot:  %s", wantLine, body)
	}

	// Both failures emit a WARN carrying their raw + cleaned names.
	logged := logs.String()
	if got := strings.Count(logged, "level=WARN"); got != 2 {
		t.Fatalf("expected 2 WARN lines (one per failure); got %d:\n%s", got, logged)
	}
	if !strings.Contains(logged, "raw_name=ABC.Alpha.A") || !strings.Contains(logged, "cleaned_name=Alpha.A") ||
		!strings.Contains(logged, "raw_name=ABC.Zulu.Z") || !strings.Contains(logged, "cleaned_name=Zulu.Z") {
		t.Fatalf("each WARN must carry raw_name + cleaned_name for its failed player; got:\n%s", logged)
	}
}

// TestBuildEnrichmentFailureField_Truncation guards the Discord 1024-char field
// limit: a mass-failure run must still produce a deliverable field rather than an
// over-limit Value that Discord would reject (dropping the whole attendance embed).
// It asserts the Value stays within the limit, that early names are listed, and
// that the omitted tail is summarized with an accurate "…and N more" count.
func TestBuildEnrichmentFailureField_Truncation(t *testing.T) {
	// 200 names of ~24 chars each (~4800 chars) — far past the 1024 limit.
	failures := make([]string, 200)
	for i := range failures {
		failures[i] = fmt.Sprintf("ABC.Player%03d.PlayerX", i)
	}

	field := buildEnrichmentFailureField(failures)

	if got := len(field.Value); got > discordFieldValueLimit {
		t.Fatalf("field Value length %d exceeds Discord limit %d", got, discordFieldValueLimit)
	}
	if !strings.Contains(field.Name, "(200)") {
		t.Fatalf("field Name must report the full failure count; got %q", field.Name)
	}
	if !strings.Contains(field.Value, "ABC.Player000.PlayerX") {
		t.Fatalf("field Value must list the first failure; got %q", field.Value)
	}
	// The omitted tail must be summarized, and the count must be accurate: total
	// minus the number actually listed.
	listed := strings.Count(field.Value, "ABC.Player")
	wantNote := fmt.Sprintf("…and %d more", len(failures)-listed)
	if !strings.Contains(field.Value, wantNote) {
		t.Fatalf("field Value must summarize the omitted tail as %q; got %q", wantNote, field.Value)
	}
}

// TestBuildEnrichmentFailureField_NoTruncation confirms a small list is rendered
// in full with no "…and N more" note — the common case must be unaffected by the
// truncation guard.
func TestBuildEnrichmentFailureField_NoTruncation(t *testing.T) {
	failures := []string{"ABC.Alpha.A", "ABC.Bravo.B", "ABC.Charlie.C"}

	field := buildEnrichmentFailureField(failures)

	for _, name := range failures {
		if !strings.Contains(field.Value, name) {
			t.Fatalf("field Value must list %q in full; got %q", name, field.Value)
		}
	}
	if strings.Contains(field.Value, "more") {
		t.Fatalf("small list must not be truncated; got %q", field.Value)
	}
}

// TestRunS3aar_UniformURLRegexFailure covers #154: enrichPlayer's branch where a
// milpac lookup SUCCEEDS (2xx, fully populated fields) but UniformUrl does NOT
// match /\d+/(\d+)\.jpg, so the milpac ID can't be extracted. That must surface
// as an enrichment failure (warning field + WARN log), not a panic or a silently
// included blank entry.
func TestRunS3aar_UniformURLRegexFailure(t *testing.T) {
	logs := captureWarnLogs(t)

	start := time.Date(2025, 11, 10, 18, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Hour)

	// Populated rank/username/roster (so it passes the #152 empty-fields guard)
	// but a UniformUrl that the ID regex can't match.
	badURLProfile := utils.ProfileResponse{
		User:       utils.User{Username: "BadURLUser"},
		Rank:       utils.Rank{RankFull: "Sergeant", RankID: "5"},
		Roster:     "ROSTER_TYPE_COMBAT",
		UniformUrl: "https://7cav.us/data/roster_uniforms/no-numeric-id.png",
	}
	serveBattleMetricsWithProfiles(t,
		[]bmSession{
			{Name: "ABC.Badurl.B", Start: start, Stop: stop},
		},
		map[string]utils.ProfileResponse{
			"Badurl.B": badURLProfile,
		},
	)

	r := &fakeResponder{}
	i := s3aarOptions("Tac1", "10NOV25", "10NOV25", "1800", "2000", 30, "")
	runS3aar(r, i)

	fups := followups(r.Calls())
	if len(fups) == 0 || len(fups[0].Params.Embeds) == 0 {
		t.Fatalf("expected attendance embed in first followup; got %+v", fups)
	}

	// The regex-miss player must surface as an enrichment failure.
	field := warningField(fups[0].Params.Embeds[0])
	if field == nil {
		t.Fatalf("UniformUrl regex miss must surface as enrichment failure; got no warning field")
	}
	if !strings.Contains(field.Value, "ABC.Badurl.B") {
		t.Fatalf("warning field must list the regex-miss player; got %q", field.Value)
	}

	// It must not slip onto the combat roster as a ghost line.
	body := readAARFile(t, fups)
	if strings.Contains(body, "[URL=") {
		t.Fatalf("regex-miss player must not emit a BBCode roster line; body:\n%q", body)
	}

	// And it must be observable in logs — asserting the UniformUrl error text (not
	// just level=WARN) pins THIS branch, distinguishing it from an empty-fields or
	// 404 failure that would render an identical warning field.
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("expected a WARN log on UniformUrl regex failure; got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "UniformUrl") {
		t.Fatalf("WARN log must carry the UniformUrl error to pin the regex branch; got:\n%s", logs.String())
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

// TestRunS3aar_SendsResolvedWindowToBattleMetrics pins the instants /s3aar
// actually asks BattleMetrics for. Without it nothing observes a successfully
// parsed date: the two rejection tests only exercise the error wrapper, and the
// green-path harness matches on URL path alone. It compares parsed instants
// rather than the raw query string because the RFC3339 layout is our own
// Format call, not something the vendor requires.
func TestRunS3aar_SendsResolvedWindowToBattleMetrics(t *testing.T) {
	capture := serveBattleMetrics(t, nil)
	r := &fakeResponder{}

	runS3aar(r, s3aarOptions("Tac1", "10NOV25", "11NOV25", "1800", "2000", 30, ""))

	q := capture.Query()
	if q == nil {
		t.Fatal("no BattleMetrics request was made")
	}

	for _, c := range []struct {
		param string
		want  time.Time
	}{
		{"start", time.Date(2025, time.November, 10, 18, 0, 0, 0, time.UTC)},
		{"stop", time.Date(2025, time.November, 11, 20, 0, 0, 0, time.UTC)},
	} {
		got, err := time.Parse(time.RFC3339, q.Get(c.param))
		if err != nil {
			t.Fatalf("%s=%q is not a parseable instant: %v", c.param, q.Get(c.param), err)
		}
		if !got.Equal(c.want) {
			t.Fatalf("%s = %s, want %s", c.param, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}
