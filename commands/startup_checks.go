package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Startup checks (spec #285). After READY the bot tells on-call, through
// Sentry, when the guild has changed under it. The rank ladder in code no
// longer matches the milpacs API, the bot's own member has lost
// Administrator, or a rank role is gone from the guild (#680). Each one
// breaks spawning or handover with no other signal. A check that cannot fetch what it needs is a WARN line and no
// capture (ADR 0001), and each check runs whether or not the other could.

// startupCheckTimeout bounds the rank ladder fetch. The checks run in their
// own goroutine, so this caps how long a stalled API holds it, nothing more.
const startupCheckTimeout = 30 * time.Second

// testerRankFull names the one entry of the ranks endpoint that is not a
// rank. It sits first in the list and has no place in the ladder.
const testerRankFull = "Tester"

// errRankLadderDrift is the identity of a rank ladder drift event in Sentry.
var errRankLadderDrift = errors.New("rank ladder in code differs from the milpacs API")

// errAdministratorMissing is the identity of a missing Administrator event in
// Sentry. The bot keeps Administrator as its deployment requirement; without
// it spawning, handover and /foxhole all fail at the next call.
var errAdministratorMissing = errors.New("bot member does not hold Administrator")

// errRankRolesMissing is the identity of a missing rank roles event in
// Sentry. Holders of a rank whose role is gone count as Cav members no
// longer, until a panel admin saves another role for the rank.
var errRankRolesMissing = errors.New("rank roles missing from the guild")

// missingRankRole is one rank whose rank role the guild does not have.
type missingRankRole struct {
	Rank   string
	RoleID string
}

// rankDrift is one position where the ladder in code and the API disagree.
// An empty side means that list ended first.
type rankDrift struct {
	Code string
	API  string
}

// RunStartupChecks runs the one-time checks the bot makes after the Discord
// session reaches READY. main starts it in a goroutine so the gateway
// handlers never wait on it. tv is the temp VC runtime, whose rank roles
// the last check reads. A bot with no store has none, and nothing that
// ranks members, so the check is skipped.
func RunStartupChecks(ctx context.Context, mgr TempVCManager, guildID, botUserID string, tv *TempVC) {
	checkRankLadder(ctx)
	checkAdministrator(mgr, guildID, botUserID)
	if tv != nil {
		checkRankRoles(mgr, guildID, tv.RankLadder())
	}
}

// checkRankRoles fetches the guild's roles and captures once, naming every
// rank whose rank role the guild lacks. That catches a role deleted while
// the bot was down, which the role delete handler never saw. A rank with no
// rank role is not missing. On a guild that never saved its rank roles the
// check reads the ladder's role IDs in code, so the test guild captures at
// every start until an admin clears the ranks it lacks.
func checkRankRoles(mgr TempVCManager, guildID string, ladder RankLadder) {
	guild, err := mgr.Guild(guildID)
	if err != nil {
		utils.Warn("Rank roles check skipped, guild fetch failed", "error", err, "guild_id", guildID)
		return
	}
	live := make(map[string]struct{}, len(guild.Roles))
	for _, r := range guild.Roles {
		live[r.ID] = struct{}{}
	}
	var missing []missingRankRole
	for _, r := range ladder.Ranks() {
		if _, ok := live[r.RoleID]; r.RoleID != "" && !ok {
			missing = append(missing, missingRankRole{Rank: r.Abbrev, RoleID: r.RoleID})
		}
	}
	if len(missing) == 0 {
		return
	}
	captureError("Rank roles missing from the guild",
		fmt.Errorf("%w: %d rank(s)", errRankRolesMissing, len(missing)),
		"guild_id", guildID, "missing", missing)
}

// checkRankLadder fetches the milpacs rank list, drops Tester, and captures
// when the abbreviations or their order differ from tempVCRankRoles.
func checkRankLadder(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, startupCheckTimeout)
	defer cancel()
	resp, err := utils.GetRanks(ctx)
	if err != nil {
		utils.Warn("Rank ladder check skipped, ranks fetch failed", "error", err)
		return
	}
	api := make([]string, 0, len(resp.Ranks))
	for _, r := range resp.Ranks {
		if r.RankFull == testerRankFull {
			continue
		}
		api = append(api, r.RankShort)
	}
	drift := rankLadderDrift(tempVCRankRoles, api)
	if len(drift) == 0 {
		return
	}
	captureError("Rank ladder drift",
		fmt.Errorf("%w: %d position(s)", errRankLadderDrift, len(drift)),
		"drift", drift)
}

// checkAdministrator fetches the bot's own member and the guild's roles and
// captures when no held role carries Administrator.
func checkAdministrator(mgr TempVCManager, guildID, botUserID string) {
	member, err := mgr.GuildMember(guildID, botUserID)
	if err != nil {
		utils.Warn("Administrator check skipped, member fetch failed", "error", err, "guild_id", guildID)
		return
	}
	guild, err := mgr.Guild(guildID)
	if err != nil {
		utils.Warn("Administrator check skipped, guild fetch failed", "error", err, "guild_id", guildID)
		return
	}
	if holdsAdministrator(guild, member) {
		return
	}
	captureError("Bot member lacks Administrator", errAdministratorMissing,
		"guild_id", guildID, "user_id", botUserID, "roles", member.Roles)
}

// holdsAdministrator reports whether any role the member holds, the guild's
// @everyone role included, carries the Administrator bit. A member from the
// REST API carries role IDs only; the bits live on the guild's role list.
func holdsAdministrator(guild *discordgo.Guild, member *discordgo.Member) bool {
	held := make(map[string]struct{}, len(member.Roles)+1)
	held[guild.ID] = struct{}{}
	for _, id := range member.Roles {
		held[id] = struct{}{}
	}
	for _, role := range guild.Roles {
		if _, ok := held[role.ID]; !ok {
			continue
		}
		if role.Permissions&discordgo.PermissionAdministrator != 0 {
			return true
		}
	}
	return false
}

// rankLadderDrift walks both lists by position and returns every position
// where they disagree. Order counts. A swap reports both positions, and a
// list that ends first reports the other's tail.
func rankLadderDrift(code []rankRole, api []string) []rankDrift {
	var drift []rankDrift
	for i := 0; i < len(code) || i < len(api); i++ {
		var c, a string
		if i < len(code) {
			c = code[i].abbrev
		}
		if i < len(api) {
			a = api[i]
		}
		if c != a {
			drift = append(drift, rankDrift{Code: c, API: a})
		}
	}
	return drift
}
