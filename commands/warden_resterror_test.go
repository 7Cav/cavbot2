package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// restError builds a *discordgo.RESTError carrying the given HTTP status and a
// raw JSON body, mirroring how discordgo wraps a failed API call. The body is
// the exact string that RESTError.Error() leaks ("HTTP <status>, <body>") — our
// classifier must never surface it in a user-facing reply.
func restError(statusCode int, code int, apiMessage string) *discordgo.RESTError {
	body := fmt.Sprintf(`{"message": %q, "code": %d}`, apiMessage, code)
	return &discordgo.RESTError{
		Response:     &http.Response{StatusCode: statusCode, Status: fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode))},
		ResponseBody: []byte(body),
		Message:      &discordgo.APIErrorMessage{Code: code, Message: apiMessage},
	}
}

// rawBody is the literal leak the classifier must keep out of user-facing text:
// a substring guaranteed to appear only if RESTError.Error() was interpolated.
const rawBodyMarker = "secret-internal-detail"

// --- classifier unit tests ---

func TestClassifyDiscordError_4xxIsClientFault(t *testing.T) {
	err := restError(http.StatusBadRequest, 50035, rawBodyMarker)
	c := classifyDiscordError(err)
	if c.SystemFault {
		t.Fatalf("a 400 must be classified as a client/config fault, not a system fault")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
	}
}

func TestClassifyDiscordError_5xxIsSystemFault(t *testing.T) {
	err := restError(http.StatusInternalServerError, 0, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 500 must be classified as a system fault")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
	}
}

func TestClassifyDiscordError_TransportErrorIsSystemFault(t *testing.T) {
	// A non-REST transport error (no HTTP response) is a system fault.
	err := fmt.Errorf("dial tcp: connection refused")
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a transport error must be classified as a system fault")
	}
}

func TestClassifyDiscordError_403RoleHierarchyMessage(t *testing.T) {
	err := restError(http.StatusForbidden, 50013, rawBodyMarker)
	c := classifyDiscordError(err)
	if c.SystemFault {
		t.Fatalf("a 403 must not be a system fault")
	}
	if !c.MissingPermissions {
		t.Fatalf("a 403 must set MissingPermissions so callers can show the hierarchy hint")
	}
}

// A *discordgo.RESTError can carry a nil Response (e.g. an error built before
// any HTTP exchange completes). With no status code to read, the classifier
// can't prove it's a client fault, so it must take the system-fault guard
// rather than dereference the nil Response.
func TestClassifyDiscordError_RESTErrorNilResponseIsSystemFault(t *testing.T) {
	err := &discordgo.RESTError{Response: nil}
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a RESTError with a nil Response must be classified as a system fault")
	}
}

// A 404 carrying Unknown Member (10007) is a distinct, non-system client fault:
// the targeted member-by-ID lookup proved the user is genuinely absent. It must
// set NotFound (so findGuildMember can render a clear "not in this server"
// message) without setting SystemFault or MissingPermissions, and never leak the
// raw body.
func TestClassifyDiscordError_404UnknownMemberIsNotFound(t *testing.T) {
	err := restError(http.StatusNotFound, 10007, rawBodyMarker)
	c := classifyDiscordError(err)
	if c.SystemFault {
		t.Fatalf("a 404 Unknown Member must not be a system fault")
	}
	if !c.NotFound {
		t.Fatalf("a 404 Unknown Member must set NotFound so callers can show a clear absent-member message")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
	}
}

// A 404 carrying Unknown Role (10011) is NOT a genuine absence — the role ID
// went stale or the role was deleted, a config fault. It must classify as a
// captured system fault (SystemFault), never as NotFound (which would misreport
// the role's members as "not in this server"), and never leak the raw body.
func TestClassifyDiscordError_404UnknownRoleIsSystemFault(t *testing.T) {
	err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 404 Unknown Role (stale/deleted role) must classify as a captured system fault")
	}
	if c.NotFound {
		t.Fatalf("a 404 Unknown Role must NOT set NotFound — it is a config fault, not an absent member")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
	}
}

// A 404 carrying Unknown Guild (10004) means the guild ID is wrong — a config
// fault, not an absent member. Same treatment as Unknown Role: captured system
// fault, never NotFound, never a body leak.
func TestClassifyDiscordError_404UnknownGuildIsSystemFault(t *testing.T) {
	err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 404 Unknown Guild (bad guild ID) must classify as a captured system fault")
	}
	if c.NotFound {
		t.Fatalf("a 404 Unknown Guild must NOT set NotFound — it is a config fault, not an absent member")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
	}
}

// A bare 404 with no application error code (Discord can answer a member lookup
// with a 404 and no body code) must keep the genuine-absence behavior: NotFound,
// no capture. This preserves the targeted member-lookup contract — only an
// unexpected NON-zero 404 code is a config fault.
func TestClassifyDiscordError_404BareCodeStaysNotFound(t *testing.T) {
	err := restError(http.StatusNotFound, 0, rawBodyMarker)
	c := classifyDiscordError(err)
	if c.SystemFault {
		t.Fatalf("a bare 404 (no application error code) must not be a system fault")
	}
	if !c.NotFound {
		t.Fatalf("a bare 404 must stay NotFound so the member-lookup path keeps its absent-member message")
	}
}

// classifyNotFound routes ANY non-zero 404 code that isn't Unknown Member through
// a single catch-all into SystemFault, not just the named Unknown Role / Unknown
// Guild codes. Pin that with an arbitrary, never-named code so a future refactor
// to explicit `case` arms can't silently narrow the contract and let an
// unexpected code fall back to NotFound.
func TestClassifyDiscordError_404UnexpectedCodeIsSystemFault(t *testing.T) {
	err := restError(http.StatusNotFound, 12345, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 404 with an unexpected non-zero code must classify as a captured system fault")
	}
	// The catch-all sets ConfigFault for EVERY non-zero, non-UnknownMember code,
	// not just the named Unknown Role / Unknown Guild ones. Pin that here with the
	// arbitrary code so a future refactor to explicit `case` arms can't silently
	// narrow the config-fault rendering to only the named codes and drop an
	// unexpected code back into the transient "try again shortly" wording.
	if !c.ConfigFault {
		t.Fatalf("a 404 with an unexpected non-zero code must set ConfigFault — the catch-all routes all such codes to the config-fault rendering")
	}
	if c.NotFound {
		t.Fatalf("a 404 with an unexpected non-zero code must NOT set NotFound — only Unknown Member or a bare 404 is a genuine absence")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
	}
}

// --- config-fault flag: a stale/deleted role or wrong guild is a CAPTURED
// system fault that the render layer must tell apart from a transient one ---

// A 404 Unknown Role (10011) is both a system fault (so it still captures) AND a
// config fault, so the render layer can drop the transient "try again shortly"
// hint that cannot fix a deleted role. The capture-on-SystemFault path is left
// untouched; only the wording branches on the new flag.
func TestClassifyDiscordError_404UnknownRoleIsConfigFault(t *testing.T) {
	err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 404 Unknown Role must stay a captured system fault")
	}
	if !c.ConfigFault {
		t.Fatalf("a 404 Unknown Role must set ConfigFault so the render layer can drop the transient retry hint")
	}
}

// A 404 Unknown Guild (10004) is a config fault for the same reason: a wrong
// GUILD_ID will not clear on its own.
func TestClassifyDiscordError_404UnknownGuildIsConfigFault(t *testing.T) {
	err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 404 Unknown Guild must stay a captured system fault")
	}
	if !c.ConfigFault {
		t.Fatalf("a 404 Unknown Guild must set ConfigFault")
	}
}

// A genuine 5xx is a captured system fault but NOT a config fault: the operator
// keeps the transient "try again shortly" wording, the right advice for a
// Discord-side blip.
func TestClassifyDiscordError_5xxIsNotConfigFault(t *testing.T) {
	err := restError(http.StatusInternalServerError, 0, rawBodyMarker)
	c := classifyDiscordError(err)
	if !c.SystemFault {
		t.Fatalf("a 500 must be a system fault")
	}
	if c.ConfigFault {
		t.Fatalf("a 500 must NOT be a config fault — it is a transient Discord-side error")
	}
}

// A transport error (no HTTP response) is a system fault but not a config fault,
// same as a 5xx.
func TestClassifyDiscordError_TransportIsNotConfigFault(t *testing.T) {
	c := classifyDiscordError(fmt.Errorf("dial tcp: connection refused"))
	if !c.SystemFault {
		t.Fatalf("a transport error must be a system fault")
	}
	if c.ConfigFault {
		t.Fatalf("a transport error must NOT be a config fault")
	}
}

// The render arms only ever read ConfigFault inside their SystemFault block, so
// the whole design rests on an unwritten invariant: nothing returns
// ConfigFault: true with SystemFault: false. If a future edit broke that, a
// config fault would skip every SystemFault arm and fall through to the default
// 4xx arm — rendering "Discord rejected the request" with NO Sentry capture, a
// silent page-skip. Pin the implication (ConfigFault ⇒ SystemFault) across a
// representative spread of inputs so that drift fails here instead of in prod.
func TestClassifyDiscordError_ConfigFaultImpliesSystemFault(t *testing.T) {
	inputs := []struct {
		name string
		err  error
	}{
		{"unknownRole404", restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker)},
		{"unknownGuild404", restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)},
		{"unexpectedCode404", restError(http.StatusNotFound, 12345, rawBodyMarker)},
		{"unknownMember404", restError(http.StatusNotFound, 10007, rawBodyMarker)},
		{"bare404", restError(http.StatusNotFound, 0, rawBodyMarker)},
		{"forbidden403", restError(http.StatusForbidden, 50013, rawBodyMarker)},
		{"badRequest400", restError(http.StatusBadRequest, 50035, rawBodyMarker)},
		{"serverError5xx", restError(http.StatusInternalServerError, 0, rawBodyMarker)},
		{"transport", fmt.Errorf("dial tcp: connection refused")},
	}

	for _, in := range inputs {
		c := classifyDiscordError(in.err)
		if c.ConfigFault && !c.SystemFault {
			t.Fatalf("%s: ConfigFault must imply SystemFault — a config fault that is not a system fault would skip capture and render the generic 4xx rejection", in.name)
		}
	}
}

// discordgo leaves RESTError.Message nil when a 404's response body isn't
// parseable JSON, so there is no application error code to read. classifyNotFound's
// restErr.Message == nil guard must treat that as a bare 404 — NotFound, no
// capture — without dereferencing the nil Message. The restError helper always
// sets a non-nil Message, so this RESTError is built inline to exercise the
// genuine nil-Message branch.
func TestClassifyDiscordError_404NilMessageStaysNotFound(t *testing.T) {
	err := &discordgo.RESTError{
		Response:     &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"},
		ResponseBody: []byte("not parseable json"),
		Message:      nil,
	}
	c := classifyDiscordError(err)
	if c.SystemFault {
		t.Fatalf("a 404 with a nil Message (unparseable body) must not be a system fault")
	}
	if !c.NotFound {
		t.Fatalf("a 404 with a nil Message must stay NotFound, matching the bare-404 contract")
	}
}

// --- mention/ID lookup: authoritative, never falls through to name search ---

// A valid mention for a present member resolves via the targeted GuildMember
// lookup and never reaches GuildMembersSearch.
func TestFindGuildMember_MentionSuccessResolvesAuthoritatively(t *testing.T) {
	gm := &fakeGuildManager{
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}

	member, err := findGuildMember(gm, "guild-1", "<@123456789012345678>")
	if err != nil {
		t.Fatalf("expected the mention to resolve, got error %v", err)
	}
	if member == nil || member.User == nil || member.User.ID != "123456789012345678" {
		t.Fatalf("expected the targeted member, got %+v", member)
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a resolved mention must not fall through to name search; got calls %v", gm.Calls())
	}
}

// A mention whose targeted lookup 404s must yield a clear "not in this server"
// message — never a name search of the raw "<@...>" string, and never a Sentry
// capture (a 404 is an ordinary, non-system condition).
func TestFindGuildMember_Mention404ClearMessageNoSearchNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusNotFound, 10007, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "<@123456789012345678>")
	if err == nil {
		t.Fatal("expected an error when the mentioned member is absent")
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("mention 404 leaked the raw Discord body: %q", err.Error())
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not in this server") {
		t.Fatalf("expected a clear absent-member message, got %q", err.Error())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a 404 mention must NOT fall through to name search; got calls %v", gm.Calls())
	}
	if rec.count != 0 {
		t.Fatalf("a 404 mention must NOT capture to Sentry; got %d", rec.count)
	}
}

// The targeted member lookup must keep its absent-member behavior for a bare 404
// (no application error code) just as for Unknown Member: a clear "not in this
// server" message, no name search, no Sentry capture. The new code-aware
// classifier only treats an unexpected NON-zero 404 code as a config fault, so
// the member-lookup contract is unchanged for the codeless case.
func TestFindGuildMember_MentionBare404StaysNotInServerNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusNotFound, 0, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "<@123456789012345678>")
	if err == nil {
		t.Fatal("expected an error when the mentioned member is absent")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not in this server") {
		t.Fatalf("a bare 404 must keep the clear absent-member message, got %q", err.Error())
	}
	if rec.count != 0 {
		t.Fatalf("a bare 404 mention must NOT capture to Sentry; got %d", rec.count)
	}
}

// A transient (non-404) failure on a mention lookup is a genuine system fault:
// it must surface an error, capture to Sentry once, and must NOT downgrade into
// a name search of the raw mention string (which would report a misleading
// "no member found").
func TestFindGuildMember_MentionTransientFaultSurfacedAndCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "<@123456789012345678>")
	if err == nil {
		t.Fatal("expected an error from a transient mention-lookup failure")
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("mention transient fault leaked the raw Discord body: %q", err.Error())
	}
	if strings.Contains(err.Error(), "No member found") {
		t.Fatalf("a transient fault must not be reported as 'no member found': %q", err.Error())
	}
	// A genuine 5xx is transient: the member-lookup reply keeps "try again shortly".
	if !strings.Contains(strings.ToLower(err.Error()), "try again shortly") {
		t.Fatalf("a transient member-lookup fault must keep the retry hint, got %q", err.Error())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a transient mention fault must NOT fall through to name search; got calls %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a transient (5xx) mention fault must capture to Sentry once; got %d", rec.count)
	}
}

// resolveMemberByID can see a 404 Unknown Guild (10004) when GUILD_ID is wrong.
// That is a config fault, not a genuine absence: the operator must get a line
// naming the wrong guild and surfacing the sanitized UserDetail phrase, with NO
// "try again shortly" hint and NO "not in this server" wording, while it still
// captures once, never leaks the raw body, and never falls through to a name
// search. Covers the SystemFault/ConfigFault arm of resolveMemberByID.
func TestFindGuildMember_MemberLookupUnknownGuild404ConfigFault(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "123456789012345678")
	if err == nil {
		t.Fatal("expected an error from an Unknown Guild member lookup")
	}
	got := err.Error()
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("member-lookup Unknown Guild 404 leaked the raw Discord body: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "try again shortly") {
		t.Fatalf("a config-fault member lookup must NOT show the transient retry hint, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "not in this server") {
		t.Fatalf("an Unknown Guild 404 is a config fault, not a genuine absence, got %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "guild") {
		t.Fatalf("a config-fault member lookup must name the wrong guild, got %q", got)
	}
	if !strings.Contains(got, "unknown role or guild") {
		t.Fatalf("a config-fault member lookup must surface the sanitized UserDetail phrase, got %q", got)
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a config-fault member lookup must NOT fall through to name search; got %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a config-fault member lookup must capture to Sentry once; got %d", rec.count)
	}
}

// A raw snowflake ID whose targeted lookup 404s is treated identically to a
// mention: a clear absent-member message, no name search of the raw ID.
func TestFindGuildMember_Snowflake404ClearMessageNoSearch(t *testing.T) {
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusNotFound, 10007, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "123456789012345678")
	if err == nil {
		t.Fatal("expected an error when the snowflake member is absent")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not in this server") {
		t.Fatalf("expected a clear absent-member message, got %q", err.Error())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a 404 snowflake must NOT fall through to name search; got calls %v", gm.Calls())
	}
}

// The raw-snowflake form must reach the same system-fault capture arm as the
// mention form on a transient (non-404) failure: surface an error, capture once,
// no name search, no misleading "no member found". Snowflakes were otherwise
// only covered for 404, so this pins the two input forms to identical handling.
func TestFindGuildMember_SnowflakeTransientFaultSurfacedAndCaptured(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "123456789012345678")
	if err == nil {
		t.Fatal("expected an error from a transient snowflake-lookup failure")
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("snowflake transient fault leaked the raw Discord body: %q", err.Error())
	}
	if strings.Contains(err.Error(), "No member found") {
		t.Fatalf("a transient fault must not be reported as 'no member found': %q", err.Error())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a transient snowflake fault must NOT fall through to name search; got calls %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a transient (5xx) snowflake fault must capture to Sentry once; got %d", rec.count)
	}
}

// A non-404 4xx (here a 400) on a targeted mention/ID lookup is an
// operator/config-fixable client fault: it shows the generic "Discord rejected
// the request" wording, never captures to Sentry, and never leaks the raw body.
// Covers the default arm of resolveMemberByID.
func TestFindGuildMember_MentionGeneric4xxNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MemberErrs: []error{restError(http.StatusBadRequest, 50035, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "<@123456789012345678>")
	if err == nil {
		t.Fatal("expected an error from a 400 mention lookup")
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("mention generic 4xx leaked the raw Discord body: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Discord rejected the request") {
		t.Fatalf("expected the generic 4xx rejection wording, got %q", err.Error())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a generic 4xx mention fault must NOT fall through to name search; got calls %v", gm.Calls())
	}
	if rec.count != 0 {
		t.Fatalf("a generic 4xx mention fault must NOT capture to Sentry; got %d", rec.count)
	}
}

// captureRecorder swaps the package-level captureError seam for the duration of
// a test and counts how many times it fires, so a test can assert the
// 4xx-vs-5xx Sentry split without a live Sentry client. lastKV holds the kv
// varargs from the most recent capture, so a test can also pin the context
// fields (command/guild/role) a site is required to forward.
type captureRecorder struct {
	count  int
	lastKV []any
}

func (c *captureRecorder) install(t *testing.T) {
	t.Helper()
	prev := captureError
	captureError = func(msg string, err error, kv ...any) {
		c.count++
		c.lastKV = kv
	}
	t.Cleanup(func() { captureError = prev })
}

// kvValue returns the value paired with key in a sequential key/value vararg
// slice (k0, v0, k1, v1, ...), and whether it was present.
func kvValue(kv []any, key string) (any, bool) {
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok && k == key {
			return kv[i+1], true
		}
	}
	return nil, false
}

// --- search path: 4xx vs 5xx split ---

func TestFindGuildMember_Search4xxDoesNotCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MembersSearchErrs: []error{restError(http.StatusBadRequest, 50035, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "somename")
	if err == nil {
		t.Fatal("expected an error from a 400 search")
	}
	if rec.count != 0 {
		t.Fatalf("a 4xx search must NOT capture to Sentry; got %d captures", rec.count)
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("search reply leaked the raw Discord body: %q", err.Error())
	}
}

func TestFindGuildMember_Search5xxCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MembersSearchErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}

	_, err := findGuildMember(gm, "guild-1", "somename")
	if err == nil {
		t.Fatal("expected an error from a 500 search")
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx search must capture to Sentry exactly once; got %d", rec.count)
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("search reply leaked the raw Discord body: %q", err.Error())
	}
}

func TestFindGuildMember_SearchTransportErrorCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{MembersSearchErrs: []error{fmt.Errorf("dial tcp: connection refused")}}

	_, err := findGuildMember(gm, "guild-1", "somename")
	if err == nil {
		t.Fatal("expected an error from a transport failure")
	}
	if rec.count != 1 {
		t.Fatalf("a transport error must capture to Sentry exactly once; got %d", rec.count)
	}
}

// --- sibling SystemFault helpers: config-fault vs transient rendering split ---

// A wrong GUILD_ID (Unknown Guild, 10004) can surface on any Discord call, so the
// sibling SystemFault helpers (member search, role resolve, channels resolve,
// purge recreate) must each render the config-fault line — no transient retry
// hint, surfacing the sanitized UserDetail phrase, never the raw body — while
// still capturing exactly once. A genuine 5xx keeps the transient "try again
// shortly" wording and also captures. This pins the split across every helper a
// config fault can reach, not just the role-mutation and member-lookup paths.
func TestSystemFaultHelpers_ConfigFaultVsTransientRendering(t *testing.T) {
	configErr := restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)
	transientErr := restError(http.StatusInternalServerError, 0, rawBodyMarker)

	cases := []struct {
		name   string
		render func(err error) string
	}{
		{"search", func(err error) string { return searchErrorReply(err).Error() }},
		{"roleResolve", func(err error) string { return roleResolveErrorReply(err, "cap").Error() }},
		{"channelsResolve", func(err error) string { return channelsResolveErrorReply(err, "cap").Error() }},
		{"purgeRecreate", func(err error) string { return purgeRecreateErrorReply("Some Role", err, "cap") }},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/config", func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			got := tc.render(configErr)
			if strings.Contains(got, rawBodyMarker) {
				t.Fatalf("config fault leaked the raw Discord body: %q", got)
			}
			if strings.Contains(strings.ToLower(got), "try again shortly") {
				t.Fatalf("a config fault must NOT show the transient retry hint, got %q", got)
			}
			if !strings.Contains(got, "unknown role or guild") {
				t.Fatalf("a config fault must surface the sanitized UserDetail phrase, got %q", got)
			}
			if rec.count != 1 {
				t.Fatalf("a config fault must capture to Sentry exactly once; got %d", rec.count)
			}
		})
		t.Run(tc.name+"/transient", func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			got := tc.render(transientErr)
			if strings.Contains(got, rawBodyMarker) {
				t.Fatalf("transient fault leaked the raw Discord body: %q", got)
			}
			if !strings.Contains(strings.ToLower(got), "try again shortly") {
				t.Fatalf("a transient fault must keep the retry hint, got %q", got)
			}
			if rec.count != 1 {
				t.Fatalf("a transient fault must capture to Sentry exactly once; got %d", rec.count)
			}
		})
	}
}

// --- role add/remove paths: 4xx vs 5xx split + 403 hierarchy hint ---

func wardenAddInteraction() *discordgo.InteractionCreate {
	return wardenInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)
}

func wardenRemoveInteraction() *discordgo.InteractionCreate {
	return wardenInteraction("guild-1",
		stringOption("command", "remove"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)
}

func wardenRoleAddGM(addErr error) *fakeGuildManager {
	return &fakeGuildManager{
		roles:             []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID:       map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleAddErrs: []error{addErr},
	}
}

func TestRunWardenAdd_RoleAdd403ShowsHierarchyHintNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := wardenRoleAddGM(restError(http.StatusForbidden, 50013, rawBodyMarker))
	f := &fakeResponder{}

	runWarden(f, gm, wardenAddInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-add 403 leaked the raw Discord body: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "above") || !strings.Contains(strings.ToLower(got), "role") {
		t.Fatalf("expected a role-hierarchy hint mentioning the role is above the bot's, got %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a 403 role add must NOT capture to Sentry; got %d", rec.count)
	}
}

func TestRunWardenAdd_RoleAdd5xxCapturesGenericRetry(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := wardenRoleAddGM(restError(http.StatusInternalServerError, 0, rawBodyMarker))
	f := &fakeResponder{}

	runWarden(f, gm, wardenAddInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-add 5xx leaked the raw Discord body: %q", got)
	}
	// A genuine 5xx is transient: the operator keeps the "try again shortly" hint.
	if !strings.Contains(strings.ToLower(got), "try again shortly") {
		t.Fatalf("a 5xx role add must keep the transient retry hint, got %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx role add must capture to Sentry once; got %d", rec.count)
	}
}

// A non-403 4xx (here a 400 with a non-permission code) is an
// operator/config-fixable client fault: it must show the generic "Discord
// rejected the request" wording, never the raw body, and never capture to
// Sentry. Covers the default arm of roleMutationErrorReply, which no other
// mutation-site test exercises. (A 404 no longer reaches this arm — Unknown Role
// and Unknown Guild codes are captured system faults; see the tests below.)
func TestRunWardenAdd_RoleAddGeneric4xxNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := wardenRoleAddGM(restError(http.StatusBadRequest, 50035, rawBodyMarker))
	f := &fakeResponder{}

	runWarden(f, gm, wardenAddInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-add generic 4xx leaked the raw Discord body: %q", got)
	}
	if !strings.Contains(got, "Discord rejected the request") {
		t.Fatalf("expected the generic 4xx rejection wording, got %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a generic 4xx role add must NOT capture to Sentry; got %d", rec.count)
	}
}

// A role-add 404 carrying Unknown Role (10011) — the resolved role was deleted
// between resolution and the add — is a config fault, not an absent member. It
// must capture to Sentry exactly once, render an operator-facing line DISTINCT
// from the plain "Discord rejected the request" not-found rendering, name the
// stale/deleted role, surface the sanitized UserDetail phrase, drop the transient
// "try again shortly" hint (retrying a deleted role only repeats the failure),
// and never leak the raw Discord body.
func TestRunWardenAdd_RoleAddUnknownRole404CapturesDistinctConfigFault(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := wardenRoleAddGM(restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker))
	f := &fakeResponder{}

	runWarden(f, gm, wardenAddInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-add Unknown Role 404 leaked the raw Discord body: %q", got)
	}
	if strings.Contains(got, "Discord rejected the request") {
		t.Fatalf("an Unknown Role 404 must surface a line distinct from the plain not-found rendering, got %q", got)
	}
	// A config fault must not send the operator down a retry path that cannot
	// clear a deleted role.
	if strings.Contains(strings.ToLower(got), "try again shortly") {
		t.Fatalf("an Unknown Role 404 (config fault) must NOT show the transient retry hint, got %q", got)
	}
	// It must name a stale/deleted role and surface the sanitized UserDetail phrase.
	if !strings.Contains(strings.ToLower(got), "stale") {
		t.Fatalf("an Unknown Role 404 must name a stale/deleted role, got %q", got)
	}
	if !strings.Contains(got, "unknown role or guild") {
		t.Fatalf("an Unknown Role 404 must surface the sanitized UserDetail phrase, got %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("an Unknown Role 404 (stale/deleted role) must capture to Sentry once; got %d", rec.count)
	}
}

// The remove path shares the classifier, so a role-remove 404 carrying Unknown
// Guild (10004) — a wrong guild ID — must capture exactly once, render the
// config-fault line (naming the wrong guild, surfacing the sanitized UserDetail
// phrase, no transient retry hint), and never leak the raw body, mirroring the
// add path's Unknown Role coverage.
func TestRunWardenRemove_RoleRemoveUnknownGuild404ConfigFault(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles:                []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID:          map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleRemoveErrs: []error{restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, wardenRemoveInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-remove Unknown Guild 404 leaked the raw Discord body: %q", got)
	}
	if strings.Contains(got, "Discord rejected the request") {
		t.Fatalf("an Unknown Guild 404 must surface a line distinct from the plain not-found rendering, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "try again shortly") {
		t.Fatalf("an Unknown Guild 404 (config fault) must NOT show the transient retry hint, got %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "guild") {
		t.Fatalf("an Unknown Guild 404 must name the wrong guild, got %q", got)
	}
	if !strings.Contains(got, "unknown role or guild") {
		t.Fatalf("an Unknown Guild 404 must surface the sanitized UserDetail phrase, got %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("an Unknown Guild 404 (bad guild ID) must capture to Sentry once; got %d", rec.count)
	}
}

func TestRunWardenRemove_RoleRemove403ShowsHierarchyHintNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles:                []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID:          map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleRemoveErrs: []error{restError(http.StatusForbidden, 50013, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, wardenRemoveInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-remove 403 leaked the raw Discord body: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "above") {
		t.Fatalf("expected a role-hierarchy hint, got %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a 403 role remove must NOT capture to Sentry; got %d", rec.count)
	}
}

func TestRunWardenRemove_RoleRemove5xxCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles:                []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID:          map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleRemoveErrs: []error{restError(http.StatusBadGateway, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}

	runWarden(f, gm, wardenRemoveInteraction())

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("role-remove 5xx leaked the raw Discord body: %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx role remove must capture to Sentry once; got %d", rec.count)
	}
}

// --- bulkadd: 4xx role-add failure must not capture but still report ---

func TestRunWardenBulkAdd_RoleAdd403NoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
		},
		MemberRoleAddErrs: []error{restError(http.StatusForbidden, 50013, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", "good"),
	)

	runWarden(f, gm, i)

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("bulkadd 403 leaked the raw Discord body: %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a 403 bulk role add must NOT capture to Sentry; got %d", rec.count)
	}
}

func TestRunWardenBulkAdd_RoleAdd5xxCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
		},
		MemberRoleAddErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", "good"),
	)

	runWarden(f, gm, i)

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("bulkadd 5xx leaked the raw Discord body: %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx bulk role add must capture to Sentry once; got %d", rec.count)
	}
}
