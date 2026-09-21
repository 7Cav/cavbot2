package commands

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// captureError is the Sentry-capture seam used by /warden's error-handling
// paths. It points at utils.CaptureError in production; tests swap it to assert
// that only genuine system faults (5xx/transport) page Sentry, per ADR 0001.
var captureError = utils.CaptureError

// discordErrorClass is the result of classifying an error returned by a Discord
// REST call. It separates operator/config-fixable client faults (4xx) from
// genuine system faults (5xx and transport errors), and never carries the raw
// Discord response body — discordgo's RESTError.Error() returns
// "HTTP <status>, <full JSON body>", which we must keep out of user-facing
// replies (ADR 0001: CaptureError is for genuine internal failures only).
type discordErrorClass struct {
	// SystemFault is true for 5xx responses, transport errors, and the
	// config-fault 404s (a stale/deleted role or a wrong guild ID — see NotFound).
	// Only these should be sent to Sentry via utils.CaptureError; ordinary,
	// operator-fixable 4xx client faults must not page on-call.
	SystemFault bool
	// ConfigFault is true only for the config-fault 404s (a stale/deleted role or
	// a wrong guild ID; see NotFound). It is always set alongside SystemFault, so
	// the capture-on-SystemFault path is unchanged and these still page on-call.
	// It exists purely so the render layer can tell a config fault apart from a
	// transient 5xx/transport fault: a config fault gets an operator line that
	// names the stale role / wrong guild and surfaces UserDetail, dropping the
	// "try again shortly" hint that cannot clear a condition which will not
	// resolve on its own. A genuine 5xx/transport fault leaves it false and keeps
	// the transient retry wording.
	ConfigFault bool
	// MissingPermissions is true for a 403 Forbidden, the common case where the
	// bot lacks Manage Roles or the target role sits above the bot's own role.
	// Callers use it to render a specific, actionable hierarchy hint.
	MissingPermissions bool
	// NotFound is true only for a 404 that means the targeted entity is genuinely
	// absent: Discord's Unknown Member (10007) code, or a bare 404 with no
	// application error code. On a member-by-ID/mention lookup the caller renders
	// it as "not in this server" rather than capturing or downgrading into a name
	// search. A 404 carrying Unknown Role (10011), Unknown Guild (10004), or any
	// other unexpected code is NOT a genuine absence — it is a stale-ID/config
	// fault routed to SystemFault instead, so a role deleted mid-operation pages
	// on-call rather than misreporting every member as absent.
	NotFound bool
	// UserDetail is a short, body-free phrase safe to show an operator. It never
	// contains the raw Discord error body.
	UserDetail string
}

// classifyDiscordError inspects err for a *discordgo.RESTError and branches on
// its HTTP status class. Anything without a RESTError (transport/connection
// errors, context cancellation) is treated as a system fault. The returned
// UserDetail is always a fixed, sanitized phrase — the raw response body is
// deliberately discarded so it can never reach a user-facing reply.
func classifyDiscordError(err error) discordErrorClass {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Response == nil {
		// No structured HTTP response: transport/connection failure or an
		// unexpected error shape. Treat as a system fault and capture.
		return discordErrorClass{
			SystemFault: true,
			UserDetail:  "could not reach Discord",
		}
	}

	status := restErr.Response.StatusCode

	switch {
	case status == http.StatusForbidden:
		return discordErrorClass{
			MissingPermissions: true,
			UserDetail:         "missing permissions",
		}
	case status == http.StatusNotFound:
		return classifyNotFound(restErr)
	case status >= 400 && status < 500:
		return discordErrorClass{
			UserDetail: "Discord rejected the request",
		}
	default:
		// 5xx (and any unexpected >=500) is a Discord-side system fault.
		return discordErrorClass{
			SystemFault: true,
			UserDetail:  "Discord returned a server error",
		}
	}
}

// classifyNotFound splits a 404 by its Discord application error code, because a
// 404 alone is ambiguous: a role-add (PUT .../members/{user}/roles/{role}) can
// 404 for an absent member, a stale/deleted role, or a wrong guild ID. Reading
// restErr.Message.Code tells them apart, the same Message.Code pattern
// isInteractionTokenExpired uses below.
//
//   - Unknown Member (10007), or a bare 404 with no application error code, is a
//     genuine absence: NotFound, non-captured, rendered as "not in this server".
//   - Unknown Role (10011), Unknown Guild (10004), or any other unexpected code
//     is a stale-ID/config fault: SystemFault, so it pages on-call (ADR 0001)
//     instead of a deleted role silently misreporting every member as absent.
//
// The raw response body is still discarded — only a sanitized UserDetail phrase
// is carried, so nothing leaks into a user-facing reply.
func classifyNotFound(restErr *discordgo.RESTError) discordErrorClass {
	code := 0
	if restErr.Message != nil {
		code = restErr.Message.Code
	}

	if code == 0 || code == discordgo.ErrCodeUnknownMember {
		return discordErrorClass{
			NotFound:   true,
			UserDetail: "not found",
		}
	}

	return discordErrorClass{
		SystemFault: true,
		ConfigFault: true,
		UserDetail:  "unknown role or guild",
	}
}

// configFaultHint is the body-free operator hint for a config-fault 404 (a
// stale/deleted role or a wrong guild ID; see classifyNotFound). It names the
// misconfiguration and surfaces the classifier's sanitized UserDetail phrase, and
// gives the real fix instead of the "try again shortly" advice that a transient
// system fault gets — a deleted role or wrong guild ID will not clear by
// retrying. It only interpolates UserDetail, never the raw Discord body. The
// SystemFault render arms call this when class.ConfigFault is set; otherwise they
// keep their own transient retry wording.
func configFaultHint(class discordErrorClass) string {
	return fmt.Sprintf(
		"a stale or deleted role, or a wrong guild ID (%s). Re-resolve the role or correct the guild ID; retrying won't help",
		class.UserDetail,
	)
}

// isInteractionTokenExpired reports whether err is Discord's signal that the
// interaction's 15-minute token window has closed, so the deferred-ephemeral
// edit can no longer be delivered. It is deliberately narrow: only the specific
// application error codes Discord returns once the webhook token is gone count
// as expiry — a 5xx or a transport error is an unexpected fault, not expiry,
// and must take the capture-to-Sentry path instead. A precise predicate keeps a
// long purge from silently swallowing a genuine delivery fault as "just
// expired".
//
//   - 50027 Invalid Webhook Token — the interaction-followup webhook token is no
//     longer valid (the canonical post-window signal).
//   - 10015 Unknown Webhook / 10062 Unknown Interaction — the webhook/interaction
//     backing the deferred response is gone, the same end-of-window condition.
func isInteractionTokenExpired(err error) bool {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Message == nil {
		return false
	}
	switch restErr.Message.Code {
	case discordgo.ErrCodeInvalidWebhookTokenProvided,
		discordgo.ErrCodeUnknownWebhook,
		discordgo.ErrCodeUnknownInteraction:
		return true
	default:
		return false
	}
}

// lookupFaultSink receives a genuine member-lookup system fault (a 5xx/transport
// error, or a wrong-guild config 404) together with the roster entry
// that triggered it. The single /warden add and /warden remove sites pass nil,
// which makes the lookup capture to Sentry inline with its own message and
// context. The /warden bulkadd loop passes a sink backed by a faultCollector, so
// a lookup-fault storm across an N-entry roster collapses to one Sentry event per
// fault signature instead of N (#216). The leaf gates on class.SystemFault before
// calling the sink, mirroring the inline capture, so non-captured client faults
// (403, not-in-server 404) never reach it (ADR 0001).
type lookupFaultSink func(err error, sampleUser string)

// recordLookupFault routes a genuine member-lookup system fault. With a nil sink
// (the single add/remove sites) it captures to Sentry immediately with the call
// site's own message and context, preserving the pre-#216 inline behavior. With a
// non-nil sink (the bulk loop) it hands the fault to the collector instead, so the
// inline message/context is dropped in favor of the collector's collapsed
// per-signature payload. Only call this for class.SystemFault errors, the same
// gate the inline capture used.
func recordLookupFault(sink lookupFaultSink, err error, sampleUser, captureMsg string, kv ...any) {
	if sink != nil {
		sink(err, sampleUser)
		return
	}
	captureError(captureMsg, err, kv...)
}

// searchErrorMessage builds the body-free, operator-facing error for a
// GuildMembersSearch failure from its classification. It does NOT capture — the
// caller decides whether and how to send the fault to Sentry (inline for the
// single lookup sites, via the faultCollector for the bulk loop). The raw Discord
// response body is never interpolated.
func searchErrorMessage(class discordErrorClass) error {
	if class.SystemFault {
		if class.ConfigFault {
			return fmt.Errorf("❌ Member search failed: %s", configFaultHint(class))
		}
		return errors.New("❌ Member search is temporarily unavailable (Discord error); please try again shortly")
	}
	return fmt.Errorf("❌ Member search failed (%s); check the query or try a mention/ID instead", class.UserDetail)
}

// searchErrorReply classifies a GuildMembersSearch failure, routes a genuine
// system fault through recordLookupFault (capturing inline when faultSink is nil,
// or collecting it when the bulk loop supplies a collector-backed sink), and
// returns the same body-free, operator-facing error searchErrorMessage builds.
// sampleUser is the roster entry that triggered the lookup, carried into the
// collapsed bulk event as sample_user; it is ignored on the inline path.
func searchErrorReply(err error, faultSink lookupFaultSink, sampleUser string) error {
	class := classifyDiscordError(err)
	if class.SystemFault {
		recordLookupFault(faultSink, err, sampleUser, "Failed to search members")
	}
	return searchErrorMessage(class)
}

// purgeRecreateErrorReply classifies a purge role-recreation failure where the
// new role was NOT left behind (the create itself failed, or an overwrite step
// failed and the new role was cleaned up). It captures to Sentry only for
// genuine system faults (per ADR 0001) and returns a body-free summary line. The
// raw Discord response body (discordgo's "HTTP <status>, <json>") is never
// interpolated: only a sanitized phrase from the classifier is shown. The line
// names the role and states it was not recreated, so the operator knows nothing
// new lingers and can retry.
//
// The distinct partial-success case (new role created but the OLD role could not
// be deleted, leaving a duplicate) is handled by purgePartialDeleteSummary, not
// here — that path must tell the operator the opposite, that a role DOES linger.
func purgeRecreateErrorReply(roleName string, err error, captureMsg string, kv ...any) string {
	class := classifyDiscordError(err)

	switch {
	case class.SystemFault:
		captureError(captureMsg, err, kv...)
		if class.ConfigFault {
			return fmt.Sprintf(
				"❌ Failed to recreate '%s': %s. The role was not recreated.",
				roleName, configFaultHint(class),
			)
		}
		return fmt.Sprintf(
			"❌ Failed to recreate '%s': Discord error, the role was not recreated; please try again shortly.",
			roleName,
		)
	case class.MissingPermissions:
		return fmt.Sprintf(
			"❌ Failed to recreate '%s': missing permissions. The bot needs Manage Roles and its own role must sit above '%s'. The role was not recreated.",
			roleName, roleName,
		)
	default:
		return fmt.Sprintf(
			"❌ Failed to recreate '%s': Discord rejected the request (%s). The role was not recreated.",
			roleName, class.UserDetail,
		)
	}
}

// purgePartialDeleteSummary renders the partial-success case: the new role was
// created and fully configured, but deleting the OLD role failed, so a duplicate
// now exists in the guild. This is the inverse of purgeRecreateErrorReply — the
// recreate DID happen, and the operator must be told a role lingers and needs
// manual cleanup, not that nothing was created. The underlying delete error is
// routed through the classifier for capture (genuine system faults page Sentry,
// per ADR 0001) but its raw body is never shown; the summary names both the new
// and old role IDs so the operator can find and remove the leftover.
func purgePartialDeleteSummary(roleName, newRoleID, oldRoleID string, err error, captureMsg string, kv ...any) string {
	if classifyDiscordError(err).SystemFault {
		captureError(captureMsg, err, kv...)
	}
	return fmt.Sprintf(
		"⚠️ Recreated '%s' (new: `%s`), but the old role (`%s`) could not be deleted and still exists. Delete it manually to remove the duplicate.",
		roleName, newRoleID, oldRoleID,
	)
}

// roleResolveErrorReply classifies a GuildRoles lookup failure raised while
// resolving a warden role by name, captures it to Sentry only for genuine system
// faults (5xx/transport, per ADR 0001), and returns a body-free, operator-facing
// error. The explicit not-found result is handled by the caller before this is
// reached and is deliberately never routed here, so it stays non-captured. The
// raw Discord response body (discordgo's "HTTP <status>, <json>") is never
// interpolated — only a sanitized classifier phrase is shown. captureMsg/kv carry
// the call site's command/guild context to Sentry.
func roleResolveErrorReply(err error, captureMsg string, kv ...any) error {
	class := classifyDiscordError(err)
	if class.SystemFault {
		captureError(captureMsg, err, kv...)
		if class.ConfigFault {
			return fmt.Errorf("❌ Failed to retrieve guild roles: %s", configFaultHint(class))
		}
		return errors.New("❌ Failed to retrieve guild roles (Discord error); please try again shortly")
	}
	return fmt.Errorf("❌ Failed to retrieve guild roles (%s)", class.UserDetail)
}

// channelsResolveErrorReply classifies a GuildChannels lookup failure raised
// while resolving a guild's channels for a warden purge, captures it to Sentry
// only for genuine system faults (5xx/transport, per ADR 0001), and returns a
// body-free, operator-facing error. It mirrors roleResolveErrorReply one call
// site below, with wording tailored to channels rather than roles. The raw
// Discord response body (discordgo's "HTTP <status>, <json>") is never
// interpolated — only a sanitized classifier phrase is shown. captureMsg/kv
// carry the call site's command/guild context to Sentry.
func channelsResolveErrorReply(err error, captureMsg string, kv ...any) error {
	class := classifyDiscordError(err)
	if class.SystemFault {
		captureError(captureMsg, err, kv...)
		if class.ConfigFault {
			return fmt.Errorf("❌ Failed to retrieve guild channels: %s", configFaultHint(class))
		}
		return errors.New("❌ Failed to retrieve guild channels (Discord error); please try again shortly")
	}
	return fmt.Errorf("❌ Failed to retrieve guild channels (%s)", class.UserDetail)
}

// roleMutationErrorMessage builds the body-free, actionable user-facing message
// for a role add/remove failure from its classification. It does NOT capture —
// the caller decides whether and how to send the fault to Sentry. The single
// add/remove sites capture immediately via roleMutationErrorReply; the PUBLIC
// /warden bulkadd loop builds its per-member failure line here and routes the
// capture through faultCollector so a per-member storm collapses to one event
// per signature (#214). (The internal bulkadd loop also routes its captures
// through the collector, but lists faulted members by forum username in bucketed
// summaries rather than calling this.) A 403 yields a specific role-hierarchy
// hint — the common cause is the target role sitting above the bot's own role,
// or the bot missing Manage Roles. The raw Discord body is never interpolated;
// only the classifier's sanitized phrase appears.
func roleMutationErrorMessage(action, roleName, userLabel string, class discordErrorClass) string {
	switch {
	case class.SystemFault:
		if class.ConfigFault {
			return fmt.Sprintf("❌ Could not %s '%s' for %s: %s.", action, roleName, userLabel, configFaultHint(class))
		}
		return fmt.Sprintf("❌ Could not %s '%s' for %s: Discord error, please try again shortly.", action, roleName, userLabel)
	case class.MissingPermissions:
		return fmt.Sprintf(
			"❌ Could not %s '%s' for %s: missing permissions — the bot needs Manage Roles and its own role must be positioned above '%s'.",
			action, roleName, userLabel, roleName,
		)
	default:
		return fmt.Sprintf("❌ Could not %s '%s' for %s: Discord rejected the request.", action, roleName, userLabel)
	}
}

// roleMutationErrorReply is the immediate-capture entry point for the single
// /warden add and remove sites: it classifies a role add/remove failure,
// captures it to Sentry once when it is a genuine system fault, and returns the
// same body-free message roleMutationErrorMessage builds. captureMsg/kv are
// forwarded to captureError so each site keeps its own log context. The bulk
// loops deliberately do NOT use this — they would capture once per member; both
// feed their system faults to a faultCollector instead (#214). The public
// /warden bulkadd loop builds its per-member line via roleMutationErrorMessage;
// the internal bulkadd loop lists faulted members by forum username in bucketed
// summaries.
func roleMutationErrorReply(action, roleName, userLabel string, err error, captureMsg string, kv ...any) string {
	class := classifyDiscordError(err)
	if class.SystemFault {
		captureError(captureMsg, err, kv...)
	}
	return roleMutationErrorMessage(action, roleName, userLabel, class)
}

// faultSignature identifies a distinct captured fault for collapse: the pair of
// HTTP status and Discord application error code. It is read straight off the
// *discordgo.RESTError (the same status and Message.Code the classifier branches
// on), so the collector does not need classifyDiscordError's contract to change.
// A transport/connection error has no HTTP response, so it keys on the zero
// signature {0, 0}; that is distinct from a real 5xx (status 500, code 0), so
// the two never fold together.
type faultSignature struct {
	status      int
	discordCode int
}

// faultSignatureOf extracts the (status, Discord code) signature from a Discord
// REST failure. Anything without a structured HTTP response (a transport error)
// returns the zero signature.
func faultSignatureOf(err error) faultSignature {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Response == nil {
		return faultSignature{}
	}
	sig := faultSignature{status: restErr.Response.StatusCode}
	if restErr.Message != nil {
		sig.discordCode = restErr.Message.Code
	}
	return sig
}

// collectedFault is the running tally for one fault signature within a single
// bulk run: how many faults hit it, a representative error for the Sentry
// payload, and the first roster entry that hit it (the debugging foothold the
// collapsed event carries as sample_user). The collector type serves both the
// role-add and lookup phases (each phase using its own instance), so sampleUser
// is NOT always a Discord ID: it is a resolved Discord ID for the by-ID lookup
// and role-add sites, but the raw search term (e.g. "alice") for a name lookup.
type collectedFault struct {
	count      int
	firstErr   error
	sampleUser string
}

// faultCollector collapses the per-entry captured system faults of a bulk run
// into one Sentry event per distinct fault signature, so a single root cause
// that hits every iteration (a role deleted mid-run ⇒ N Unknown Role 404s, or a
// 5xx storm) pages on-call once instead of N times (#214, #216). It serves both
// phases of the bulk loop — the member-lookup phase
// (GuildMember/GuildMembersSearch) and the role-add phase (GuildMemberRoleAdd) —
// each of which constructs its own collector, feeds it every captured fault during
// iteration via recordSystemFault, and flushes once after the loop. It is
// per-invocation only: a fresh collector per run, no cross-run or time-windowed
// dedup. Only genuine system faults belong here — the caller filters on
// class.SystemFault, since non-captured client faults (403, not-in-server 404)
// must stay uncaptured per ADR 0001.
type faultCollector struct {
	// order preserves first-seen signature order so flush emits deterministically.
	order   []faultSignature
	entries map[faultSignature]*collectedFault
}

func newFaultCollector() *faultCollector {
	return &faultCollector{entries: map[faultSignature]*collectedFault{}}
}

// recordSystemFault records one captured system fault for the signature of err,
// attributed to the roster-entry sampleUser (a resolved Discord ID for the by-ID
// lookup and role-add callers, or the raw search term for a name lookup — see
// collectedFault). It is for genuine system faults ONLY: every error handed here
// is sent to Sentry by flush, so every caller gates on class.SystemFault before
// calling it — non-captured client faults (403, not-in-server 404) must never
// reach the collector (ADR 0001, #214, #216). The first sample per signature is
// kept; every subsequent same-signature fault only bumps the count. The count's
// meaning depends on the caller: for the role-add phase, which adds several roles
// per member, each failed member-role attempt is one call, so affected_count
// counts attempts; for the lookup phase, which resolves one entry per iteration,
// affected_count counts failed lookups (one per entry).
//
// The entries map is lazy-initialized here, so the zero-value faultCollector is
// safe to record into even if a caller skipped newFaultCollector(); it can never
// nil-panic on the first record.
func (fc *faultCollector) recordSystemFault(err error, sampleUser string) {
	sig := faultSignatureOf(err)
	if fc.entries == nil {
		fc.entries = map[faultSignature]*collectedFault{}
	}
	entry, ok := fc.entries[sig]
	if !ok {
		entry = &collectedFault{firstErr: err, sampleUser: sampleUser}
		fc.entries[sig] = entry
		fc.order = append(fc.order, sig)
	}
	entry.count++
}

// flush emits one captureError per distinct signature collected this run, each
// carrying the affected attempt count, the sample member, and the signature
// (status + Discord code) as searchable values, appended to the caller's shared
// run context (command/guild, plus unit for the internal command). Calling it on
// a collector that saw no system faults is a no-op, so a clean run pages nothing.
func (fc *faultCollector) flush(captureMsg string, baseKV ...any) {
	for _, sig := range fc.order {
		entry := fc.entries[sig]
		kv := append([]any(nil), baseKV...)
		kv = append(kv,
			"affected_count", entry.count,
			"sample_user", entry.sampleUser,
			"http_status", sig.status,
			"discord_code", sig.discordCode,
		)
		captureError(captureMsg, entry.firstErr, kv...)
	}
}
