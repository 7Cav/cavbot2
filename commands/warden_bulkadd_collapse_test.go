package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// bulkAddInteraction builds a /warden bulkadd interaction for the given scope
// and comma-separated discordname list.
func bulkAddInteraction(scope, names string) *discordgo.InteractionCreate {
	return wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", scope),
		stringOption("discordname", names),
	)
}

// searchMember is a one-result GuildMembersSearch entry keyed by its query.
func searchMember(query, id string) (string, []*discordgo.Member) {
	return query, []*discordgo.Member{{User: &discordgo.User{ID: id, Username: query}}}
}

// The headline #214 scenario for the PUBLIC loop: the resolved role is deleted
// between resolution and the add loop, so every member's add 404s with the same
// Unknown Role (10011) signature. Today each member fires its own capture; the
// loop must collapse them into ONE Sentry event carrying the affected attempt
// count and a sample member, while still listing every failure for the operator.
func TestRunWardenBulkAdd_SameSignatureCollapsesToOneCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	aliceQ, aliceM := searchMember("alice", "111")
	bobQ, bobM := searchMember("bob", "222")
	carolQ, carolM := searchMember("carol", "333")
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			aliceQ: aliceM, bobQ: bobM, carolQ: carolM,
		},
		// All three adds 404 with Unknown Role: the role ID went stale mid-run.
		MemberRoleAddErrs: []error{
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob, carol"))

	// One root cause, one Sentry event — not one per member.
	if rec.count != 1 {
		t.Fatalf("three same-signature stale-role 404s must collapse to ONE capture; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 3 {
		t.Fatalf("the collapsed capture must carry affected_count=3; got %v (kv %v)", affected, rec.lastKV)
	}
	// Entries are processed in list order, so the sample is the first member.
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "111" {
		t.Fatalf("sample_user must be the first failed member's Discord ID (111); got %v", sample)
	}

	// The per-member summary is unchanged: every failure is still listed.
	got := lastEditContent(f.Calls())
	for _, name := range []string{"alice", "bob", "carol"} {
		if !strings.Contains(got, name) {
			t.Fatalf("every failed member must still be listed in the summary; missing %q in %q", name, got)
		}
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// The public mirror of the internal client-fault tests: in one /warden bulkadd
// run, one member's add hits a 403 (missing permissions) and another hits a
// not-in-server 404 (Unknown Member 10007). Both are captured-NEVER client
// faults, so the public SystemFault gate must keep them out of the collector —
// nothing pages — while still listing every faulted member for the operator.
// Every other public collapse test feeds only system faults, so without this a
// regression dropping the public SystemFault gate would fail no test.
func TestRunWardenBulkAdd_ClientFaultsListedNotCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	aliceQ, aliceM := searchMember("alice", "111")
	bobQ, bobM := searchMember("bob", "222")
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			aliceQ: aliceM, bobQ: bobM,
		},
		// alice draws a 403 (missing perms), bob a not-in-server 404 (Unknown
		// Member): neither is a system fault, so neither may reach the collector.
		MemberRoleAddErrs: []error{
			restError(http.StatusForbidden, 50013, rawBodyMarker),
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownMember, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob"))

	// Client faults must never page on-call.
	if rec.count != 0 {
		t.Fatalf("public bulkadd client faults (403, not-in-server 404) must NOT capture to Sentry; got %d", rec.count)
	}
	// Both faulted members are still surfaced, never silently dropped.
	got := lastEditContent(f.Calls())
	for _, name := range []string{"alice", "bob"} {
		if !strings.Contains(got, name) {
			t.Fatalf("every client-faulted member must still be listed in the summary; missing %q in %q", name, got)
		}
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// Two members fail with DISTINCT signatures in one public bulkadd run — one
// stale-role 404, one transient 5xx. The collapse must key on signature, so each
// gets its own event with its own affected_count of 1; a 10011 storm alongside a
// 5xx must never fold into a single event (#214).
func TestRunWardenBulkAdd_MixedSignaturesCaptureOncePerSignature(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	aliceQ, aliceM := searchMember("alice", "111")
	bobQ, bobM := searchMember("bob", "222")
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			aliceQ: aliceM, bobQ: bobM,
		},
		MemberRoleAddErrs: []error{
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob"))

	if rec.count != 2 {
		t.Fatalf("two distinct fault signatures must capture once each (no folding); got %d", rec.count)
	}
	statuses := map[any]bool{}
	for _, kv := range rec.kvs {
		if affected, ok := kvValue(kv, "affected_count"); !ok || affected != 1 {
			t.Fatalf("each distinct-signature event must carry affected_count=1; got %v (kv %v)", affected, kv)
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

// Two faults in one run share the HTTP status (404) but differ in Discord code —
// Unknown Role (10011) and Unknown Guild (10004). The collapse keys on the
// (status, Discord code) pair, so these are two distinct root causes and must NOT
// fold into one event. Every other multi-signature test distinguishes only by
// http_status, so a regression collapsing the key to status-only would still pass
// them; and the emitted discord_code payload value is otherwise never asserted.
// This pins both: two captures, carrying distinct discord_code values 10011 and
// 10004 (#214).
func TestRunWardenBulkAdd_SameStatusDifferentCodeCapturesPerCode(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	aliceQ, aliceM := searchMember("alice", "111")
	bobQ, bobM := searchMember("bob", "222")
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			aliceQ: aliceM, bobQ: bobM,
		},
		// Same 404 status, different Discord code: a stale role vs a wrong guild.
		MemberRoleAddErrs: []error{
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
			restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("internal", "alice, bob"))

	// Same status, different code: two distinct signatures, never folded to one.
	if rec.count != 2 {
		t.Fatalf("two 404s with different Discord codes must capture once each (no status-only folding); got %d", rec.count)
	}
	codes := map[any]bool{}
	for _, kv := range rec.kvs {
		// Both faults share the 404 status: the discriminator is the Discord code.
		if status, ok := kvValue(kv, "http_status"); !ok || status != http.StatusNotFound {
			t.Fatalf("both events must carry the shared 404 http_status; got %v (kv %v)", status, kv)
		}
		code, ok := kvValue(kv, "discord_code")
		if !ok {
			t.Fatalf("each event must carry the discord_code signature value; got kv %v", kv)
		}
		codes[code] = true
	}
	// The two captures must carry the two distinct codes, not a single folded one.
	if !codes[discordgo.ErrCodeUnknownRole] || !codes[discordgo.ErrCodeUnknownGuild] {
		t.Fatalf("the two events must carry distinct discord_code values 10011 and 10004; got %v", codes)
	}
}

// /warden bulkadd with scope "both" adds two roles per member, so affected_count
// must count member-ROLE attempts, not distinct members. One member whose two
// role-adds both fail with the same signature must collapse to one event with
// affected_count=2, sampled to that member (#214).
func TestRunWardenBulkAdd_BothScopeCountsRoleAttemptsNotMembers(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	aliceQ, aliceM := searchMember("alice", "111")
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{
			wardenRole("r-int", wardenRoleBaseNameDefault+" Internal"),
			wardenRole("r-ext", wardenRoleBaseNameDefault+" External"),
		},
		searchResults: map[string][]*discordgo.Member{aliceQ: aliceM},
		// Both the Internal and External adds for the one member fail 5xx.
		MemberRoleAddErrs: []error{
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
			restError(http.StatusInternalServerError, 0, rawBodyMarker),
		},
	}
	f := &fakeResponder{}

	runWarden(f, gm, bulkAddInteraction("both", "alice"))

	if gm.countCalls("GuildMemberRoleAdd") != 2 {
		t.Fatalf("scope 'both' must attempt two role adds for the member; got %d", gm.countCalls("GuildMemberRoleAdd"))
	}
	if rec.count != 1 {
		t.Fatalf("two same-signature role attempts for one member must collapse to ONE capture; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 2 {
		t.Fatalf("affected_count must count member-role attempts (2), not distinct members (1); got %v", affected)
	}
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "111" {
		t.Fatalf("sample_user must be the failed member's Discord ID (111); got %v", sample)
	}
}

// The zero-value faultCollector must be safe to record into without going through
// newFaultCollector(): recordSystemFault lazy-inits the entries map, so a future
// caller that builds the collector as a plain struct literal can never nil-panic
// on the first record (the map-write that a nil map would panic on). A flush of
// what it collected still emits exactly that one fault.
func TestFaultCollector_ZeroValueRecordsWithoutNilPanic(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	var fc faultCollector // zero value: entries is a nil map
	fc.recordSystemFault(restError(http.StatusInternalServerError, 0, rawBodyMarker), "111")
	fc.flush("boom", "command", "warden")

	if rec.count != 1 {
		t.Fatalf("a zero-value collector must record and flush one fault; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 1 {
		t.Fatalf("the flushed fault must carry affected_count=1; got %v (kv %v)", affected, rec.lastKV)
	}
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "111" {
		t.Fatalf("the flushed fault must carry the sample_user; got %v (kv %v)", sample, rec.lastKV)
	}
}

// The public bulkadd flush is deferred immediately after the collector is built,
// so a panic between recording a fault and the normal end of the loop cannot
// silently discard pending captures — the exact per-member silent drop #214
// fixes. Here alice's add 5xxs and is recorded, then a member with a nil User
// makes the role-add path panic on member.User.ID; the deferred flush must still
// emit alice's capture during unwinding. A plain post-loop flush statement would
// be skipped by the panic and lose it.
func TestRunWardenBulkAdd_PanicMidLoopStillFlushesPendingCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	aliceQ, aliceM := searchMember("alice", "111")
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			aliceQ: aliceM,
			// A member with a nil User: dereferencing member.User.ID in the role-add
			// path panics, standing in for any unexpected mid-loop blowup.
			"boom": {{User: nil}},
		},
		// Only alice's add is reached; she 5xxs and is recorded before boom panics.
		MemberRoleAddErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}

	func() {
		defer func() { _ = recover() }() // swallow the deliberate mid-loop panic
		runWarden(f, gm, bulkAddInteraction("internal", "alice, boom"))
	}()

	// The deferred flush ran during panic unwinding, so alice's pending system-fault
	// capture survived rather than being silently dropped.
	if rec.count != 1 {
		t.Fatalf("a panic mid-loop must still flush the pending capture via defer; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 1 {
		t.Fatalf("the flushed capture must carry affected_count=1; got %v (kv %v)", affected, rec.lastKV)
	}
}
