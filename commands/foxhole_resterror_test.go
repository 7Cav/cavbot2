package commands

import (
	"errors"
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

// captureRecorder swaps the package-level captureError seam for the duration of
// a test and counts how many times it fires, so a test can assert the
// 4xx-vs-5xx Sentry split without a live Sentry client. lastKV holds the kv
// varargs from the most recent capture, so a test can also pin the context
// fields (command/guild/role) a site is required to forward. kvs keeps every
// capture's kv in fire order, so a test that expects more than one capture (the
// bulk-loop fault collapse, #214) can assert per-event payloads, not just the
// most recent. msgs keeps every capture's message in the same fire order, so a
// test can tell the two bulk collectors apart by their distinct flush messages
// (the lookup vs role-add separation invariant, #216).
type captureRecorder struct {
	count  int
	lastKV []any
	kvs    [][]any
	msgs   []string
	errs   []error
}

func (c *captureRecorder) install(t *testing.T) {
	t.Helper()
	prev := captureError
	captureError = func(msg string, err error, kv ...any) {
		c.count++
		c.lastKV = kv
		c.kvs = append(c.kvs, kv)
		c.msgs = append(c.msgs, msg)
		c.errs = append(c.errs, err)
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

// discordFailure is one kind of answer Discord gives a failed call, the
// advice a reply gives for it, and whether it is a system fault that pages
// Sentry (ADR 0001).
type discordFailure struct {
	name     string
	err      error
	advice   string
	captured bool
}

// The answers a Foxhole command meets from Discord, one of each kind
// classifyDiscordError tells apart.
var (
	serverError = discordFailure{"server error", restError(http.StatusInternalServerError, 0, rawBodyMarker), adviceTransient, true}
	transport   = discordFailure{"transport error", errors.New("dial tcp: connection refused " + rawBodyMarker), adviceTransient, true}
	unknownRole = discordFailure{"404 Unknown Role", restError(http.StatusNotFound, discordgo.ErrCodeUnknownRole, rawBodyMarker), adviceConfigFault, true}
	// unknownGuild is a wrong GUILD_ID, which any Discord call can meet.
	unknownGuild = discordFailure{"404 Unknown Guild", restError(http.StatusNotFound, discordgo.ErrCodeUnknownGuild, rawBodyMarker), adviceConfigFault, true}
	forbidden    = discordFailure{"403", restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker), adviceMissingPermissions, false}
	badRequest   = discordFailure{"400", restError(http.StatusBadRequest, 50035, rawBodyMarker), adviceRejected, false}
)

// assertFailureReply fails unless reply is one ❌ line carrying the
// failure's advice and none of the other kinds', without the raw Discord
// body, and Sentry got one event exactly when the failure is a system
// fault.
func assertFailureReply(t *testing.T, reply string, failure discordFailure, rec *captureRecorder) {
	t.Helper()
	assertAdvice(t, reply, failure.advice)
	if strings.Contains(reply, rawBodyMarker) {
		t.Errorf("reply %q leaks the raw Discord body", reply)
	}
	want := 0
	if failure.captured {
		want = 1
	}
	if rec.count != want {
		t.Errorf("Sentry got %d events, want %d", rec.count, want)
	}
}

// --- member lookup: one reply for each kind of Discord failure ---

// A mention names one member, so /foxhole add looks them up by ID and never
// falls back to a name search of the raw "<@...>" text, whatever Discord
// answers. Each kind of failure gets its own advice: a member who isn't in
// the server is told apart from a stale role or wrong guild, from a fault
// worth retrying, from missing permissions and from any other refusal. Only
// a system fault pages Sentry.
func TestFoxholeMemberLookupAdvisesOnEachKindOfDiscordFailure(t *testing.T) {
	absent := func(name string, err error) discordFailure {
		return discordFailure{name, err, adviceAbsent, false}
	}
	failures := []discordFailure{
		serverError,
		transport,
		absent("404 Unknown Member", restError(http.StatusNotFound, discordgo.ErrCodeUnknownMember, rawBodyMarker)),
		absent("bare 404", restError(http.StatusNotFound, 0, rawBodyMarker)),
		// discordgo leaves Message nil when a 404's body isn't JSON.
		absent("404 with an unparseable body", &discordgo.RESTError{
			Response:     &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"},
			ResponseBody: []byte("not parseable json " + rawBodyMarker),
		}),
		unknownRole,
		unknownGuild,
		// Any 404 code but Unknown Member is a config fault, not only the
		// codes the classifier names.
		{"404 with an unexpected code", restError(http.StatusNotFound, 12345, rawBodyMarker), adviceConfigFault, true},
		forbidden,
		badRequest,
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			gm := &fakeGuildManager{
				roles:      []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)},
				MemberErrs: []error{failure.err},
			}
			f := &fakeResponder{}

			runFoxhole(f, gm, nil, foxholeInteraction("guild-1",
				stringOption("command", "add"),
				stringOption("flag", "internal"),
				stringOption("discordname", "<@123456789012345678>"),
			))

			reply := lastEditContent(f.Calls())
			if got := verdicts(reply, ""); len(got) != 1 || got[0] != verdictFailed {
				t.Errorf("the reply's verdicts are %q, want one %q", got, verdictFailed)
			}
			assertFailureReply(t, reply, failure, rec)
			if n := gm.countCalls("GuildMembersSearch"); n != 0 {
				t.Errorf("the add searched by name %d times after its lookup by ID", n)
			}
			if n := gm.countCalls("GuildMemberRoleAdd"); n != 0 {
				t.Errorf("the add changed %d roles for a member it couldn't look up", n)
			}
		})
	}
}

// A mention or a raw snowflake ID names one member: /foxhole add looks them
// up by that ID and never searches by name, whether they are there or not.
func TestRunFoxholeAdd_MentionOrSnowflakeLooksTheMemberUpByID(t *testing.T) {
	t.Run("a mention of a member in the server", func(t *testing.T) {
		gm := foxholeRoleAddGM(nil)
		f := &fakeResponder{}

		runFoxhole(f, gm, nil, foxholeInteraction("guild-1",
			stringOption("command", "add"),
			stringOption("flag", "internal"),
			stringOption("discordname", "<@123456789012345678>"),
		))

		want := []roleAddCall{{guildID: "guild-1", userID: "123456789012345678", roleID: "r-int"}}
		if got := gm.roleAddCalls(); len(got) != 1 || got[0] != want[0] {
			t.Errorf("role adds = %+v, want %+v", got, want)
		}
		if n := gm.countCalls("GuildMembersSearch"); n != 0 {
			t.Errorf("the add searched by name %d times for a mention", n)
		}
	})

	t.Run("a snowflake of a member not in the server", func(t *testing.T) {
		gm := &fakeGuildManager{
			roles:      []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)},
			MemberErrs: []error{restError(http.StatusNotFound, discordgo.ErrCodeUnknownMember, rawBodyMarker)},
		}
		f := &fakeResponder{}

		runFoxhole(f, gm, nil, foxholeAddInteraction())

		reply := lastEditContent(f.Calls())
		assertVerdict(t, reply, "123456789012345678", verdictFailed)
		assertAdvice(t, reply, adviceAbsent)
		if n := gm.countCalls("GuildMembersSearch"); n != 0 {
			t.Errorf("the add searched by name %d times for a snowflake", n)
		}
	})
}

// A name search that Discord fails gets the advice for that kind of
// failure, and only a system fault pages Sentry.
func TestFoxholeNameSearchAdvisesOnEachKindOfDiscordFailure(t *testing.T) {
	for _, failure := range []discordFailure{serverError, unknownGuild, badRequest} {
		t.Run(failure.name, func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			gm := &fakeGuildManager{
				roles:             []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)},
				MembersSearchErrs: []error{failure.err},
			}
			f := &fakeResponder{}

			runFoxhole(f, gm, nil, foxholeInteraction("guild-1",
				stringOption("command", "add"),
				stringOption("flag", "internal"),
				stringOption("discordname", "somename"),
			))

			reply := lastEditContent(f.Calls())
			if got := verdicts(reply, ""); len(got) != 1 || got[0] != verdictFailed {
				t.Errorf("the reply's verdicts are %q, want one %q", got, verdictFailed)
			}
			assertFailureReply(t, reply, failure, rec)
		})
	}
}

// --- role add/remove paths: one reply for each kind of Discord failure ---

func foxholeAddInteraction() *discordgo.InteractionCreate {
	return foxholeInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)
}

func foxholeRemoveInteraction() *discordgo.InteractionCreate {
	return foxholeInteraction("guild-1",
		stringOption("command", "remove"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)
}

func foxholeRoleAddGM(addErr error) *fakeGuildManager {
	return &fakeGuildManager{
		roles:             []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		membersByID:       map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleAddErrs: []error{addErr},
	}
}

// When Discord refuses the role add, /foxhole add's reply names the member
// and the role it couldn't give, with the advice for that kind of failure.
// A missing permission also names what to fix: Manage Roles, and the role
// the bot's own role must sit above. A role deleted since it was resolved is
// a config fault: it pages Sentry, and retrying won't clear it.
func TestFoxholeRoleAddAdvisesOnEachKindOfDiscordFailure(t *testing.T) {
	for _, failure := range []discordFailure{serverError, unknownRole, forbidden, badRequest} {
		t.Run(failure.name, func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			f := &fakeResponder{}

			runFoxhole(f, foxholeRoleAddGM(failure.err), nil, foxholeAddInteraction())

			reply := lastEditContent(f.Calls())
			assertVerdict(t, reply, "trooper", verdictFailed)
			assertReplyNames(t, reply, defaultInternalRoleName)
			assertFailureReply(t, reply, failure, rec)
			if failure.advice == adviceMissingPermissions {
				assertReplyNames(t, reply, "Manage Roles")
			}
		})
	}
}

// /foxhole remove shares the add's failure replies: a Discord server error
// on the removal names the member, keeps the advice to retry, and pages
// Sentry once.
func TestRunFoxholeRemove_RoleRemove5xxCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles:                []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		membersByID:          map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleRemoveErrs: []error{serverError.err},
	}
	f := &fakeResponder{}

	runFoxhole(f, gm, nil, foxholeRemoveInteraction())

	reply := lastEditContent(f.Calls())
	assertVerdict(t, reply, "trooper", verdictFailed)
	assertFailureReply(t, reply, serverError, rec)
}

// --- bulkadd: 4xx role-add failure must not capture but still report ---

func TestRunFoxholeBulkAdd_RoleAdd403NoCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
		},
		MemberRoleAddErrs: []error{restError(http.StatusForbidden, 50013, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", "good"),
	)

	runFoxhole(f, gm, nil, i)

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("bulkadd 403 leaked the raw Discord body: %q", got)
	}
	if rec.count != 0 {
		t.Fatalf("a 403 bulk role add must NOT capture to Sentry; got %d", rec.count)
	}
}

func TestRunFoxholeBulkAdd_RoleAdd5xxCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
		},
		MemberRoleAddErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", "good"),
	)

	runFoxhole(f, gm, nil, i)

	got := lastEditContent(f.Calls())
	if strings.Contains(got, rawBodyMarker) {
		t.Fatalf("bulkadd 5xx leaked the raw Discord body: %q", got)
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx bulk role add must capture to Sentry once; got %d", rec.count)
	}
}
