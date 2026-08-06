package commands

import (
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// commandInvokedMsg is the message marker on the per-invocation telemetry line.
//
// This marker and the field keys built in telemetryFields form a CROSS-REPO
// contract: the metrics host's Alloy collector matches on the marker and reads
// the keys by name to derive Prometheus counters and histograms. Renaming
// either half here breaks metric derivation SILENTLY — the bot keeps logging,
// the dashboards just stop moving. See ADR 0011 and docs/command-telemetry.md,
// which carries the collector's half of the contract.
const commandInvokedMsg = "command_invoked"

// slashCommandPanicContext is the Sentry "context" tag for a panic that escaped
// a slash-command handler. Distinct from main.go's "interaction-handler", which
// now only sees panics from outside a registered command's execution.
const slashCommandPanicContext = "slash-command"

// telemetryNow is the decorator's clock. A package var rather than a direct
// time.Now call so tests can pin elapsed time without sleeping — the same
// reason wardenOverwriteDelay is a var.
var telemetryNow = time.Now

// telemetryFields builds the key/value set for one invocation's telemetry line.
func telemetryFields(name string, i *discordgo.InteractionCreate, elapsed time.Duration) []any {
	username, discordID := interactionUsernameAndID(i)
	fields := []any{
		"command", name,
		"latency_ms", elapsed.Milliseconds(),
		"discord_id", discordID,
		"username", username,
	}

	subcommand, options := unwrapSubcommand(i.ApplicationCommandData().Options)
	if subcommand != "" {
		fields = append(fields, "subcommand", subcommand)
	}
	for _, opt := range options {
		if opt == nil {
			continue
		}
		fields = append(fields, "opt_"+opt.Name, boundOptionValue(opt.Value))
	}
	return fields
}

// maxOptionValueRunes caps how much of a single option value rides the
// telemetry line. Discord allows a multi-thousand-character string option
// (/warden bulkadd takes up to maxBulkAddEntries names in one), and the metrics
// host ingests every line the bot emits — an unbounded copy would make routine
// bulk operations the largest thing in the log stream. Enough to recognize the
// input in a Loki drill-down, not enough to carry it.
const maxOptionValueRunes = 64

// boundOptionValue truncates a string option to maxOptionValueRunes. Non-string
// values (numbers, booleans, snowflakes) are bounded by their own types and pass
// through untouched, so they keep their native encoding on the line. Truncation
// is by rune so a multi-byte character is never cut in half.
func boundOptionValue(value any) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	runes := []rune(s)
	if len(runes) <= maxOptionValueRunes {
		return s
	}
	return string(runes[:maxOptionValueRunes])
}

// unwrapSubcommand resolves the invoked subcommand path (if any) and returns the
// options that actually belong to it. Discord nests a subcommand's options one
// level down — and two levels for a subcommand group — so reading the top-level
// options directly would report an empty option set for any command that grows a
// subcommand later. No command in the registry uses true subcommands today
// (warden spells its own as a plain "command" string option), so this exists to
// keep the contract honest when one does.
func unwrapSubcommand(options []*discordgo.ApplicationCommandInteractionDataOption) (string, []*discordgo.ApplicationCommandInteractionDataOption) {
	path := ""
	for len(options) == 1 && options[0] != nil &&
		(options[0].Type == discordgo.ApplicationCommandOptionSubCommand ||
			options[0].Type == discordgo.ApplicationCommandOptionSubCommandGroup) {
		if path != "" {
			path += "."
		}
		path += options[0].Name
		options = options[0].Options
	}
	return path, options
}

// instrument decorates a command handler so that every invocation emits one
// commandInvokedMsg line. Applied at registration (see registry.go) so that
// instrumentation follows the registry's single source of truth (ADR 0006) and
// no per-command work is needed when a command is added.
func instrument(name string, h CommandHandler) CommandHandler {
	return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		// Slash commands only. The dispatcher routes component CustomIDs through
		// GetHandler by their command-name prefix, so this decorator sees button
		// presses too; each one continues a command already counted, and its
		// interaction carries MessageComponentInteractionData, which the field
		// builder's ApplicationCommandData read would panic on.
		if i == nil || i.Interaction == nil || i.Type != discordgo.InteractionApplicationCommand {
			h(s, i)
			return
		}

		// Registered first so it unwinds LAST: the telemetry defer below runs
		// while the goroutine is still panicking and gets its line out, then
		// this one swallows the panic. Recovering here rather than leaving it to
		// main.go's dispatcher-level recover is what lets the Sentry event name
		// the command that actually failed.
		defer utils.RecoverPanic(slashCommandPanicContext, "command", name)

		start := telemetryNow()
		defer func() {
			utils.Info(commandInvokedMsg, telemetryFields(name, i, telemetryNow().Sub(start))...)
		}()
		h(s, i)
	}
}
