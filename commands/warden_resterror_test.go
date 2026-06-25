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

// A 404 is a distinct, non-system client fault: the targeted member-by-ID
// lookup proved the user is absent. It must set NotFound (so findGuildMember can
// render a clear "not in this server" message) without setting SystemFault or
// MissingPermissions, and never leak the raw body.
func TestClassifyDiscordError_404IsNotFound(t *testing.T) {
	err := restError(http.StatusNotFound, 10007, rawBodyMarker)
	c := classifyDiscordError(err)
	if c.SystemFault {
		t.Fatalf("a 404 must not be a system fault")
	}
	if !c.NotFound {
		t.Fatalf("a 404 must set NotFound so callers can show a clear absent-member message")
	}
	if strings.Contains(c.UserDetail, rawBodyMarker) {
		t.Fatalf("classifier leaked the raw Discord body: %q", c.UserDetail)
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
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("a transient mention fault must NOT fall through to name search; got calls %v", gm.Calls())
	}
	if rec.count != 1 {
		t.Fatalf("a transient (5xx) mention fault must capture to Sentry once; got %d", rec.count)
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
// 4xx-vs-5xx Sentry split without a live Sentry client.
type captureRecorder struct {
	count int
}

func (c *captureRecorder) install(t *testing.T) {
	t.Helper()
	prev := captureError
	captureError = func(msg string, err error, kv ...any) { c.count++ }
	t.Cleanup(func() { captureError = prev })
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
	if rec.count != 1 {
		t.Fatalf("a 5xx role add must capture to Sentry once; got %d", rec.count)
	}
}

// A non-403 4xx (here a 404) is still an operator/config-fixable client fault:
// it must show the generic "Discord rejected the request" wording, never the
// raw body, and never capture to Sentry. Covers the default arm of
// roleMutationErrorReply, which no other mutation-site test exercises.
func TestRunWardenAdd_RoleAddGeneric4xxNoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := wardenRoleAddGM(restError(http.StatusNotFound, 10011, rawBodyMarker))
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
