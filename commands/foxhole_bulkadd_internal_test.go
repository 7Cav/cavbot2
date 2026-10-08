package commands

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const defaultInternalRoleName = foxholeRoleBaseNameDefault + " Internal"

// foxholeBulkAddInternalInteraction builds a guild-context interaction carrying
// the chosen unit value, mirroring what the Choices-backed picker emits.
func foxholeBulkAddInternalInteraction(unit string) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(stringOption("unit", unit))
	i.GuildID = "guild-1"
	return i
}

// internalRoleGM is a fake guild that already has the Internal role, the
// precondition every successful run needs.
func internalRoleGM() *fakeGuildManager {
	return &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)},
	}
}

// liteMember is a roster entry constructor: a forum username plus its linked
// Discord ID (empty means no Discord connection on the milpac).
func liteMember(username, discordID string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{
		User:      utils.User{Username: username},
		DiscordID: discordID,
	}
}

func liteRoster(members ...utils.LiteProfileResponse) utils.LiteRosterResponse {
	profiles := make(map[string]utils.LiteProfileResponse, len(members))
	for idx, m := range members {
		profiles[strconvI(idx)] = m
	}
	return utils.LiteRosterResponse{LiteProfiles: profiles}
}

func strconvI(i int) string {
	return string(rune('a' + i))
}

// lastEditEmbed returns the first embed on the last Edit call, or nil.
func lastEditEmbed(calls []recordedCall) *discordgo.MessageEmbed {
	for idx := len(calls) - 1; idx >= 0; idx-- {
		c := calls[idx]
		if c.Method == "Edit" && c.Edit != nil && c.Edit.Embeds != nil && len(*c.Edit.Embeds) > 0 {
			return (*c.Edit.Embeds)[0]
		}
	}
	return nil
}

// The unit input is a validated, Choices-backed dropdown derived from the unit
// registry — never free text. This is the safety decision the whole command
// hangs on (see ADR 0009): the operator can only ever emit a value the registry
// already contains, so a careless short query can't substring-match the regiment.
func TestFoxholeBulkAddInternalDefinition_SingleUnitPickerFromRegistry(t *testing.T) {
	cmd := FoxholeBulkAddInternal(nil)

	if cmd.Definition.Name != "foxhole-bulkadd-internal" {
		t.Fatalf("command name = %q, want foxhole-bulkadd-internal", cmd.Definition.Name)
	}
	if cmd.Handler == nil {
		t.Fatal("command must have a handler")
	}

	// Exactly one option: the unit picker. A one-field surface is the point.
	if len(cmd.Definition.Options) != 1 {
		t.Fatalf("expected exactly one option (unit), got %d", len(cmd.Definition.Options))
	}
	unitOpt := cmd.Definition.Options[0]
	if unitOpt.Name != "unit" {
		t.Fatalf("option name = %q, want unit", unitOpt.Name)
	}
	if !unitOpt.Required {
		t.Fatal("the unit option must be required")
	}
	// Choices-backed dropdown, not free text.
	if len(unitOpt.Choices) == 0 {
		t.Fatal("the unit option must carry Choices (validated dropdown), not be free text")
	}
	// The choices are derived from the registry, so adding a unit later is one new
	// registry row with no command-definition change.
	if len(unitOpt.Choices) != len(validatedInternalUnits) {
		t.Fatalf("choices (%d) must be derived 1:1 from the registry (%d)", len(unitOpt.Choices), len(validatedInternalUnits))
	}
	foundDACD := false
	for _, choice := range unitOpt.Choices {
		if choice.Value == "D/ACD" {
			foundDACD = true
		}
	}
	if !foundDACD {
		t.Fatalf("expected a D/ACD choice derived from the registry, got %+v", unitOpt.Choices)
	}

	// No DefaultMemberPermissions in code: access is governed by Discord's
	// server-side command-permission override, matching the /foxhole convention.
	if cmd.Definition.DefaultMemberPermissions != nil {
		t.Fatal("must not set DefaultMemberPermissions in code (Foxhole convention)")
	}
}

// The registry is both the extension seam and the safety boundary: a lookup
// resolves a known unit to its author-controlled query, and rejects anything
// that isn't a registered value.
func TestLookupValidatedInternalUnit(t *testing.T) {
	unit, ok := LookupValidatedInternalUnit("D/ACD")
	if !ok {
		t.Fatal("expected D/ACD to resolve from the registry")
	}
	if unit.query != "D/ACD" {
		t.Fatalf("query = %q, want D/ACD", unit.query)
	}
	if unit.Label == "" {
		t.Fatal("a registered unit must carry a human label")
	}

	if _, ok := LookupValidatedInternalUnit("7"); ok {
		t.Fatal("an unregistered value must not resolve (the safety boundary)")
	}
	if _, ok := LookupValidatedInternalUnit(""); ok {
		t.Fatal("an empty value must not resolve")
	}
}

// Every registry row must carry a non-empty value, query, and label, and values
// must be unique. A genuine miss returns ("", false) from getOptionString and the
// caller rejects it on the boolean before any lookup, but a present-but-empty
// option (unit="") returns ("", true), so the guard passes and an empty-value
// registry row would resolve that input instead of it being rejected as "Unknown
// unit". A duplicate value would let the first matching row silently shadow a
// later one. Both are invisible to "add a row, no logic change", so this pins the
// invariant to fail loudly here.
func TestValidatedInternalUnits_RegistryRowsValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i, unit := range validatedInternalUnits {
		if unit.Value == "" {
			t.Fatalf("registry row %d has an empty value", i)
		}
		if unit.query == "" {
			t.Fatalf("registry row %d (%q) has an empty query", i, unit.Value)
		}
		if unit.Label == "" {
			t.Fatalf("registry row %d (%q) has an empty label", i, unit.Value)
		}
		if seen[unit.Value] {
			t.Fatalf("registry row %d has a duplicate value %q; the first match would shadow it", i, unit.Value)
		}
		seen[unit.Value] = true
	}
}

// rosterAddHeadings are the headings a roster add's reply lists troopers
// under.
var rosterAddHeadings = []string{rosterAddNotInDiscord, rosterAddNoDiscordLinked, rosterAddCouldNotAdd}

// troopersUnder returns which of troopers a roster add's reply lists under
// each group heading, keyed by the heading. A heading's group runs from its
// heading to the next.
func troopersUnder(reply string, troopers ...string) map[string][]string {
	groups := map[string][]string{}
	heading := ""
	for _, line := range strings.Split(reply, "\n") {
		if i := slices.IndexFunc(rosterAddHeadings, func(h string) bool { return strings.Contains(line, h) }); i >= 0 {
			heading = rosterAddHeadings[i]
			continue
		}
		if heading == "" {
			continue
		}
		for _, trooper := range troopers {
			if strings.Contains(line, trooper) {
				groups[heading] = append(groups[heading], trooper)
			}
		}
	}
	return groups
}

// assertTroopersUnder fails unless the roster add's reply lists troopers
// under each heading exactly as want does, and the troopers want leaves out
// under none.
func assertTroopersUnder(t *testing.T, reply string, want map[string][]string, troopers ...string) {
	t.Helper()
	got := troopersUnder(reply, troopers...)
	for _, heading := range rosterAddHeadings {
		if !slices.Equal(got[heading], want[heading]) {
			t.Errorf("the reply lists %v under %q, want %v; reply %q", got[heading], heading, want[heading], reply)
		}
	}
}

// embedMentions returns the IDs of the members the added embed names, in
// order, or none when the reply carries no embed.
func embedMentions(embed *discordgo.MessageEmbed) []string {
	if embed == nil {
		return nil
	}
	var ids []string
	for _, match := range regexp.MustCompile(`<@(\d+)>`).FindAllStringSubmatch(embed.Description, -1) {
		ids = append(ids, match[1])
	}
	return ids
}

// assertLeadNamesTheUnit fails unless the roster add's reply has one line
// naming the unit, and that line says the add was done.
func assertLeadNamesTheUnit(t *testing.T, reply string) {
	t.Helper()
	assertVerdict(t, reply, "D/ACD", verdictDone)
}

// Happy path: every roster member has a linked, in-guild Discord. Each one is
// added straight to the Internal role by ID (no member search), the run
// is acknowledged with a deferred ephemeral, and the reply names the unit
// and every member added.
func TestRunFoxholeBulkAddInternal_HappyPathAddsAllAndReportsCount(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
		liteMember("Trooper.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	// IDs come from the milpac, so the command adds directly and never searches.
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("must add by Discord ID directly, never searching; got %v", gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != 2 {
		t.Fatalf("expected 2 role adds, got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}

	// The right IDs reach the right role: every add carries the run's guild and
	// the resolved internal role id (r-int), and the user ids are exactly the
	// roster members' Discord ids (map order is nondeterministic, so compare as a
	// set).
	gotUserIDs := map[string]bool{}
	for _, add := range gm.roleAddCalls() {
		if add.guildID != "guild-1" {
			t.Fatalf("add must carry the run guild; got %q", add.guildID)
		}
		if add.roleID != "r-int" {
			t.Fatalf("add must target the resolved internal role id r-int; got %q", add.roleID)
		}
		gotUserIDs[add.userID] = true
	}
	for _, want := range []string{"111111111111111111", "222222222222222222"} {
		if !gotUserIDs[want] {
			t.Fatalf("expected roster member %s to be added by Discord id; got adds %+v", want, gm.roleAddCalls())
		}
	}

	calls := f.Calls()
	if len(calls) == 0 {
		t.Fatal("expected responder calls")
	}
	first := calls[0]
	if first.Method != "Respond" || first.Response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource {
		t.Fatalf("expected a deferred response first, got %+v", first)
	}
	if first.Response.Data == nil || first.Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatal("the defer must be ephemeral")
	}

	reply := lastEditContent(calls)
	assertLeadNamesTheUnit(t, reply)
	assertReplyNames(t, reply, defaultInternalRoleName)
	// A clean run is not a permissions problem: no advice for one.
	if strings.Contains(reply, adviceMissingPermissions) {
		t.Fatalf("a clean run must not carry the missing-permissions advice; got %q", reply)
	}
	assertTroopersUnder(t, reply, nil, "Trooper.A", "Trooper.B")

	added := embedMentions(lastEditEmbed(calls))
	slices.Sort(added)
	if want := []string{"111111111111111111", "222222222222222222"}; !slices.Equal(added, want) {
		t.Fatalf("the embed names %v as added, want %v", added, want)
	}

	// A clean run must never page Sentry.
	if rec.count != 0 {
		t.Fatalf("a clean happy-path run must not capture to Sentry; got %d", rec.count)
	}
}

// Idempotency: re-adding a member who already holds the role is a no-op on
// Discord's side (the role-add PUT returns success), so the fake returns nil and
// the member must count as added, not under any failure heading. This is the
// contract behind the "added or confirmed" wording — a nil error means the
// member has the role whether or not this call is what put it there.
func TestRunFoxholeBulkAddInternal_IdempotentReAddCountsAsConfirmed(t *testing.T) {
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Already.In", "111111111111111111"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// No queued error: the add of an already-present member succeeds (nil), exactly
	// as Discord's idempotent role-add PUT behaves.
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	reply := lastEditContent(f.Calls())
	assertLeadNamesTheUnit(t, reply)
	assertTroopersUnder(t, reply, nil, "Already.In")
	if got := embedMentions(lastEditEmbed(f.Calls())); !slices.Equal(got, []string{"111111111111111111"}) {
		t.Fatalf("the embed names %v as added, want the re-added member", got)
	}
}

// A member whose milpac carries no Discord connection (empty discordId) is
// listed by forum username and never sent to Discord — a mention would render as
// a dead raw ID, and there is nothing to add.
func TestRunFoxholeBulkAddInternal_NoDiscordLinkedListedNotAdded(t *testing.T) {
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
		liteMember("Linkless.B", ""),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	// Only the linked member reaches Discord; the link-less one is skipped.
	if gm.countCalls("GuildMemberRoleAdd") != 1 {
		t.Fatalf("expected exactly 1 role add (the linked member), got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	reply := lastEditContent(f.Calls())
	assertLeadNamesTheUnit(t, reply)
	assertTroopersUnder(t, reply, map[string][]string{rosterAddNoDiscordLinked: {"Linkless.B"}}, "Trooper.A", "Linkless.B")
	if got := embedMentions(lastEditEmbed(f.Calls())); !slices.Equal(got, []string{"111111111111111111"}) {
		t.Fatalf("the embed names %v as added, want only the linked member", got)
	}
}

// A member with a linked Discord whose add returns 404 (Unknown Member) is "not
// in this server": listed by forum username, not added, the run continues past
// it, and a 404 never captures to Sentry.
func TestRunFoxholeBulkAddInternal_NotInGuild404ListedNotAddedNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Present.A", "111111111111111111"),
		liteMember("Absent.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	gm.roleAddErrsByUser = map[string]error{"222222222222222222": restError(http.StatusNotFound, 10007, rawBodyMarker)}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	// Both members were attempted: the run continued past the 404.
	if gm.countCalls("GuildMemberRoleAdd") != 2 {
		t.Fatalf("expected both members attempted, got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	reply := lastEditContent(f.Calls())
	assertLeadNamesTheUnit(t, reply)
	assertTroopersUnder(t, reply, map[string][]string{rosterAddNotInDiscord: {"Absent.B"}}, "Present.A", "Absent.B")
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
	if rec.count != 0 {
		t.Fatalf("a 404 (not in server) must NOT capture to Sentry; got %d", rec.count)
	}
	if got := embedMentions(lastEditEmbed(f.Calls())); !slices.Equal(got, []string{"111111111111111111"}) {
		t.Fatalf("the embed names %v as added, want only the present member", got)
	}
}

// The headline #209 scenario: the resolved role is deleted between resolution
// and the add loop, so every add 404s with Unknown Role (10011). This must NOT
// be misread as the members being absent — that conflates a config fault with
// genuine absence. Both members are listed as not added, the faults collapse
// to one Sentry event, and the raw Discord body never leaks.
func TestRunFoxholeBulkAddInternal_DeletedRole404CapturedNotMisreportedAbsent(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Present.A", "111111111111111111"),
		liteMember("Present.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// Both present members 404 with Unknown Role: the role ID went stale.
	gm.MemberRoleAddErrs = []error{
		restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
		restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
	}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	reply := lastEditContent(f.Calls())
	assertTroopersUnder(t, reply, map[string][]string{rosterAddCouldNotAdd: {"Present.A", "Present.B"}}, "Present.A", "Present.B")
	// Both members hit the SAME fault signature (404 Unknown Role), so the loop
	// must collapse them into ONE Sentry event for the one root cause, not page
	// once per member (#214). The collapsed event carries the affected count and a
	// sample member, plus the unit tag the per-member captures used to carry.
	if rec.count != 1 {
		t.Fatalf("two same-signature stale-role 404s must collapse to ONE capture; got %d", rec.count)
	}
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 2 {
		t.Fatalf("the collapsed capture must carry affected_count=2; got %v (kv %v)", affected, rec.lastKV)
	}
	sample, ok := kvValue(rec.lastKV, "sample_user")
	if !ok {
		t.Fatalf("the collapsed capture must carry a sample_user; got kv %v", rec.lastKV)
	}
	// Roster iteration order is map-nondeterministic, so the sample is whichever
	// present member the loop reached first; it must be one of the two real IDs.
	if sample != "111111111111111111" && sample != "222222222222222222" {
		t.Fatalf("sample_user must be one of the failed members' Discord IDs; got %v", sample)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
}

// Two members fail with DISTINCT fault signatures in one run — one stale-role
// 404 (Unknown Role 10011), one transient 5xx. These are two different root
// causes, so the collapse must NOT fold them together: each signature gets its
// own Sentry event, with its own affected_count of 1. A 10011 storm alongside a
// transient 5xx must surface as two events, never one (#214).
func TestRunFoxholeBulkAddInternal_MixedSignaturesCaptureOncePerSignature(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Present.A", "111111111111111111"),
		liteMember("Present.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// One stale-role 404, one transient 5xx: two distinct signatures.
	gm.roleAddErrsByUser = map[string]error{
		"111111111111111111": restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker),
		"222222222222222222": restError(http.StatusInternalServerError, 0, rawBodyMarker),
	}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	// Two distinct signatures must produce exactly two captures, one per signature.
	if rec.count != 2 {
		t.Fatalf("two distinct fault signatures must capture once each (no folding); got %d", rec.count)
	}
	// Each event carries affected_count=1, and the two events carry distinct
	// signatures (different http_status), proving the collapse keys on signature.
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
	// Both members are still listed for the operator, unchanged by the collapse.
	assertTroopersUnder(t, lastEditContent(f.Calls()), map[string][]string{rosterAddCouldNotAdd: {"Present.A", "Present.B"}}, "Present.A", "Present.B")
}

// A non-404 client fault on an add (here a 403 — bot lacks Manage Roles or the
// role sits above it) is still listed, never silently dropped, but it is an
// operator-fixable condition so it must NOT capture to Sentry. The reply
// adds the advice for missing permissions, naming Manage Roles and the role
// the bot's own role must sit above.
func TestRunFoxholeBulkAddInternal_PerMemberClientFaultListedNotCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Forbidden.A", "111111111111111111"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	gm.MemberRoleAddErrs = []error{restError(http.StatusForbidden, 50013, rawBodyMarker)}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	reply := lastEditContent(f.Calls())
	assertTroopersUnder(t, reply, map[string][]string{rosterAddCouldNotAdd: {"Forbidden.A"}}, "Forbidden.A")
	if rec.count != 0 {
		t.Fatalf("a 4xx client fault must NOT capture to Sentry; got %d", rec.count)
	}
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
	if !strings.Contains(reply, adviceMissingPermissions) {
		t.Fatalf("a 403 must carry the missing-permissions advice; got %q", reply)
	}
	assertReplyNames(t, reply, "Manage Roles", defaultInternalRoleName)
}

// A genuine per-member fault (5xx) is listed, sent to Sentry tagged with the
// unit value, and the run continues past it so the other members still get
// processed.
func TestRunFoxholeBulkAddInternal_PerMemberFaultCapturedAndRunContinues(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Ok.A", "111111111111111111"),
		liteMember("Boom.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	gm.roleAddErrsByUser = map[string]error{"222222222222222222": restError(http.StatusInternalServerError, 0, rawBodyMarker)}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	// Both attempted: the run continued past the per-member 5xx.
	if gm.countCalls("GuildMemberRoleAdd") != 2 {
		t.Fatalf("expected both members attempted, got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx per-member fault must capture to Sentry exactly once; got %d", rec.count)
	}
	// Pin the "single fault → count 1" acceptance criterion directly on the payload,
	// not just transitively via rec.count: the one collected fault carries an
	// affected_count of 1 and the faulted member as its sample, so a regression
	// that miscounts attempts or drops the sample fails here rather than only in
	// the multi-fault tests.
	if affected, ok := kvValue(rec.lastKV, "affected_count"); !ok || affected != 1 {
		t.Fatalf("the single collected fault must carry affected_count=1; got %v (kv %v)", affected, rec.lastKV)
	}
	if sample, ok := kvValue(rec.lastKV, "sample_user"); !ok || sample != "222222222222222222" {
		t.Fatalf("sample_user must be the faulted member's Discord ID; got %v (kv %v)", sample, rec.lastKV)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	reply := lastEditContent(f.Calls())
	assertTroopersUnder(t, reply, map[string][]string{rosterAddCouldNotAdd: {"Boom.B"}}, "Ok.A", "Boom.B")
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
	// A pure-5xx fault is not a permissions problem, so the missing-permissions
	// advice must NOT be added.
	if strings.Contains(reply, adviceMissingPermissions) {
		t.Fatalf("a 5xx fault must not carry the missing-permissions advice; got %q", reply)
	}
	if got := embedMentions(lastEditEmbed(f.Calls())); !slices.Equal(got, []string{"111111111111111111"}) {
		t.Fatalf("the embed names %v as added, want only the member whose add went through", got)
	}
}

// A generic non-403/404 4xx on an add (here a 400 with a non-permission code) is
// the classifier's fall-through client fault. Like the 403 it is listed and not
// captured, but unlike the 403 it carries no missing-permissions signal, so the
// reply must NOT add that advice. The clean member still counts as added, so
// the run is realistic.
func TestRunFoxholeBulkAddInternal_PerMemberGeneric4xxListedNotCapturedNoHint(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Ok.A", "111111111111111111"),
		liteMember("Reject.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// Reject.B's add 400s with a non-permission code (50035 Invalid Form Body),
	// hitting the classifier's generic 4xx arm — not NotFound, not SystemFault,
	// not MissingPermissions.
	gm.roleAddErrsByUser = map[string]error{"222222222222222222": restError(http.StatusBadRequest, 50035, rawBodyMarker)}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	reply := lastEditContent(f.Calls())
	assertLeadNamesTheUnit(t, reply)
	assertTroopersUnder(t, reply, map[string][]string{rosterAddCouldNotAdd: {"Reject.B"}}, "Ok.A", "Reject.B")
	// A generic 4xx is a client fault, not a system fault: it must not page Sentry.
	if rec.count != 0 {
		t.Fatalf("a generic 4xx client fault must NOT capture to Sentry; got %d", rec.count)
	}
	// It is not a 403 either, so no missing-permissions advice may be added.
	if strings.Contains(reply, adviceMissingPermissions) {
		t.Fatalf("a non-403 4xx must not carry the missing-permissions advice; got %q", reply)
	}
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
}

// Every group at once: one clean add, one linked-but-absent (404), one with
// no Discord link, and one genuine fault (5xx). Each trooper sits under its
// own group's heading, and the clean one under none.
func TestRunFoxholeBulkAddInternal_AllGroupsCoexist(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Added.A", "111111111111111111"),
		liteMember("Absent.B", "222222222222222222"),
		liteMember("Linkless.C", ""),
		liteMember("Boom.D", "333333333333333333"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	gm.roleAddErrsByUser = map[string]error{
		"222222222222222222": restError(http.StatusNotFound, 10007, rawBodyMarker),
		"333333333333333333": restError(http.StatusInternalServerError, 0, rawBodyMarker),
	}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	reply := lastEditContent(f.Calls())
	assertLeadNamesTheUnit(t, reply)
	assertTroopersUnder(t, reply, map[string][]string{
		rosterAddNotInDiscord:    {"Absent.B"},
		rosterAddNoDiscordLinked: {"Linkless.C"},
		rosterAddCouldNotAdd:     {"Boom.D"},
	}, "Added.A", "Absent.B", "Linkless.C", "Boom.D")
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
	// Only the 5xx is a genuine fault; it captures exactly once.
	if rec.count != 1 {
		t.Fatalf("only the 5xx fault should capture to Sentry; got %d", rec.count)
	}
}

// The picker feeding a registry feeding a fixed query makes this fixed input, so
// an empty roster is structurally a bug, not "the unit is empty" (ADR 0002). It
// must change nothing, warn the operator, and capture tagged with the unit
// value — never a silent "added 0".
func TestRunFoxholeBulkAddInternal_EmptyRosterCapturedNothingChanged(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{},
	}, http.StatusOK, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("an empty roster must change nothing; got adds %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("an empty roster from a validated unit must capture exactly once; got %d", rec.count)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the empty-roster capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	assertVerdict(t, lastEditContent(f.Calls()), "D/ACD", verdictLeftOver)
	// No success embed for a non-result.
	if embed := lastEditEmbed(f.Calls()); embed != nil {
		t.Fatalf("an empty roster must not produce a success embed; got %+v", embed)
	}
}

// A failed roster fetch is a genuine milpac fault on fixed input: it must be
// captured (tagged with the unit value), surfaced to the operator, and add
// nobody.
func TestRunFoxholeBulkAddInternal_RosterFetchFaultCapturedNoAdds(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, utils.LiteRosterResponse{}, http.StatusInternalServerError, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("a failed roster fetch must not add anyone; got %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a genuine milpac fault must capture exactly once; got %d", rec.count)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the roster-fetch capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	assertVerdict(t, lastEditContent(f.Calls()), "D/ACD", verdictFailed)
}

// A DM-shaped interaction (no GuildID) is refused before any defer, guild
// call, or roster fetch.
func TestRunFoxholeBulkAddInternal_MissingGuildRejectedBeforeAnything(t *testing.T) {
	tripwireAPIServer(t)
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("unit", "D/ACD")) // no GuildID

	runFoxholeBulkAddInternal(f, gm, nil, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("DM-context must not touch the guild; got %v", gm.Calls())
	}
	assertVerdict(t, lastResponseContent(f.Calls()), "", verdictFailed)
}

// A crafted interaction carrying a value absent from the registry is refused
// before any guild call or roster fetch — the registry is the safety
// boundary — and the refusal echoes the value back.
func TestRunFoxholeBulkAddInternal_UnknownUnitRejectedBeforeAnything(t *testing.T) {
	tripwireAPIServer(t)
	gm := internalRoleGM()
	f := &fakeResponder{}
	i := foxholeBulkAddInternalInteraction("7") // a careless short query, not in the registry

	runFoxholeBulkAddInternal(f, gm, nil, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("an unregistered unit must be rejected before any guild call; got %v", gm.Calls())
	}
	assertVerdict(t, lastResponseContent(f.Calls()), "7", verdictFailed)
}

// A (malformed) interaction with no unit option is refused before any guild
// call or roster fetch.
func TestRunFoxholeBulkAddInternal_MissingUnitOptionRejected(t *testing.T) {
	tripwireAPIServer(t)
	gm := internalRoleGM()
	f := &fakeResponder{}
	i := fakeAppCommandInteraction() // no unit option
	i.GuildID = "guild-1"

	runFoxholeBulkAddInternal(f, gm, nil, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("a missing unit must short-circuit before any guild call; got %v", gm.Calls())
	}
	assertVerdict(t, lastResponseContent(f.Calls()), "", verdictFailed)
}

// When the deferred-ephemeral acknowledge fails, the run bails before resolving
// roles or fetching the roster, rather than pressing on with no live response.
func TestRunFoxholeBulkAddInternal_DeferFailureBailsBeforeWork(t *testing.T) {
	tripwireAPIServer(t)
	gm := internalRoleGM()
	f := &fakeResponder{RespondErrs: []error{errFirstRespond}}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildRoles") != 0 {
		t.Fatalf("a failed defer must bail before role resolution; got %v", gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("a failed defer must add nobody; got %v", gm.Calls())
	}
}

// When the Internal role is absent from the guild, the run names the role it
// couldn't find and never fetches the roster or adds anyone.
func TestRunFoxholeBulkAddInternal_InternalRoleMissingSurfacedNoFetch(t *testing.T) {
	tripwireAPIServer(t)
	gm := &fakeGuildManager{roles: []*discordgo.Role{guildRole("x", "Some Other Role")}}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("must not add anyone when the role is missing; got %v", gm.Calls())
	}
	assertVerdict(t, lastEditContent(f.Calls()), defaultInternalRoleName, verdictFailed)
}

// A 5xx from GuildRoles during role resolution is a genuine Discord fault: it
// captures once, shows a body-free retry message, and never fetches the roster.
func TestRunFoxholeBulkAddInternal_RoleResolve5xxCapturedNoFetch(t *testing.T) {
	tripwireAPIServer(t)
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{RolesErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	if rec.count != 1 {
		t.Fatalf("a 5xx GuildRoles fault must capture exactly once; got %d", rec.count)
	}
	reply := lastEditContent(f.Calls())
	if strings.Contains(reply, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", reply)
	}
	assertAdvice(t, reply, adviceTransient)
}

// manyTroopers is a roster of n troopers, Trooper.000 up, each with an
// 18-digit Discord ID when linked is true and none otherwise.
func manyTroopers(n int, linked bool) utils.LiteRosterResponse {
	profiles := make(map[string]utils.LiteProfileResponse, n)
	for i := range n {
		id := ""
		if linked {
			id = fmt.Sprintf("1%017d", i)
		}
		profiles[fmt.Sprintf("p%d", i)] = liteMember(fmt.Sprintf("Trooper.%03d", i), id)
	}
	return utils.LiteRosterResponse{LiteProfiles: profiles}
}

// A group too big for one message would push the reply past Discord's
// 2000-character message limit and fail the edit. The reply stays within the
// limit and keeps its lead line naming the unit.
func TestRunFoxholeBulkAddInternal_ReplyStaysWithinDiscordsMessageLimit(t *testing.T) {
	serveRosterAndProfiles(t, manyTroopers(400, false), http.StatusOK, nil)
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, internalRoleGM(), nil, foxholeBulkAddInternalInteraction("D/ACD"))

	reply := lastEditContent(f.Calls())
	if n := len(reply); n > 2000 {
		t.Fatalf("the reply is %d characters, over Discord's 2000", n)
	}
	assertLeadNamesTheUnit(t, reply)
}

// addedEmbed runs a roster add of total linked troopers and returns the
// members its added embed names, how many more its last line counts, and
// the lines that name members.
func addedEmbed(t *testing.T, total int) (named []string, left int, mentionLines string) {
	t.Helper()
	serveRosterAndProfiles(t, manyTroopers(total, true), http.StatusOK, nil)
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, internalRoleGM(), nil, foxholeBulkAddInternalInteraction("D/ACD"))

	embed := lastEditEmbed(f.Calls())
	if embed == nil {
		t.Fatal("the reply carries no embed of the members added")
	}
	added := map[string]bool{}
	for _, p := range manyTroopers(total, true).LiteProfiles {
		added[p.DiscordID] = true
	}
	named = embedMentions(embed)
	seen := map[string]bool{}
	for _, id := range named {
		if !added[id] || seen[id] {
			t.Errorf("the embed names %s, which is no added member or is named twice", id)
		}
		seen[id] = true
	}
	lines := strings.Split(embed.Description, "\n")
	if _, err := fmt.Sscanf(lines[len(lines)-1], addedEmbedMore, &left); err == nil {
		lines = lines[:len(lines)-1]
	}
	return named, left, strings.Join(lines, "\n")
}

// A roster add's embed names every member it added when they fit.
func TestRunFoxholeBulkAddInternal_EmbedNamesEveryMemberAddedWhenTheyFit(t *testing.T) {
	named, left, _ := addedEmbed(t, 5)

	if len(named) != 5 || left != 0 {
		t.Errorf("the embed names %d members and counts %d more, want all 5 named", len(named), left)
	}
}

// When the members added don't fit, the embed names as many as fit in
// Discord's 4096-character description, each one added and none twice, and
// its last line counts the rest, so the members named and the members
// counted make up everyone added.
func TestRunFoxholeBulkAddInternal_EmbedCountsTheMembersItCantName(t *testing.T) {
	named, left, mentionLines := addedEmbed(t, 200)

	if left == 0 || len(named)+left != 200 {
		t.Errorf("the embed names %d members and counts %d more, want the rest of the 200 counted", len(named), left)
	}
	if n := len(mentionLines); n > 4096 {
		t.Errorf("the embed's mentions run %d characters, over Discord's 4096", n)
	}
}

// The command is wired into the registry so Discord registers it and routes its
// interactions to the handler.
func TestRegistry_RegistersFoxholeBulkAddInternal(t *testing.T) {
	reg := NewRegistry(nil, nil)

	registered := false
	for _, def := range reg.GetCommands() {
		if def.Name == "foxhole-bulkadd-internal" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("foxhole-bulkadd-internal must be registered in the command registry")
	}
	if _, ok := reg.GetHandler("foxhole-bulkadd-internal"); !ok {
		t.Fatal("foxhole-bulkadd-internal must resolve a handler from the registry")
	}
}
