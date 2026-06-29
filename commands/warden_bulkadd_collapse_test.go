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
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
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
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
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
			wardenRole("r-int", wardenRoleBaseName+" Internal"),
			wardenRole("r-ext", wardenRoleBaseName+" External"),
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
