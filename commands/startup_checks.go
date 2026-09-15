package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Startup checks (issue #287). Two things the temporary voice channel feature
// assumes about the guild can change under the bot without any code change:
// the rank ladder in code can drift from the milpacs API, and the bot can
// lose Administrator. Either breaks spawning or handover in a way nobody
// would notice from a WARN line, so each is a Sentry event (ADR 0001). A
// failed fetch of either is a WARN line, not a capture: the check could not
// run, which is not the same as the guild having changed.

// startupChecksTimeout bounds the ranks fetch. The Discord reads go through
// discordgo's own client timeout.
const startupChecksTimeout = 30 * time.Second

// RunStartupChecks runs both checks once, for the guild the bot serves, as
// the bot user READY reported. Call it from a goroutine after READY so the
// checks never sit between the gateway and its handlers; each check is one
// fetch or two and then returns.
func RunStartupChecks(mgr TempVCManager, guildID, botUserID string) {
	defer utils.RecoverPanic("startup-checks")
	ctx, cancel := context.WithTimeout(context.Background(), startupChecksTimeout)
	defer cancel()
	checkRankLadder(ctx)
	checkAdministrator(mgr, guildID, botUserID)
}

// apiTesterRankFull is the one entry the ranks endpoint carries that the code
// ladder does not: a roster-side test rank with no Discord role.
const apiTesterRankFull = "Tester"

// errRankLadderDrift is the error a drift capture carries. The differing
// entries travel in the capture's key/values, so the event groups on the
// message and the detail stays readable in Sentry's context pane.
var errRankLadderDrift = errors.New("rank ladder in code differs from the milpacs API")

// checkRankLadder compares the rank ladder in code against the milpacs ranks
// endpoint, abbreviations and order, and captures any drift once. The API's
// Tester entry is dropped first, and the API's order is its display order.
func checkRankLadder(ctx context.Context) {
	resp, err := utils.GetRanks(ctx)
	if err != nil {
		utils.Warn("Rank ladder check skipped: ranks fetch failed", "error", err)
		return
	}

	api := apiLadderAbbrevs(resp.Ranks)
	code := make([]string, 0, len(tempVCRankRoles))
	for _, rr := range tempVCRankRoles {
		code = append(code, rr.abbrev)
	}
	if slices.Equal(api, code) {
		utils.Info("Rank ladder check passed", "ranks", len(code))
		return
	}
	captureError("Rank ladder drift", errRankLadderDrift,
		"code_ladder", code,
		"api_ladder", api,
		"differing", ladderDiff(code, api),
	)
}

// apiLadderAbbrevs returns the API's abbreviations in display order, without
// the Tester entry.
func apiLadderAbbrevs(ranks []utils.RankEntry) []string {
	kept := make([]utils.RankEntry, 0, len(ranks))
	for _, r := range ranks {
		if r.RankFull != apiTesterRankFull {
			kept = append(kept, r)
		}
	}
	slices.SortStableFunc(kept, func(a, b utils.RankEntry) int {
		return a.RankDisplayOrder - b.RankDisplayOrder
	})
	out := make([]string, 0, len(kept))
	for _, r := range kept {
		out = append(out, r.RankShort)
	}
	return out
}

// ladderDiff lists each position where the two ladders disagree as
// "<position>: code=<abbrev> api=<abbrev>", with a missing side shown as "-".
func ladderDiff(code, api []string) []string {
	var diff []string
	for i := 0; i < max(len(code), len(api)); i++ {
		c, a := "-", "-"
		if i < len(code) {
			c = code[i]
		}
		if i < len(api) {
			a = api[i]
		}
		if c != a {
			diff = append(diff, fmt.Sprintf("%d: code=%s api=%s", i, c, a))
		}
	}
	return diff
}

// errAdministratorMissing is the error the Administrator capture carries.
var errAdministratorMissing = errors.New("bot member does not hold Administrator")

// checkAdministrator reads the bot's own member and the guild's roles and
// captures when no role the bot holds carries Administrator. The guild owner
// holds every permission, so an owner bot passes without a role.
func checkAdministrator(mgr TempVCManager, guildID, botUserID string) {
	member, err := mgr.GuildMember(guildID, botUserID)
	if err != nil {
		utils.Warn("Administrator check skipped: member fetch failed", "error", err, "guild_id", guildID)
		return
	}
	guild, err := mgr.Guild(guildID)
	if err != nil {
		utils.Warn("Administrator check skipped: guild fetch failed", "error", err, "guild_id", guildID)
		return
	}
	if guild.OwnerID == botUserID || holdsAdministrator(member, guild) {
		utils.Info("Administrator check passed", "guild_id", guildID)
		return
	}
	captureError("Bot has lost Administrator", errAdministratorMissing,
		"guild_id", guildID, "bot_user_id", botUserID, "roles", member.Roles)
}

// holdsAdministrator reports whether the everyone role or any role the member
// holds carries the Administrator permission. Administrator is not subject to
// channel overwrites, so a role-level grant is the whole answer.
func holdsAdministrator(member *discordgo.Member, guild *discordgo.Guild) bool {
	held := make(map[string]bool, len(member.Roles)+1)
	held[guild.ID] = true // the everyone role shares the guild's ID
	for _, id := range member.Roles {
		held[id] = true
	}
	for _, role := range guild.Roles {
		if held[role.ID] && role.Permissions&discordgo.PermissionAdministrator != 0 {
			return true
		}
	}
	return false
}
