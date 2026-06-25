package commands

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// errEditBoom is the synthetic post-ack edit failure used to drive the migrated
// edit-failure sites: a plain (non-RESTError) error so the path's only handling
// is the capture seam, not Discord-error classification.
var errEditBoom = errors.New("503 service unavailable")

// assertSingleCaptureNoRetry pins the issue-#191 contract for a migrated site:
// the failing Edit was issued exactly once, the helper did NOT retry through a
// second Edit or a fresh Respond, and the loss was captured to Sentry exactly
// once. placeholderResponds is how many legitimate pre-edit Respond calls the
// command makes (the immediate-ack placeholder = 1).
func assertSingleCaptureNoRetry(t *testing.T, calls []recordedCall, captures, placeholderResponds int) {
	t.Helper()
	if got := countMethod(calls, "Edit"); got != 1 {
		t.Fatalf("expected exactly 1 Edit (the failing call, no retry), got %d: %v", got, calls)
	}
	if got := countMethod(calls, "Respond"); got != placeholderResponds {
		t.Fatalf("expected exactly %d Respond (placeholder only, no HandleError retry), got %d: %v", placeholderResponds, got, calls)
	}
	if captures != 1 {
		t.Fatalf("expected exactly 1 Sentry capture, got %d", captures)
	}
}

// appCommandInteractionWithGuild builds an app-command interaction carrying the
// given guild id, so a migrated command's edit-failure site can be driven and
// the captured {command, guild_id} context asserted.
func appCommandInteractionWithGuild(guildID string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(opts...)
	i.GuildID = guildID
	return i
}

// captureDeferredEditFailure must capture exactly once with command + guild
// context read off the interaction, and must NOT touch the responder (no
// Respond/Edit retry): the helper only reports the already-lost reply.
func TestCaptureDeferredEditFailure_CapturesOnceWithContext(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	i := appCommandInteractionWithGuild("guild-7")

	captureDeferredEditFailure(i, "Milpac", errors.New("503 service unavailable"))

	if rec.count != 1 {
		t.Fatalf("expected exactly 1 Sentry capture, got %d", rec.count)
	}
	kvMap := kvToMap(rec.lastKV)
	if kvMap["command"] != "Milpac" {
		t.Fatalf("expected command=Milpac in capture context, got %v", kvMap["command"])
	}
	if kvMap["guild_id"] != "guild-7" {
		t.Fatalf("expected guild_id=guild-7 in capture context, got %v", kvMap["guild_id"])
	}
}

// --- /gamertag_search: final-edit failure ---

func TestRunGamertagSearch_EditFailureCapturesOnceNoRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(gamertagProfileJSON))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-gt", stringOption("gamertag", "SpecOps"))

	runGamertagSearch(f, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "GamertagSearch" || kv["guild_id"] != "g-gt" {
		t.Fatalf("expected command=GamertagSearch guild_id=g-gt, got %v", kv)
	}
}

// --- /milpac: final embed-edit failure ---

func TestRunMilpac_EditFailureCapturesOnceNoRetry(t *testing.T) {
	serveMilpacByDiscordID(t, milpacProfileWithSecondaries())

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-mp", userOption("user", "111"))

	runMilpac(f, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "Milpac" || kv["guild_id"] != "g-mp" {
		t.Fatalf("expected command=Milpac guild_id=g-mp, got %v", kv)
	}
}

// --- /loa: each of the three deferred-edit sites ---

// loaEditInteraction wires the guild id onto a /loa interaction.
func loaEditInteraction(guildID, position string) *discordgo.InteractionCreate {
	return appCommandInteractionWithGuild(guildID, stringOption("position", position))
}

// TestRunLoa_UnavailableEditFailure drives the unhealthy-cache edit site
// (loa.go:108): the edit of the "cache unavailable" message fails.
func TestRunLoa_UnavailableEditFailureCapturesOnceNoRetry(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	// Unhealthy cache → the unavailable-message edit site fires; tripwire API
	// proves we never reach the roster fetch.
	tripwireAPIServer(t)
	cache := &fakeLOAView{healthy: false, lastRefresh: loaRefDate}
	f := &fakeResponder{EditErrs: []error{errEditBoom}}

	runLoa(f, cache, loaRefDate, loaEditInteraction("g-loa1", "1-7"))

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "LOA" || kv["guild_id"] != "g-loa1" {
		t.Fatalf("expected command=LOA guild_id=g-loa1, got %v", kv)
	}
}

// TestRunLoa_NoResultsEditFailure drives the "no active/upcoming" edit site
// (loa.go:161): roster resolves but no member has an LOA entry.
func TestRunLoa_NoResultsEditFailureCapturesOnceNoRetry(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.NoLOA", "100"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	rec := &captureRecorder{}
	rec.install(t)

	cache := healthyView(map[string]utils.LOAEntry{}) // no entries → no results
	f := &fakeResponder{EditErrs: []error{errEditBoom}}

	runLoa(f, cache, loaRefDate, loaEditInteraction("g-loa2", "1-7"))

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "LOA" || kv["guild_id"] != "g-loa2" {
		t.Fatalf("expected command=LOA guild_id=g-loa2, got %v", kv)
	}
}

// TestRunLoa_ResultsEmbedEditFailure drives the final embed-edit site
// (loa.go:220): an active LOA renders, and the embed edit fails.
func TestRunLoa_ResultsEmbedEditFailureCapturesOnceNoRetry(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Active", "100"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	rec := &captureRecorder{}
	rec.install(t)

	cache := healthyView(map[string]utils.LOAEntry{
		"trooper.active": {
			Username:  "Trooper.Active",
			StartDate: loaRefDate.AddDate(0, 0, -5),
			EndDate:   loaRefDate.AddDate(0, 0, 5),
		},
	})
	f := &fakeResponder{EditErrs: []error{errEditBoom}}

	runLoa(f, cache, loaRefDate, loaEditInteraction("g-loa3", "1-7"))

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "LOA" || kv["guild_id"] != "g-loa3" {
		t.Fatalf("expected command=LOA guild_id=g-loa3, got %v", kv)
	}
}

// --- /awol: each of the three deferred-edit sites ---

// TestRunAwol_NoResultsEditFailure drives the "no users AWOL" edit site
// (awol.go:265).
func TestRunAwol_NoResultsEditFailureCapturesOnceNoRetry(t *testing.T) {
	// A member whose last forum post is recent → not AWOL → empty result set.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.Fresh", "100", "2026-05-14 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-awol1", stringOption("position", "1-7"))

	runAwol(f, healthyCache(nil), awolRefDate, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "Awol" || kv["guild_id"] != "g-awol1" {
		t.Fatalf("expected command=Awol guild_id=g-awol1, got %v", kv)
	}
}

// TestRunAwol_EmbedEditFailure drives the embed-edit site (awol.go:333).
func TestRunAwol_EmbedEditFailureCapturesOnceNoRetry(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-awol2", stringOption("position", "1-7"))

	runAwol(f, healthyCache(nil), awolRefDate, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "Awol" || kv["guild_id"] != "g-awol2" {
		t.Fatalf("expected command=Awol guild_id=g-awol2, got %v", kv)
	}
}

// TestRunAwol_FileSendFailure drives the file-attachment edit site
// (awol.go:443, sendAwolFile): force_file_output flips rendering to the file
// path and that follow-up send fails.
func TestRunAwol_FileSendFailureCapturesOnceNoRetry(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-awol3",
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	)

	runAwol(f, healthyCache(nil), awolRefDate, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "Awol" || kv["guild_id"] != "g-awol3" {
		t.Fatalf("expected command=Awol guild_id=g-awol3, got %v", kv)
	}
}

// --- /afsm: final-edit failure ---

func TestRunAFSM_EditFailureCapturesOnceNoRetry(t *testing.T) {
	// Empty department roster is rejected pre-edit (HandleError), so use a
	// roster with one ineligible member: the command reaches the final edit with
	// a "no eligible members" body.
	memberB := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.B"}, Primary: utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
		Secondary:  []utils.Position{{PositionTitle: "S1 Analytics Senior"}},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/200.jpg",
	}
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{"200": memberB},
	}
	profiles := map[string]utils.ProfileResponse{
		"Test.B": {User: memberB.User, UniformUrl: memberB.UniformUrl},
	}
	serveRosterAndProfiles(t, roster, http.StatusOK, profiles)

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-afsm", stringOption("department", "S6"))

	runAFSM(f, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "AFSM" || kv["guild_id"] != "g-afsm" {
		t.Fatalf("expected command=AFSM guild_id=g-afsm, got %v", kv)
	}
}

// --- /s6-it-check: final-edit failure ---

func TestRunS6ITCheck_EditFailureCapturesOnceNoRetry(t *testing.T) {
	memberB := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.B"}, UniformUrl: "https://7cav.us/data/roster_uniforms/0/200.jpg",
	}
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{"200": memberB},
	}
	profiles := map[string]utils.ProfileResponse{
		"Test.B": {
			User: memberB.User, UniformUrl: memberB.UniformUrl,
			Primary:   utils.Position{PositionTitle: "S6 Forums Staff"}, // S6 but no IT
			Secondary: []utils.Position{{PositionTitle: "S1 Analytics Senior"}},
		},
	}
	serveS6RosterAndProfiles(t, roster, http.StatusOK, profiles)

	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errEditBoom}}
	i := appCommandInteractionWithGuild("g-s6")

	runS6ITCheck(f, i)

	assertSingleCaptureNoRetry(t, f.Calls(), rec.count, 1)
	if kv := kvToMap(rec.lastKV); kv["command"] != "S6ITCheck" || kv["guild_id"] != "g-s6" {
		t.Fatalf("expected command=S6ITCheck guild_id=g-s6, got %v", kv)
	}
}
