package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const wardenInternalRoleName = wardenRoleBaseName + " Internal"

// wardenBulkAddInternalInteraction builds a guild-context interaction carrying
// the chosen unit value, mirroring what the Choices-backed picker emits.
func wardenBulkAddInternalInteraction(unit string) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(stringOption("unit", unit))
	i.GuildID = "guild-1"
	return i
}

// internalRoleGM is a fake guild that already has the Verified Warden Internal
// role, the precondition every successful run needs.
func internalRoleGM() *fakeGuildManager {
	return &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenInternalRoleName)},
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
func TestWardenBulkAddInternalDefinition_SingleUnitPickerFromRegistry(t *testing.T) {
	cmd := WardenBulkAddInternal()

	if cmd.Definition.Name != "warden-bulkadd-internal" {
		t.Fatalf("command name = %q, want warden-bulkadd-internal", cmd.Definition.Name)
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
	if len(unitOpt.Choices) != len(wardenInternalUnits) {
		t.Fatalf("choices (%d) must be derived 1:1 from the registry (%d)", len(unitOpt.Choices), len(wardenInternalUnits))
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
	// server-side command-permission override, matching the warden convention.
	if cmd.Definition.DefaultMemberPermissions != nil {
		t.Fatal("must not set DefaultMemberPermissions in code (warden convention)")
	}
}

// The registry is both the extension seam and the safety boundary: a lookup
// resolves a known unit to its author-controlled query, and rejects anything
// that isn't a registered value.
func TestLookupWardenInternalUnit(t *testing.T) {
	unit, ok := lookupWardenInternalUnit("D/ACD")
	if !ok {
		t.Fatal("expected D/ACD to resolve from the registry")
	}
	if unit.query != "D/ACD" {
		t.Fatalf("query = %q, want D/ACD", unit.query)
	}
	if unit.label == "" {
		t.Fatal("a registered unit must carry a human label")
	}

	if _, ok := lookupWardenInternalUnit("7"); ok {
		t.Fatal("an unregistered value must not resolve (the safety boundary)")
	}
	if _, ok := lookupWardenInternalUnit(""); ok {
		t.Fatal("an empty value must not resolve")
	}
}

// Every registry row must carry a non-empty value, query, and label, and values
// must be unique. An empty value would shadow getOptionString's "" miss return,
// and a duplicate value would let the first matching row silently shadow a later
// one — both invisible to "add a row, no logic change". This pins the invariant
// so a careless new row fails loudly here.
func TestWardenInternalUnits_RegistryRowsValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i, unit := range wardenInternalUnits {
		if unit.value == "" {
			t.Fatalf("registry row %d has an empty value", i)
		}
		if unit.query == "" {
			t.Fatalf("registry row %d (%q) has an empty query", i, unit.value)
		}
		if unit.label == "" {
			t.Fatalf("registry row %d (%q) has an empty label", i, unit.value)
		}
		if seen[unit.value] {
			t.Fatalf("registry row %d has a duplicate value %q; the first match would shadow it", i, unit.value)
		}
		seen[unit.value] = true
	}
}

// Happy path: every roster member has a linked, in-guild Discord. Each one is
// added straight to Verified Warden Internal by ID (no member search), the run
// is acknowledged with a deferred ephemeral, and the report carries the
// added-or-confirmed count plus the success mention embed.
func TestRunWardenBulkAddInternal_HappyPathAddsAllAndReportsCount(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
		liteMember("Trooper.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

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

	got := lastEditContent(calls)
	if !strings.Contains(got, "Added or confirmed 2") {
		t.Fatalf("expected the exact added-or-confirmed lead phrase, got %q", got)
	}
	// A clean run is not a permissions problem: no hint.
	if strings.Contains(got, "Manage Roles") {
		t.Fatalf("a clean run must not surface the missing-permissions hint; got %q", got)
	}

	embed := lastEditEmbed(calls)
	if embed == nil {
		t.Fatal("expected a success mention embed")
	}
	if embed.Title != "Added 2 user(s)" {
		t.Fatalf("embed should report exactly 2 users, got %q", embed.Title)
	}

	// A clean run must never page Sentry.
	if rec.count != 0 {
		t.Fatalf("a clean happy-path run must not capture to Sentry; got %d", rec.count)
	}
}

// Idempotency: re-adding a member who already holds the role is a no-op on
// Discord's side (the role-add PUT returns success), so the fake returns nil and
// the member must land in added/confirmed, not in any failure bucket. This is the
// contract behind the "added or confirmed" wording — a nil error means the member
// has the role whether or not this call is what put it there.
func TestRunWardenBulkAddInternal_IdempotentReAddCountsAsConfirmed(t *testing.T) {
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Already.In", "111111111111111111"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// No queued error: the add of an already-present member succeeds (nil), exactly
	// as Discord's idempotent role-add PUT behaves.
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Added or confirmed 1") {
		t.Fatalf("an idempotent re-add must count as confirmed, got %q", got)
	}
	if strings.Contains(got, "Could not be added") {
		t.Fatalf("an idempotent re-add must not be reported as a failure, got %q", got)
	}
	embed := lastEditEmbed(f.Calls())
	if embed == nil || !strings.Contains(embed.Description, "111111111111111111") {
		t.Fatalf("the re-added member must appear in the success embed, got %+v", embed)
	}
}

// A member whose milpac carries no Discord connection (empty discordId) is
// listed by forum username and never sent to Discord — a mention would render as
// a dead raw ID, and there is nothing to add.
func TestRunWardenBulkAddInternal_NoDiscordLinkedListedNotAdded(t *testing.T) {
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
		liteMember("Linkless.B", ""),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	// Only the linked member reaches Discord; the link-less one is skipped.
	if gm.countCalls("GuildMemberRoleAdd") != 1 {
		t.Fatalf("expected exactly 1 role add (the linked member), got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Added or confirmed 1") {
		t.Fatalf("expected the exact added-or-confirmed lead phrase for the one linked member, got %q", got)
	}
	if !strings.Contains(got, "No Discord linked") {
		t.Fatalf("expected a 'No Discord linked' section, got %q", got)
	}
	if !strings.Contains(got, "Linkless.B") {
		t.Fatalf("expected the link-less member listed by forum username, got %q", got)
	}
	// The link-less member must not appear as a mention in the success embed.
	if embed := lastEditEmbed(f.Calls()); embed != nil && strings.Contains(embed.Description, "Linkless.B") {
		t.Fatalf("link-less member must not be in the success embed, got %q", embed.Description)
	}
}

// A member with a linked Discord whose add returns 404 (Unknown Member) is "not
// in this server": listed by forum username, not added, the run continues past
// it, and a 404 never captures to Sentry.
func TestRunWardenBulkAddInternal_NotInGuild404ListedNotAddedNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Present.A", "111111111111111111"),
		liteMember("Absent.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// One of the two adds 404s. Map iteration order is nondeterministic, so assert
	// on counts rather than which member lands in the bucket.
	gm.MemberRoleAddErrs = []error{nil, restError(http.StatusNotFound, 10007, rawBodyMarker)}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	// Both members were attempted: the run continued past the 404.
	if gm.countCalls("GuildMemberRoleAdd") != 2 {
		t.Fatalf("expected both members attempted, got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Added or confirmed 1") {
		t.Fatalf("expected the exact added-or-confirmed lead phrase for the one present member, got %q", got)
	}
	if !strings.Contains(got, "Not in this Discord (1)") {
		t.Fatalf("expected one member in the 'Not in this Discord' section, got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a 404 (not in server) must NOT capture to Sentry; got %d", rec.count)
	}
	if embed := lastEditEmbed(f.Calls()); embed == nil || !strings.Contains(embed.Title, "1") {
		t.Fatal("expected the one successful add reported in the success embed")
	}
}

// A non-404 client fault on an add (here a 403 — bot lacks Manage Roles or the
// role sits above it) is still surfaced, never silently dropped, but it is an
// operator-fixable condition so it must NOT capture to Sentry.
func TestRunWardenBulkAddInternal_PerMemberClientFaultListedNotCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Forbidden.A", "111111111111111111"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	gm.MemberRoleAddErrs = []error{restError(http.StatusForbidden, 50013, rawBodyMarker)}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Could not be added (1)") {
		t.Fatalf("a non-404 client fault must still be listed, not silently dropped; got %q", got)
	}
	if !strings.Contains(got, "Forbidden.A") {
		t.Fatalf("expected the member listed by forum username, got %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a 4xx client fault must NOT capture to Sentry; got %d", rec.count)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
	// A 403 is a missing-permissions fault: the summary must carry an actionable
	// hint (Manage Roles + role position) so the operator isn't left with only an
	// opaque "Could not be added" list and a misleading "added 0" lead.
	if !strings.Contains(got, "Manage Roles") {
		t.Fatalf("a missing-permissions fault must surface a 'Manage Roles' hierarchy hint; got %q", got)
	}
}

// A genuine per-member fault (5xx) is listed, sent to Sentry tagged with the
// unit value, and the run continues past it so the other members still get
// processed.
func TestRunWardenBulkAddInternal_PerMemberFaultCapturedAndRunContinues(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Ok.A", "111111111111111111"),
		liteMember("Boom.B", "222222222222222222"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	gm.MemberRoleAddErrs = []error{nil, restError(http.StatusInternalServerError, 0, rawBodyMarker)}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	// Both attempted: the run continued past the per-member 5xx.
	if gm.countCalls("GuildMemberRoleAdd") != 2 {
		t.Fatalf("expected both members attempted, got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx per-member fault must capture to Sentry exactly once; got %d", rec.count)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Could not be added (1)") {
		t.Fatalf("expected a 'Could not be added' section listing the faulted member, got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
	// A pure-5xx fault is not a permissions problem, so the missing-permissions
	// hint must NOT be appended.
	if strings.Contains(got, "Manage Roles") {
		t.Fatalf("a 5xx fault must not surface the missing-permissions hint; got %q", got)
	}
	if embed := lastEditEmbed(f.Calls()); embed == nil || !strings.Contains(embed.Title, "1") {
		t.Fatal("expected the one successful add reported in the success embed")
	}
}

// All four outcome buckets at once: one clean add, one linked-but-absent (404),
// one with no Discord link, and one genuine fault (5xx). The lead count must
// coexist with every section, and the sections must appear in a stable order
// (lead, not-in-Discord, no-Discord-linked, could-not-be-added). Map iteration
// order randomizes WHICH linked member draws which error, but the multiset of
// outcomes — and therefore every bucket size — is fixed.
func TestRunWardenBulkAddInternal_AllBucketsCoexistWithStableOrdering(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Added.A", "111111111111111111"),
		liteMember("Absent.B", "222222222222222222"),
		liteMember("Linkless.C", ""),
		liteMember("Boom.D", "333333333333333333"),
	), http.StatusOK, nil)

	gm := internalRoleGM()
	// Three linked members draw these three outcomes (one each) in map order: a
	// success, a 404 (not in this Discord), and a 5xx (a genuine fault).
	gm.MemberRoleAddErrs = []error{
		nil,
		restError(http.StatusNotFound, 10007, rawBodyMarker),
		restError(http.StatusInternalServerError, 0, rawBodyMarker),
	}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	got := lastEditContent(f.Calls())
	leadIdx := strings.Index(got, "Added or confirmed 1")
	notInIdx := strings.Index(got, "Not in this Discord (1)")
	noLinkIdx := strings.Index(got, "No Discord linked (1)")
	faultIdx := strings.Index(got, "Could not be added (1)")
	if leadIdx < 0 || notInIdx < 0 || noLinkIdx < 0 || faultIdx < 0 {
		t.Fatalf("expected the lead count and all three buckets present, got %q", got)
	}
	ordered := leadIdx < notInIdx && notInIdx < noLinkIdx && noLinkIdx < faultIdx
	if !ordered {
		t.Fatalf("sections must keep a stable order (lead, not-in-Discord, no-link, could-not-be-added); got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
	// Only the 5xx is a genuine fault; it captures exactly once.
	if rec.count != 1 {
		t.Fatalf("only the 5xx fault should capture to Sentry; got %d", rec.count)
	}
}

// The picker feeding a registry feeding a fixed query makes this fixed input, so
// an empty roster is structurally a bug, not "the unit is empty" (ADR 0002). It
// must change nothing, tell the operator it shouldn't happen and was reported,
// and capture tagged with the unit value — never a silent "added 0".
func TestRunWardenBulkAddInternal_EmptyRosterCapturedNothingChanged(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{},
	}, http.StatusOK, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("an empty roster must change nothing; got adds %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("an empty roster from a validated unit must capture exactly once; got %d", rec.count)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the empty-roster capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	got := lastEditContent(f.Calls())
	if strings.Contains(got, "Added or confirmed 0") {
		t.Fatalf("empty roster must not be reported as a normal 'added 0' summary; got %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "shouldn't happen") {
		t.Fatalf("expected an ADR-0002 'shouldn't happen, reported' message, got %q", got)
	}
	// No success embed for a non-result.
	if embed := lastEditEmbed(f.Calls()); embed != nil {
		t.Fatalf("an empty roster must not produce a success embed; got %+v", embed)
	}
}

// A failed roster fetch is a genuine milpac fault on fixed input: it must be
// captured (tagged with the unit value), surfaced to the operator, and add
// nobody.
func TestRunWardenBulkAddInternal_RosterFetchFaultCapturedNoAdds(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, utils.LiteRosterResponse{}, http.StatusInternalServerError, nil)

	gm := internalRoleGM()
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("a failed roster fetch must not add anyone; got %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a genuine milpac fault must capture exactly once; got %d", rec.count)
	}
	if unitVal, ok := kvValue(rec.lastKV, "unit"); !ok || unitVal != "D/ACD" {
		t.Fatalf("the roster-fetch capture must be tagged with the unit value; got kv %v", rec.lastKV)
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Failed to fetch") {
		t.Fatalf("expected a roster-fetch failure message, got %q", got)
	}
}

// A DM-shaped interaction (no GuildID) is rejected with a clear server-only
// message before any defer, guild call, or roster fetch.
func TestRunWardenBulkAddInternal_MissingGuildRejectedBeforeAnything(t *testing.T) {
	tripwireAPIServer(t)
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("unit", "D/ACD")) // no GuildID

	runWardenBulkAddInternal(f, gm, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("DM-context must not touch the guild; got %v", gm.Calls())
	}
	if got := lastResponseContent(f.Calls()); !strings.Contains(got, "can only be used in a server") {
		t.Fatalf("expected a server-only rejection, got %q", got)
	}
}

// A crafted interaction carrying a value absent from the registry is rejected
// before any guild call or roster fetch — the registry is the safety boundary.
func TestRunWardenBulkAddInternal_UnknownUnitRejectedBeforeAnything(t *testing.T) {
	tripwireAPIServer(t)
	gm := internalRoleGM()
	f := &fakeResponder{}
	i := wardenBulkAddInternalInteraction("7") // a careless short query, not in the registry

	runWardenBulkAddInternal(f, gm, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("an unregistered unit must be rejected before any guild call; got %v", gm.Calls())
	}
	if got := lastResponseContent(f.Calls()); !strings.Contains(got, "Unknown unit") {
		t.Fatalf("expected an unknown-unit rejection, got %q", got)
	}
}

// A (malformed) interaction with no unit option is rejected before any guild
// call or roster fetch.
func TestRunWardenBulkAddInternal_MissingUnitOptionRejected(t *testing.T) {
	tripwireAPIServer(t)
	gm := internalRoleGM()
	f := &fakeResponder{}
	i := fakeAppCommandInteraction() // no unit option
	i.GuildID = "guild-1"

	runWardenBulkAddInternal(f, gm, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("a missing unit must short-circuit before any guild call; got %v", gm.Calls())
	}
	if got := lastResponseContent(f.Calls()); !strings.Contains(got, "Missing unit") {
		t.Fatalf("expected a missing-unit rejection, got %q", got)
	}
}

// When the deferred-ephemeral acknowledge fails, the run bails before resolving
// roles or fetching the roster, rather than pressing on with no live response.
func TestRunWardenBulkAddInternal_DeferFailureBailsBeforeWork(t *testing.T) {
	tripwireAPIServer(t)
	gm := internalRoleGM()
	f := &fakeResponder{RespondErrs: []error{errFirstRespond}}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildRoles") != 0 {
		t.Fatalf("a failed defer must bail before role resolution; got %v", gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("a failed defer must add nobody; got %v", gm.Calls())
	}
}

// When the Verified Warden Internal role is absent from the guild, the run says
// so and never fetches the roster or adds anyone.
func TestRunWardenBulkAddInternal_InternalRoleMissingSurfacedNoFetch(t *testing.T) {
	tripwireAPIServer(t)
	gm := &fakeGuildManager{roles: []*discordgo.Role{wardenRole("x", "Some Other Role")}}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("must not add anyone when the role is missing; got %v", gm.Calls())
	}
	if got := lastEditContent(f.Calls()); !strings.Contains(got, "role not found in guild") {
		t.Fatalf("expected a role-not-found message, got %q", got)
	}
}

// A 5xx from GuildRoles during role resolution is a genuine Discord fault: it
// captures once, shows a body-free retry message, and never fetches the roster.
func TestRunWardenBulkAddInternal_RoleResolve5xxCapturedNoFetch(t *testing.T) {
	tripwireAPIServer(t)
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{RolesErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	if rec.count != 1 {
		t.Fatalf("a 5xx GuildRoles fault must capture exactly once; got %d", rec.count)
	}
	if got := lastEditContent(f.Calls()); strings.Contains(got, rawBodyMarker) {
		t.Fatalf("must not leak the raw Discord body, got %q", got)
	}
}

// A very large bucket would otherwise push the summary past Discord's 2000-char
// message limit and fail the edit. The summary must clamp to the limit while
// keeping the always-present lead line.
func TestBuildWardenInternalBulkAddSummary_ClampsToDiscordLimit(t *testing.T) {
	many := make([]string, 400)
	for i := range many {
		many[i] = "Trooper.Placeholder.Name"
	}

	got := buildWardenInternalBulkAddSummary("D/ACD", wardenInternalRoleName, 0, nil, nil, many, false)

	if len(got) > 2000 {
		t.Fatalf("summary must stay within Discord's 2000-char limit, got %d", len(got))
	}
	if !strings.Contains(got, "Added or confirmed 0") {
		t.Fatalf("the lead summary line must survive truncation, got %q", got)
	}
}

// The command is wired into the registry so Discord registers it and routes its
// interactions to the handler.
func TestRegistry_RegistersWardenBulkAddInternal(t *testing.T) {
	reg := NewRegistry()

	registered := false
	for _, def := range reg.GetCommands() {
		if def.Name == "warden-bulkadd-internal" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("warden-bulkadd-internal must be registered in the command registry")
	}
	if _, ok := reg.GetHandler("warden-bulkadd-internal"); !ok {
		t.Fatal("warden-bulkadd-internal must resolve a handler from the registry")
	}
}
