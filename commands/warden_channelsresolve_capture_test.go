package commands

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- runWardenPurge: GuildChannels fault capture split (#195) ---
//
// The GuildChannels lookup inside runWardenPurge must route a genuine system
// fault (5xx/transport) through the shared classifier and capture it to Sentry
// (per ADR 0001), while a 4xx stays a non-captured actionable message. The raw
// Discord response body must never reach the operator-facing message. This
// mirrors the GuildRoles split #194 closed one call site below.

// purgeChannelsGM builds a guild manager whose warden role resolves cleanly so
// the purge reaches the GuildChannels lookup, then fails that lookup with err.
func purgeChannelsGM(err error) *fakeGuildManager {
	return &fakeGuildManager{
		roles:        []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
		ChannelsErrs: []error{err},
	}
}

// A 5xx from GuildChannels during purge is a genuine Discord-side fault: it must
// capture to Sentry exactly once, show a body-free message, and never leak the
// raw Discord response body.
func TestRunWardenPurge_GuildChannels5xxCaptures(t *testing.T) {
	noOverwriteDelay(t)
	rec := &captureRecorder{}
	rec.install(t)
	gm := purgeChannelsGM(restError(http.StatusInternalServerError, 0, rawBodyMarker))
	f := &fakeResponder{}

	runWardenPurge(f, gm, wardenInteraction("guild-1"), "guild-1", "internal")

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("purge channels 5xx leaked the raw Discord body: %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx GuildChannels fault must capture to Sentry exactly once; got %d", rec.count)
	}
	if gm.countCalls("GuildRoleCreate") != 0 {
		t.Fatalf("must not recreate roles when channels are inaccessible; got %v", gm.Calls())
	}
	// #195 requires command+guild context on the capture.
	for key, want := range map[string]string{
		"command": "warden",
		"guild":   "guild-1",
	} {
		gotV, ok := kvValue(rec.lastKV, key)
		if !ok {
			t.Fatalf("capture context missing %q; got kv %v", key, rec.lastKV)
		}
		if gotV != want {
			t.Fatalf("capture context %q = %v, want %q", key, gotV, want)
		}
	}
}

// A transport (non-REST) failure from GuildChannels is also a system fault:
// capture once, no raw body leak.
func TestRunWardenPurge_GuildChannelsTransportErrorCaptures(t *testing.T) {
	noOverwriteDelay(t)
	rec := &captureRecorder{}
	rec.install(t)
	gm := purgeChannelsGM(errors.New("dial tcp: connection refused"))
	f := &fakeResponder{}

	runWardenPurge(f, gm, wardenInteraction("guild-1"), "guild-1", "internal")

	if rec.count != 1 {
		t.Fatalf("a transport GuildChannels fault must capture to Sentry exactly once; got %d", rec.count)
	}
}

// A 4xx from GuildChannels is an operator/config-fixable client fault: it must
// surface an actionable, body-free message and must NOT capture to Sentry.
func TestRunWardenPurge_GuildChannels4xxDoesNotCapture(t *testing.T) {
	noOverwriteDelay(t)
	rec := &captureRecorder{}
	rec.install(t)
	gm := purgeChannelsGM(restError(http.StatusForbidden, 50013, rawBodyMarker))
	f := &fakeResponder{}

	runWardenPurge(f, gm, wardenInteraction("guild-1"), "guild-1", "internal")

	got := lastEditContent(f.Calls())
	if rec.count != 0 {
		t.Fatalf("a 4xx GuildChannels fault must NOT capture to Sentry; got %d", rec.count)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("purge channels 4xx leaked the raw Discord body: %q", got)
	}
	if !strings.Contains(got, "❌ Failed to retrieve guild channels") {
		t.Fatalf("expected an actionable guild-channels failure message, got %q", got)
	}
	if gm.countCalls("GuildRoleCreate") != 0 {
		t.Fatalf("must not recreate roles when channels are inaccessible; got %v", gm.Calls())
	}
}
