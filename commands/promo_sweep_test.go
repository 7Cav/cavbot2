package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

var errBoom = errors.New("boom")

// fakeChannelSender records ChannelMessageSend calls; optional error queue.
type fakeChannelSender struct {
	mu       sync.Mutex
	channels []string
	bodies   []string
	files    [][]*discordgo.File
	errs     []error
}

func (f *fakeChannelSender) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = append(f.channels, channelID)
	f.bodies = append(f.bodies, data.Content)
	f.files = append(f.files, data.Files)
	return &discordgo.Message{}, popErr(&f.errs)
}

func TestPromoSweepConfig(t *testing.T) {
	// Channel, scope, and the kill switch are all constants; nothing is read
	// from the environment, so a stray variable can't redirect the sweep.
	t.Setenv("PROMO_SWEEP_CHANNEL_ID", "999")
	t.Setenv("PROMO_SWEEP_POSITIONS", "S6")
	cfg := newPromoSweepConfig()
	if cfg.ChannelID != promoSweepChannelID {
		t.Errorf("ChannelID = %q, want the constant to win over the env", cfg.ChannelID)
	}
	if len(cfg.Positions) != 1 || cfg.Positions[0] != promoSweepScope {
		t.Errorf("Positions = %v, want [%s]", cfg.Positions, promoSweepScope)
	}
	if promoSweepDisabled {
		t.Error("sweep should be enabled by default (kill switch off)")
	}
}

func TestNextPromoSweepFire(t *testing.T) {
	// From a Wednesday, next fire is the following Monday 09:00 UTC.
	wednesday := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	fire := nextPromoSweepFire(wednesday)
	if fire.Weekday() != time.Monday || fire.Hour() != 9 || fire.Minute() != 0 {
		t.Errorf("fire = %v, want Monday 09:00 UTC", fire)
	}
	if !fire.After(wednesday) {
		t.Error("fire must be strictly after now")
	}
	// Exactly at a fire moment → strictly the NEXT week (double-fire guard).
	atFire := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC) // a Monday 09:00
	next := nextPromoSweepFire(atFire)
	if !next.Equal(atFire.AddDate(0, 0, 7)) {
		t.Errorf("fire at boundary = %v, want exactly one week later", next)
	}
}

func TestRunPromoSweepPostsPerPosition(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	sender := &fakeChannelSender{}
	cfg := promoSweepConfig{ChannelID: "chan-1", Positions: []string{"ACD", "1-7"}}
	if err := runPromoSweep(sender, cfg, afsmRefDate); err != nil {
		t.Fatalf("runPromoSweep: %v", err)
	}
	if len(sender.bodies) != 2 {
		t.Fatalf("posted %d messages, want 2 (one per position)", len(sender.bodies))
	}
	for idx, ch := range sender.channels {
		if ch != "chan-1" {
			t.Errorf("message %d sent to %q, want chan-1", idx, ch)
		}
	}
	if !strings.Contains(sender.bodies[0], "Weekly promotion sweep") {
		t.Errorf("sweep header missing: %q", sender.bodies[0])
	}
	if !strings.Contains(sender.bodies[0], "Ready.R") {
		t.Errorf("candidate missing from sweep body: %q", sender.bodies[0])
	}
}

// A long candidate list posts one message with what fits and attaches the
// full report, rather than paging the channel.
func TestRunPromoSweepAttachesReportForLongLists(t *testing.T) {
	roster, profiles := manyEligiblePVTs(60)
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	sender := &fakeChannelSender{}
	cfg := promoSweepConfig{ChannelID: "chan-1", Positions: []string{"ACD"}}
	if err := runPromoSweep(sender, cfg, afsmRefDate); err != nil {
		t.Fatalf("runPromoSweep: %v", err)
	}
	if len(sender.bodies) != 1 {
		t.Fatalf("expected a single post, got %d", len(sender.bodies))
	}
	if len(sender.bodies[0]) >= 2000 {
		t.Errorf("message length %d exceeds Discord limit", len(sender.bodies[0]))
	}
	if !strings.Contains(sender.bodies[0], "Weekly promotion sweep") {
		t.Errorf("sweep header missing: %q", sender.bodies[0])
	}
	if !strings.Contains(sender.bodies[0], "more. Full list in the attached report") {
		t.Errorf("overflow notice missing: %q", sender.bodies[0])
	}
	if !strings.Contains(sender.bodies[0], "Member.000") {
		t.Errorf("inline list should show the first candidates: %q", sender.bodies[0])
	}
	if len(sender.files) != 1 || len(sender.files[0]) != 2 {
		t.Fatalf("expected CSV + HTML attachments, got %+v", sender.files)
	}
	// Every candidate reaches the reader via both reports.
	var htmlReport, csvReport string
	for _, fl := range sender.files[0] {
		if strings.HasSuffix(fl.Name, ".html") {
			htmlReport = readFileBody(t, fl)
		} else if strings.HasSuffix(fl.Name, ".csv") {
			csvReport = readFileBody(t, fl)
		}
	}
	if htmlReport == "" || csvReport == "" {
		t.Fatalf("missing csv or html attachment: %+v", sender.files[0])
	}
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("Member.%03d", i)
		if !strings.Contains(htmlReport, name) || !strings.Contains(csvReport, name) {
			t.Fatalf("candidate %s missing from a report", name)
		}
	}
}

// readFileBody drains a discordgo file attachment.
func readFileBody(t *testing.T, f *discordgo.File) string {
	t.Helper()
	var sb strings.Builder
	if _, err := io.Copy(&sb, f.Reader); err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	return sb.String()
}

func TestRunPromoSweepEmptyConfiguredRosterReportsBug(t *testing.T) {
	// A configured position is fixed input: empty roster → Sentry + a
	// warning post, per ADR 0002 (contrast with the slash command's
	// user-typed position).
	servePromoAPIWithRanks(t, utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}, nil, viiTestRanks())

	sender := &fakeChannelSender{}
	cfg := promoSweepConfig{ChannelID: "chan-1", Positions: []string{"GHOST"}}
	if err := runPromoSweep(sender, cfg, afsmRefDate); err != nil {
		t.Fatalf("runPromoSweep: %v", err)
	}
	if len(sender.bodies) != 1 || !strings.Contains(sender.bodies[0], "shouldn't happen") {
		t.Errorf("expected empty-roster bug warning, got %+v", sender.bodies)
	}
}

func TestRunPromoSweepSendFailureAborts(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	sender := &fakeChannelSender{errs: []error{errBoom}}
	cfg := promoSweepConfig{ChannelID: "chan-1", Positions: []string{"ACD", "1-7"}}
	err := runPromoSweep(sender, cfg, afsmRefDate)
	if err == nil {
		t.Fatal("expected error when channel send fails")
	}
	if len(sender.bodies) != 1 {
		t.Errorf("sends after failure = %d, want abort after first", len(sender.bodies))
	}
}

func TestCollectPromoCandidatesActiveDutyScope(t *testing.T) {
	// "active-duty" must hit /roster/ROSTER_TYPE_COMBAT/lite, not the fuzzy
	// position search.
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	rosterBody, _ := json.Marshal(roster)
	ranksBody, _ := json.Marshal(viiTestRanks())
	profileBody, _ := json.Marshal(profiles["Ready.R"])
	var combatHits, fuzzyHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/roster/ROSTER_TYPE_COMBAT/lite"):
			combatHits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(rosterBody)
		case strings.HasPrefix(r.URL.Path, "/milpacs/position/search/"):
			fuzzyHits++
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/milpacs/ranks"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(ranksBody)
		case strings.HasPrefix(r.URL.Path, "/milpacs/profile/username/Ready.R"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(profileBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	// Canonical spelling and the legacy hyphenated one must both route to the
	// combat roster.
	for _, scopeSpelling := range []string{"activeduty", "Active-Duty"} {
		scan, err := collectPromoCandidates(t.Context(), scopeSpelling, "", afsmRefDate, promoFilter{})
		if err != nil {
			t.Fatalf("collectPromoCandidates(%q): %v", scopeSpelling, err)
		}
		if len(scan.Candidates) != 1 || scan.Candidates[0].Username != "Ready.R" {
			t.Errorf("candidates for %q = %+v", scopeSpelling, scan.Candidates)
		}
	}
	if combatHits != 2 || fuzzyHits != 0 {
		t.Errorf("combat=%d fuzzy=%d, want 2/0", combatHits, fuzzyHits)
	}
}
