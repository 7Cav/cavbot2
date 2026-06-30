package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The headline #216 scenario for the PUBLIC bulkadd member-LOOKUP path: Discord
// throws a 5xx storm during the per-entry name search, so every entry's lookup
// fails with the same signature before any role is even attempted. Today each
// entry fires its own captureError("Failed to search members", ...); the loop
// must collapse them into ONE Sentry event carrying the affected lookup count
// and a sample entry, while still listing every failure for the operator.
func TestRunWardenBulkAdd_LookupSameSignatureCollapsesToOneCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		// Every name search 500s with the same signature: a lookup-side storm.
		MembersSearchErrs: []error{
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob, carol"))

	// One root cause, one Sentry event — not one per entry.
	if rec.count != 1 {
		t.Fatalf("three same-signature lookup 500s must collapse to ONE capture; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 3 {
		t.Fatalf("the collapsed capture must carry affected_count=3; got %v (kv %v)", affected, rec.lastKV)
	}
	// Entries are processed in list order, so the sample is the first entry.
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "alice" {
		t.Fatalf("sample_user must be the first failed entry (alice); got %v", sample)
	}

	// The per-entry summary is unchanged: every failed lookup is still listed.
	got := lastEditContent(f.Calls())
	if strings.Count(got, "❌") != 3 {
		t.Fatalf("every failed lookup must still be listed in the summary; got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// Two entries fail their lookup with DISTINCT signatures in one bulkadd run — one
// 500, one 503. The collapse keys on signature, so each gets its own event with
// its own affected_count of 1; a 500 storm alongside a 503 must never fold into a
// single event.
func TestRunWardenBulkAdd_LookupMixedSignaturesCaptureOncePerSignature(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		MembersSearchErrs: []error{
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
			restError(http.StatusServiceUnavailable, 0, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob"))

	if rec.count != 2 {
		t.Fatalf("two distinct lookup signatures must capture once each (no folding); got %d", rec.count)
	}
	statuses := map[any]bool{}
	for _, kv := range rec.kvs {
		if affected, ok := kvValue(kv, "affected_count"); !ok || affected != 1 {
			t.Fatalf("each distinct-signature event must carry affected_count=1; got %v (kv %v)", affected, kv)
		}
		if _, ok := kvValue(kv, "sample_user"); !ok {
			t.Fatalf("each event must carry a sample_user; got kv %v", kv)
		}
		status, ok := kvValue(kv, "http_status")
		if !ok {
			t.Fatalf("each event must carry the http_status signature; got kv %v", kv)
		}
		statuses[status] = true
	}
	if len(statuses) != 2 {
		t.Fatalf("the two events must carry distinct http_status signatures; got %v", statuses)
	}
}

// A single-entry bulkadd whose one lookup 500s must still emit exactly one event,
// carrying affected_count=1 and the sample entry — the lower bound of the
// collapse, pinned directly on the payload so a miscount fails here.
func TestRunWardenBulkAdd_LookupSingleFaultCapturesOnceCountOne(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles:             []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		MembersSearchErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice"))

	if rec.count != 1 {
		t.Fatalf("a single lookup fault must capture exactly once; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 1 {
		t.Fatalf("the single collected lookup fault must carry affected_count=1; got %v (kv %v)", affected, rec.lastKV)
	}
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "alice" {
		t.Fatalf("the single collected lookup fault must carry sample_user=alice; got %v (kv %v)", sample, rec.lastKV)
	}
}

// The other lookup call site: mention/snowflake entries resolve through
// GuildMember (resolveMemberByID), not GuildMembersSearch. A 5xx storm on that
// path must collapse the same way and carry the resolved Discord ID as
// sample_user, proving the by-ID site routes to the collector too, not only the
// name-search site.
func TestRunWardenBulkAdd_LookupByIDFaultRoutesToCollector(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		// Every GuildMember lookup 500s with the same signature.
		MemberErrs: []error{
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "<@111>, <@222>"))

	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("mention entries must resolve by ID, never fall through to name search; got calls %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("two same-signature by-ID lookup 500s must collapse to ONE capture; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 2 {
		t.Fatalf("the collapsed by-ID capture must carry affected_count=2; got %v (kv %v)", affected, rec.lastKV)
	}
	// The first mention resolves to userID 111, the sample for the collapsed event.
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "111" {
		t.Fatalf("sample_user must be the first failed entry's resolved Discord ID (111); got %v", sample)
	}
}

// The non-captured lookup outcomes must stay listed for the operator AND never
// reach the collector: a not-in-server 404 (Unknown Member), a name with no
// match, and a name with too many matches are all operator-fixable, not system
// faults. Every other lookup-collapse test feeds only system faults, so without
// this a regression dropping the SystemFault gate (routing a 404/no-match into
// the collector) would fail no test.
func TestRunWardenBulkAdd_LookupNonCapturedOutcomesListedNotCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		// The mention resolves by ID and 404s as a genuine absence (Unknown Member).
		MemberErrs: []error{restError(http.StatusNotFound, discordgo.ErrCodeUnknownMember, rawBodyMarker)},
		// "common" matches two members (too many); "ghost" is absent from the map (no match).
		searchResults: map[string][]*discordgo.Member{
			"common": {
				{User: &discordgo.User{ID: "1", Username: "common"}},
				{User: &discordgo.User{ID: "2", Username: "common"}},
			},
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "<@111>, ghost, common"))

	if rec.count != 0 {
		t.Fatalf("not-in-server / no-match / too-many lookup outcomes must NOT capture to Sentry; got %d", rec.count)
	}
	// All three are still surfaced to the operator, never silently dropped.
	got := lastEditContent(f.Calls())
	if strings.Count(got, "❌") != 3 {
		t.Fatalf("every non-captured lookup failure must still be listed; got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// A lookup 4xx client fault in bulk (here a 400 on the name search) is
// operator-fixable, not a system fault: it must be listed for the operator and
// never reach the collector, the lookup-path mirror of the role-add 403 case.
func TestRunWardenBulkAdd_Lookup4xxListedNotCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles:             []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		MembersSearchErrs: []error{restError(http.StatusBadRequest, 50035, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice"))

	if rec.count != 0 {
		t.Fatalf("a 4xx lookup client fault must NOT capture to Sentry; got %d", rec.count)
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "❌") {
		t.Fatalf("the 4xx-faulted lookup must still be listed; got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// The split must leave the single /warden add lookup capturing inline: a member
// lookup 5xx on a single add still pages exactly once, NOT through a collector.
// The bulk-only collapse must not regress the single-call path to zero captures
// (a collector that never flushes) or to a collapsed payload.
func TestRunWardenAdd_MemberLookup5xxCapturesOnceInline(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	// No membersByID: the snowflake lookup hits the injected 5xx instead of resolving.
	gm := &fakeGuildManager{
		roles:      []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		MemberErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, wardenAddInteraction())

	if rec.count != 1 {
		t.Fatalf("a single /warden add lookup 5xx must capture inline exactly once; got %d", rec.count)
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(strings.ToLower(got), "try again shortly") {
		t.Fatalf("a transient member-lookup fault must keep the retry hint, got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// The remove path shares findGuildMember, so a single /warden remove lookup 5xx
// must likewise capture inline exactly once, behavior-identical to before the
// bulk-only collapse.
func TestRunWardenRemove_MemberLookup5xxCapturesOnceInline(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles:      []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		MemberErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, wardenRemoveInteraction())

	if rec.count != 1 {
		t.Fatalf("a single /warden remove lookup 5xx must capture inline exactly once; got %d", rec.count)
	}
	if got := lastEditContent(f.Calls()); strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}
