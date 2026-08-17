package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The headline #216 scenario for the PUBLIC bulkadd member-LOOKUP path: Discord
// throws a 5xx storm during the per-entry name search, so every entry's lookup
// fails with the same signature before any role is even attempted. The invariant
// this file guards: when every entry's lookup fails with the same signature, the
// bulk loop emits exactly one Sentry event carrying the affected count and a
// sample entry, while still listing every failure for the operator.
func TestRunWardenBulkAdd_LookupSameSignatureCollapsesToOneCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles:             []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles:             []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles:      []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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
		roles:      []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
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

// The headline #216 guarantee: the lookup collector and the role-add collector
// must stay SEPARATE, never folding a same-signature fault from the two phases
// into one event. Here one run hits both phases with the SAME signature {500,0}:
// entry "alice"'s member LOOKUP 500s (it never reaches a role add), while entry
// "bob" resolves but its ROLE ADD 500s. Two distinct root causes, one shared
// signature — so a single shared collector would collapse them to ONE event.
// Assert TWO events fire, carrying the two distinct flush messages, proving the
// build breaks if a future edit cross-wires the two collectors.
func TestRunWardenBulkAdd_LookupAndRoleAddSameSignatureDoNotFold(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
		// alice (processed first) is a name search that 500s on lookup.
		MembersSearchErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
		// bob resolves cleanly, then its role add 500s — same {500,0} signature.
		searchResults: map[string][]*discordgo.Member{
			"bob": {{User: &discordgo.User{ID: "222", Username: "bob"}}},
		},
		MemberRoleAddErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob"))

	// A lookup fault and a role-add fault sharing {500,0} must NOT fold: two
	// collectors, two events.
	if rec.count != 2 {
		t.Fatalf("a lookup fault and a role-add fault of the same signature must NOT fold into one event; got %d", rec.count)
	}
	// The two events must carry the two distinct collector flush messages, so a
	// cross-wiring (both phases feeding one collector) fails here.
	const lookupMsg = "Failed to look up guild member in bulk"
	const roleAddMsg = "Failed to add warden role in bulk"
	seen := map[string]bool{}
	for _, m := range rec.msgs {
		seen[m] = true
	}
	if !seen[lookupMsg] {
		t.Fatalf("expected the lookup collector's flush message %q among the events; got %v", lookupMsg, rec.msgs)
	}
	if !seen[roleAddMsg] {
		t.Fatalf("expected the role-add collector's flush message %q among the events; got %v", roleAddMsg, rec.msgs)
	}
	if len(seen) != 2 {
		t.Fatalf("the two events must carry two DISTINCT flush messages (lookup vs role-add); got %v", rec.msgs)
	}
}

// A bulk by-ID member lookup that 404s with a config fault (Unknown Guild 10004,
// a wrong GUILD_ID) IS a captured system fault: it must reach the lookup
// collector and page once, contrasted with a genuine absence (Unknown Member
// 10007) which stays uncaptured. This guards the lookup-by-ID classifier branch
// where a config-404 falls through `case class.NotFound` into
// `case class.SystemFault` — a narrowing edit that routed Unknown Guild back into
// the absent-member arm would silence the page and only fail here.
func TestRunWardenBulkAdd_LookupByIDConfig404CapturedAbsenceNot(t *testing.T) {
	t.Run("unknownGuild404Captured", func(t *testing.T) {
		rec := &captureRecorder{}
		rec.install(t)
		gm := &fakeGuildManager{
			roles:      []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
			MemberErrs: []error{restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)},
		}
		f := &fakeResponder{}

		runWarden(f, gm, bulkAddInteraction("internal", "<@111>"))

		if rec.count != 1 {
			t.Fatalf("a config-fault 404 (Unknown Guild) on the by-ID lookup must be collected and page once; got %d", rec.count)
		}
		// Routed through the lookup collector, not captured inline: the event
		// carries the collapsed payload's affected_count.
		if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 1 {
			t.Fatalf("the collected config-404 lookup fault must carry affected_count=1; got %v (kv %v)", affected, rec.lastKV)
		}
		if got := lastEditContent(f.Calls()); strings.Contains(got, rawBodyMarker) {
			t.Fatalf("must not leak the raw Discord body, got %q", got)
		}
	})

	t.Run("unknownMember404NotCaptured", func(t *testing.T) {
		rec := &captureRecorder{}
		rec.install(t)
		gm := &fakeGuildManager{
			roles:      []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
			MemberErrs: []error{restError(http.StatusNotFound, discordgo.ErrCodeUnknownMember, rawBodyMarker)},
		}
		f := &fakeResponder{}

		runWarden(f, gm, bulkAddInteraction("internal", "<@111>"))

		if rec.count != 0 {
			t.Fatalf("a genuine absence (Unknown Member 404) on the by-ID lookup must NOT capture; got %d", rec.count)
		}
		if got := lastEditContent(f.Calls()); !strings.Contains(got, "❌") {
			t.Fatalf("the absent member must still be listed for the operator; got %q", got)
		}
	})
}
