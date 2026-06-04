package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func stringPtr(s string) *string {
	return &s
}

const (
	maxEmbedsPerMsg   = 10
	awolThresholdDays = 8
)

// loaCacheUnavailableFooter is the warning appended to /awol output (#96) when
// the LOA cache is unhealthy. Styled after loaUnavailableMessage; the On LOA
// column is rendered "unknown" rather than a misleading "false" in this state.
const loaCacheUnavailableFooter = "⚠️ LOA cache unavailable; On LOA column may be stale."

// loaCacheReader is the minimal LOA-cache surface /awol consumes. Production
// wires *utils.LOACache (GlobalLOACache); tests substitute a fake with canned
// entries and a forced health verdict so the handler stays deterministic
// without touching the process-global singleton. /awol reads each member's LOA
// state with a SINGLE GetEntry call and derives both the On LOA verdict
// (entry.IsActive) and the [[LOA]] link from that one snapshot (#158/S4), so the
// two can't disagree across a concurrent refresh. IsHealthy gates whether the
// per-member On LOA column can be trusted (#96): it is an age check on the last
// successful refresh, so an unhealthy cache is stale, not empty — GetEntry may
// still return (now possibly outdated) windows. When unhealthy, the render path
// gates the column to "unknown" rather than trusting those stale reads.
type loaCacheReader interface {
	GetEntry(username string) (utils.LOAEntry, bool)
	IsHealthy(maxAge time.Duration) (bool, time.Time)
}

type AwolUser struct {
	Username          string
	MilpacUrl         string
	TimeSinceLastPost string
	LastPostDate      time.Time
	OnLOA             bool
	// LOAEntry is the single cache snapshot read for this user (see the GetEntry
	// call in runAwol). HasLOAEntry records whether that read found a window, so
	// the [[LOA]] link can reuse the same snapshot instead of a second, possibly
	// inconsistent, cache lookup. OnLOA is derived from this snapshot's window.
	LOAEntry    utils.LOAEntry
	HasLOAEntry bool
}

func Awol() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "awol",
			Description: "Return the AWOL users for a position.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "position",
					Description: "Position to check for awols. (EX: 2/B/1-7 | Reservist | S1)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "force_file_output",
					Description: "Forces the output to be a file instead of an embed. This is useful for large AWOL lists.",
					Required:    false,
				},
			},
		},
		Handler: handleAwolCommand,
	}
}

func handleAwolCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runAwol(utils.NewSessionResponder(s), utils.GlobalLOACache, time.Now(), i)
}

func runAwol(r utils.InteractionResponder, cache loaCacheReader, now time.Time, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting AWOL check", "command", "Awol", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()
	forceFile := false
	if len(i.ApplicationCommandData().Options) > 1 {
		forceFile = i.ApplicationCommandData().Options[1].BoolValue()
	}

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AWOL data for %s...", position),
		},
	})
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	// #96: probe cache health once per invocation. IsHealthy is an age check on
	// the last successful refresh, so an unhealthy cache is stale, not empty —
	// per-user GetEntry reads may still return (possibly outdated) windows that
	// the On LOA column would otherwise present as current truth. We do NOT abort
	// — /awol's primary signal (lastForumPostDate) is independent — but the render
	// path below gates the column to "unknown" via cacheHealthy and warns. No
	// Sentry capture: operational degradation, not an internal error (ADR 0001).
	cacheHealthy, lastRefresh := cache.IsHealthy(loaCacheMaxAge)
	if !cacheHealthy {
		utils.Debug("AWOL served with unhealthy LOA cache",
			"command", "Awol",
			"username", i.Member.User.Username,
			"discord_id", i.Member.User.ID,
			"last_success", lastRefresh,
			"served_at", now,
			"staleness", now.Sub(lastRefresh),
		)
	}

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	if len(roster.LiteProfiles) == 0 {
		utils.HandleError(r, i, emptyRosterSearchMessage(position))
		return
	}

	awolThreshold := now.AddDate(0, 0, -awolThresholdDays)
	awolUsers := []AwolUser{}
	for _, member := range roster.LiteProfiles {
		if member.User.Username == "Tester.B" || strings.Contains(member.Rank.RankFull, "General") {
			continue
		}
		lastPostDate, err := time.Parse("2006-01-02 15:04:05", member.LastForumPostDate)
		if err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to parse last forum post date: %v", err))
			return
		}
		if lastPostDate.Before(awolThreshold) {
			matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
			if len(matches) < 2 {
				utils.HandleError(r, i, "❌ Failed to parse uniform URL")
				return
			}
			// #158/S4: read the LOA cache ONCE per user. Deriving OnLOA from this
			// same snapshot (rather than a separate active-window lookup) means the
			// On LOA verdict and the [[LOA]] link below can never disagree, even if a
			// 15-min refresh lands mid-loop now that ended windows are retained.
			entry, hasEntry := cache.GetEntry(member.User.Username)
			awolUsers = append(awolUsers, AwolUser{
				Username:          member.User.Username,
				MilpacUrl:         fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
				TimeSinceLastPost: utils.FormatTimeSinceDuration(lastPostDate),
				LastPostDate:      lastPostDate,
				OnLOA:             hasEntry && entry.IsActive(now),
				LOAEntry:          entry,
				HasLOAEntry:       hasEntry,
			})
		}
	}

	sort.Slice(awolUsers, func(i, j int) bool {
		return awolUsers[i].LastPostDate.Before(awolUsers[j].LastPostDate)
	})

	if len(awolUsers) == 0 {
		response := fmt.Sprintf("Search completed successfully: no users matching \"%s\" are AWOL.", position)
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		}); err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		}
		return
	}

	loaCount := 0
	if cacheHealthy {
		for _, u := range awolUsers {
			if u.OnLOA {
				loaCount++
			}
		}
	}

	var chunks []string
	currentChunk := ""
	for _, user := range awolUsers {
		// #96: only trust the cache lookup when it's healthy. When unhealthy,
		// mark every row "On LOA: unknown" and suppress the [LOA] decoration so
		// the column never silently reads all-clear.
		var loaTag string
		switch {
		case !cacheHealthy:
			loaTag = "On LOA: unknown — "
		case user.OnLOA:
			// Reuse the single per-user snapshot captured above — no second cache
			// read — so the link can't point at a thread the row's OnLOA disagrees with.
			if user.HasLOAEntry && user.LOAEntry.ThreadID != 0 {
				loaTag = fmt.Sprintf("**[[LOA]](https://7cav.us/threads/%d/)** ", user.LOAEntry.ThreadID)
			} else {
				loaTag = "**[LOA]** "
			}
		}
		userLine := fmt.Sprintf("%s[%s](%s) (%s)\n",
			loaTag,
			user.Username,
			user.MilpacUrl,
			user.TimeSinceLastPost)

		if len(currentChunk)+len(userLine) > 4096 {
			chunks = append(chunks, currentChunk)
			currentChunk = userLine
		} else {
			currentChunk += userLine
		}
	}
	if currentChunk != "" {
		chunks = append(chunks, currentChunk)
	}
	utils.Info("Debug chunks info", "chunks_length", len(chunks), "max_embeds", maxEmbedsPerMsg)
	if len(chunks) > maxEmbedsPerMsg {
		utils.Info("⚠️ Too many AWOL users for embeds, falling back to file upload", "count", len(awolUsers))
		sendAwolFile(r, i, awolUsers, position, forceFile, cacheHealthy, now)
		utils.Info("✨ Done!", "command", "Awol")
		return
	} else if forceFile {
		utils.Info("⚠️ Force file output enabled, falling back to embeds", "count", len(awolUsers))
		sendAwolFile(r, i, awolUsers, position, forceFile, cacheHealthy, now)
		utils.Info("✨ Done!", "command", "Awol")
		return
	}

	// #96: when the cache is unhealthy the loaCount is meaningless, so report
	// the warning instead of a concrete "(N on LOA)" tally.
	footerText := fmt.Sprintf("Total AWOL: %d (%d on LOA)", len(awolUsers), loaCount)
	if !cacheHealthy {
		footerText = fmt.Sprintf("Total AWOL: %d — %s", len(awolUsers), loaCacheUnavailableFooter)
	}

	var embeds []*discordgo.MessageEmbed
	for idx, chunk := range chunks {
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("AWOL Users for %s (Page %d/%d)", position, idx+1, len(chunks)),
			Description: chunk,
			Color:       0xfbcc29,
			Footer: &discordgo.MessageEmbedFooter{
				Text: footerText,
			},
			Timestamp: now.Format(time.RFC3339),
		}
		embeds = append(embeds, embed)
	}

	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: nil,
		Embeds:  &embeds,
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "Awol")
}

func sendAwolFile(r utils.InteractionResponder, i *discordgo.InteractionCreate, awolUsers []AwolUser, position string, forceFile, cacheHealthy bool, now time.Time) {
	var content strings.Builder
	_, _ = fmt.Fprintf(&content, "AWOL Report for %s\nGenerated: %s\n\n",
		position,
		now.Format("2006-01-02 15:04:05"))
	if !cacheHealthy {
		// #96: surface the cache-unavailable warning in the file too.
		_, _ = fmt.Fprintf(&content, "%s\n\n", loaCacheUnavailableFooter)
	}

	for _, user := range awolUsers {
		// #96: only trust the OnLOA snapshot when the cache is healthy; otherwise
		// the column is unknown, not all-clear.
		loaTag := ""
		switch {
		case !cacheHealthy:
			loaTag = " (On LOA: unknown)"
		case user.OnLOA:
			loaTag = " [LOA]"
		}
		_, _ = fmt.Fprintf(&content, "%s%s - %s\nMilpac: %s\n\n",
			user.Username,
			loaTag,
			user.TimeSinceLastPost,
			user.MilpacUrl)
	}

	file := &discordgo.File{
		Name:        fmt.Sprintf("awol_report_%s.txt", strings.ReplaceAll(position, "/", "-")),
		ContentType: "text/plain",
		Reader:      strings.NewReader(content.String()),
	}
	var prefix string
	if forceFile {
		prefix = fmt.Sprintf("Force File Set True, AWOL report for %s generated as file:", position)
	} else {
		prefix = fmt.Sprintf("Large AWOL report for %s generated as file:", position)
	}
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(prefix),
		Files:   []*discordgo.File{file},
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to send file: %v", err))
	}
}
