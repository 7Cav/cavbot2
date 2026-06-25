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
	// SystemFault is true for 5xx responses and transport errors. Only these
	// should be sent to Sentry via utils.CaptureError; 4xx client/config faults
	// must not page on-call for an ordinary, fixable condition.
	SystemFault bool
	// MissingPermissions is true for a 403 Forbidden, the common case where the
	// bot lacks Manage Roles or the target role sits above the bot's own role.
	// Callers use it to render a specific, actionable hierarchy hint.
	MissingPermissions bool
	// NotFound is true for a 404. On a targeted member-by-ID/mention lookup this
	// means the user is genuinely absent from the guild — a clear, non-system
	// condition the caller renders as "not in this server" rather than capturing
	// or downgrading into a name search.
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
		return discordErrorClass{
			NotFound:   true,
			UserDetail: "not found",
		}
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

// searchErrorReply classifies a GuildMembersSearch failure, captures it to
// Sentry only when it is a genuine system fault, and returns a body-free,
// operator-facing error. The raw Discord response body is never interpolated.
func searchErrorReply(err error) error {
	class := classifyDiscordError(err)
	if class.SystemFault {
		captureError("Failed to search members", err)
		return errors.New("❌ Member search is temporarily unavailable (Discord error); please try again shortly")
	}
	return fmt.Errorf("❌ Member search failed (%s); check the query or try a mention/ID instead", class.UserDetail)
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
		return errors.New("❌ Failed to retrieve guild roles (Discord error); please try again shortly")
	}
	return fmt.Errorf("❌ Failed to retrieve guild roles (%s)", class.UserDetail)
}

// roleMutationErrorReply classifies a role add/remove failure, captures it to
// Sentry only for genuine system faults, and returns a body-free, actionable
// message. A 403 yields a specific role-hierarchy hint — the common cause is the
// target role sitting above the bot's own role, or the bot missing Manage Roles.
// captureMsg/kv are forwarded to captureError so each site keeps its own log
// context (action, user, role).
func roleMutationErrorReply(action, roleName, userLabel string, err error, captureMsg string, kv ...any) string {
	class := classifyDiscordError(err)

	switch {
	case class.SystemFault:
		captureError(captureMsg, err, kv...)
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
