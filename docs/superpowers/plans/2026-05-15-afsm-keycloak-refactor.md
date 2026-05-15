# AFSM Keycloak Refactor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Re-route `/afsm` and `/s6-it-check` milpac lookups off the removed Keycloak service to forum username, and convert per-member errors from abort-the-report to skip-the-member-and-Sentry-report.

**Architecture:** Extract a per-member evaluation helper for each handler (`evaluateAFSMMember`, `evaluateS6Member`). The handler loop becomes a thin caller that handles only the skip/keep decision and skipped-count accounting. Existing `CaptureError`/`HandleError` plumbing stays; one `CaptureError` site per command replaces the multiple inner sites. Dead code from the Keycloak era (`GetMilpacByKeycloakID` + `KeycloakID` struct fields) is deleted.

**Tech Stack:** Go 1.26, `bwmarrin/discordgo`, `utils.GetMilpacByUsername` (existing wrapper around `api.7cav.us`), `utils.CaptureError` (Sentry), `utils.ExtractMilpacIDFromUniformURL` (existing regex helper at `utils/milpac_url.go`).

**Spec:** `docs/superpowers/specs/2026-05-15-afsm-keycloak-refactor-design.md`

**Branch:** `fix/afsm-keycloak-refactor` (already created from `develop`, spec already committed)

---

## File structure

| File | Action |
|---|---|
| `commands/afsm.go` | Modify — extract `evaluateAFSMMember` helper, rewrite handler loop, add footer, drop `regexp` import |
| `commands/s6_trackers.go` | Modify — extract `evaluateS6Member` helper, rewrite handler loop, add footer, drop `regexp` import |
| `utils/milpacs.go` | Modify — delete `GetMilpacByKeycloakID`, delete `KeycloakID` fields from `ProfileResponse` and `LiteProfileResponse` |

No new files. No test files in this PR (per spec non-goal — tests blocked on the runX conversion tracked under #76).

---

## Task 1: Refactor `/afsm` handler

**Files:**
- Modify: `commands/afsm.go` (whole `handleAFSMCommand` body + new `evaluateAFSMMember` helper + import list)

- [ ] **Step 1: Replace the imports block**

Current imports (top of `commands/afsm.go`):

```go
import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"sort"
	"strings"
	"time"
)
```

Replace with (drop `"regexp"` — `evaluateAFSMMember` calls `utils.ExtractMilpacIDFromUniformURL` instead of using the regex directly):

```go
import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"sort"
	"strings"
	"time"
)
```

- [ ] **Step 2: Replace `handleAFSMCommand` (lines 51-246) with the slim version**

The current function body (everything between `func handleAFSMCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {` and its closing `}`) gets fully replaced with:

```go
func handleAFSMCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🎯 AFSM Check requested", "command", "AFSM", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	choice := i.ApplicationCommandData().Options[0].StringValue()
	utils.Debug("🔍 Processing department choice", "department", choice)

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AFSM data for %s...", choice),
		},
	})
	if err != nil {
		utils.CaptureError("❌ Interaction response failed", err)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	utils.Debug("📊 Fetching roster data", "department", choice)
	Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, choice)
	if err != nil {
		utils.CaptureError("❌ Roster fetch failed", err)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch Members: %v", err))
		return
	}
	utils.Info("📋 Retrieved roster", "member_count", len(Members.LiteProfiles))

	if len(Members.LiteProfiles) == 0 {
		utils.CaptureError(
			"AFSM roster lookup returned zero members",
			fmt.Errorf("empty roster for department %q", choice),
			"department", choice,
		)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("⚠️ The %s roster came back empty — this shouldn't happen for a preset department. The issue has been reported.", choice))
		return
	}

	currentDate := time.Now()
	utils.Debug("⏰ Current date set", "date", currentDate)

	eligibleMembers := []AFSMMember{}
	skippedCount := 0
	for _, member := range Members.LiteProfiles {
		result, err := evaluateAFSMMember(ctx, member, choice, currentDate)
		if err != nil {
			utils.CaptureError(
				"AFSM member evaluation failed",
				err,
				"username", member.User.Username,
				"department", choice,
			)
			skippedCount++
			continue
		}
		if result != nil {
			eligibleMembers = append(eligibleMembers, *result)
		}
	}

	utils.Debug("📊 Sorting eligible members", "count", len(eligibleMembers))
	sort.Slice(eligibleMembers, func(i, j int) bool {
		return eligibleMembers[i].Date.Before(eligibleMembers[j].Date)
	})

	AFSMUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		utils.Debug("📝 Formatting output for user", "username", user.Username, "time_since", user.TimeSince)
		AFSMUserOutput = append(AFSMUserOutput, fmt.Sprintf("[%s](<%s>) (%s)", user.Username, user.MilpacUrl, user.TimeSince))
	}

	var response string
	if len(AFSMUserOutput) > 0 {
		response = fmt.Sprintf("⚠️ This command cannot be made completely accurate. Please check the output carefully.\nThe following %s members are eligible for AFSM:\n%s", choice, strings.Join(AFSMUserOutput, "\n"))
		utils.Info("✅ Found eligible members", "department", choice, "count", len(AFSMUserOutput))
	} else {
		response = fmt.Sprintf("No %s members found eligible for AFSM", choice)
		utils.Info("📭 No eligible members found", "department", choice)
	}

	if skippedCount > 0 {
		noun := "members"
		if skippedCount == 1 {
			noun = "member"
		}
		response += fmt.Sprintf("\n⚠️ %d %s skipped due to errors (reported)", skippedCount, noun)
		utils.Info("⚠️ Members skipped during AFSM evaluation", "department", choice, "count", skippedCount)
	}

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.CaptureError("❌ Response edit failed", err)
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Command completed successfully", "command", "AFSM", "department", choice)
}
```

- [ ] **Step 3: Add `evaluateAFSMMember` helper above `determineRecordType`**

Insert immediately after the closing `}` of `handleAFSMCommand` and before `func determineRecordType(...)`:

```go
// evaluateAFSMMember returns (member, nil) when the roster entry is eligible
// for the AFSM, (nil, nil) when it is not, and (nil, err) when the entry's
// data could not be processed and should be skipped + reported.
func evaluateAFSMMember(
	ctx context.Context,
	member utils.LiteProfileResponse,
	dept string,
	currentDate time.Time,
) (*AFSMMember, error) {
	utils.Debug("👤 Processing member", "username", member.User.Username)

	eligible := false
	for _, secondary := range member.Secondary {
		utils.Debug("🔍 Checking secondary position", "position", secondary.PositionTitle, "department", dept)
		if strings.Contains(secondary.PositionTitle, dept) {
			eligible = true
			utils.Debug("✅ Member eligible through secondary", "position", secondary.PositionTitle)
		}
		if strings.Contains(member.Primary.PositionTitle, dept) {
			eligible = false
			utils.Debug("❌ Member ineligible due to primary position", "position", member.Primary.PositionTitle)
		}
	}
	if !eligible {
		return nil, nil
	}

	utils.Info("🎯 Found eligible member", "username", member.User.Username)
	fullProfile, err := utils.GetMilpacByUsername(ctx, member.User.Username)
	if err != nil {
		return nil, fmt.Errorf("milpac fetch failed: %w", err)
	}

	utils.Debug("📝 Checking assignments", "username", member.User.Username)
	var assignments []map[string]interface{}
	for _, record := range fullProfile.Records {
		if record.RecordType != "RECORD_TYPE_ASSIGNMENT" &&
			record.RecordType != "RECORD_TYPE_TRANSFER" &&
			record.RecordType != "RECORD_TYPE_ELOA" &&
			record.RecordType != "RECORD_TYPE_DISCHARGE" {
			continue
		}
		utils.Debug("📋 Processing record", "type", record.RecordType, "date", record.RecordDate)
		recordDate, err := time.Parse("2006-01-02", record.RecordDate)
		if err != nil {
			return nil, fmt.Errorf("record date parse failed for %q: %w", record.RecordDate, err)
		}
		if strings.Contains(record.RecordDetails, dept) ||
			strings.Contains(record.RecordDetails, "ELOA") ||
			strings.Contains(record.RecordDetails, "Discharge") ||
			strings.Contains(record.RecordDetails, "Retired") {
			event := map[string]interface{}{
				"record_date":    recordDate,
				"record_type":    determineRecordType(record.RecordDetails),
				"record_details": record.RecordDetails,
			}
			assignments = append(assignments, event)
			utils.Debug("✍️ Added assignment record", "type", event["record_type"], "date", recordDate)
		}
	}

	sort.Slice(assignments, func(i, j int) bool {
		return assignments[i]["record_date"].(time.Time).Before(assignments[j]["record_date"].(time.Time))
	})
	utils.Debug("📊 Sorted assignments", "count", len(assignments))

	var startDate time.Time
	for _, assignment := range assignments {
		if assignment["record_type"].(string) == "join" {
			if startDate.IsZero() {
				startDate = assignment["record_date"].(time.Time)
				utils.Debug("📅 Set start date", "date", startDate)
			}
		} else {
			startDate = time.Time{}
			utils.Debug("🔄 Reset start date due to interruption")
		}
	}

	if startDate.IsZero() || !startDate.Before(currentDate.AddDate(-1, 0, 0)) {
		utils.Debug("⏳ Member has not served long enough", "username", member.User.Username)
		return nil, nil
	}

	utils.Debug("✅ Member meets time requirement, checking awards", "username", member.User.Username)
	var latestAward time.Time
	for _, award := range fullProfile.Awards {
		if award.AwardName != "Armed Forces Service Medal" || !strings.Contains(award.AwardDetails, dept) {
			continue
		}
		utils.Debug("🎖️ Found AFSM award", "date", award.AwardDate, "details", award.AwardDetails)
		awardDate, err := time.Parse("2006-01-02", award.AwardDate)
		if err != nil {
			return nil, fmt.Errorf("award date parse failed for %q: %w", award.AwardDate, err)
		}
		if latestAward.IsZero() || awardDate.After(latestAward) {
			latestAward = awardDate
			utils.Debug("📅 Updated latest award date", "date", latestAward)
		}
	}

	if !latestAward.IsZero() && !latestAward.Before(currentDate.AddDate(-1, 0, 0)) {
		utils.Debug("⏳ Member not yet eligible since last award", "username", member.User.Username, "last_award", latestAward)
		return nil, nil
	}

	milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
	if err != nil {
		return nil, fmt.Errorf("uniform URL parse failed: %w", err)
	}

	utils.Info("✨ Member eligible", "username", member.User.Username, "start_date", startDate)
	refDate := startDate
	if !latestAward.IsZero() {
		refDate = latestAward
	}
	return &AFSMMember{
		Username:  member.User.Username,
		MilpacUrl: fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID),
		TimeSince: utils.FormatTimeSinceDuration(refDate),
		Date:      refDate,
	}, nil
}
```

- [ ] **Step 4: Verify `commands/afsm.go` compiles**

Run: `go build -o /tmp/cavbot2-build .`
Expected: exit 0, no output. Remove the binary: `rm -f /tmp/cavbot2-build`.

If the build fails on `utils.ExtractMilpacIDFromUniformURL` not being found, double-check `utils/milpac_url.go` exists (it should — added in PR #74).

- [ ] **Step 5: Verify `go vet ./...` is clean**

Run: `go vet ./...`
Expected: exit 0, no output.

- [ ] **Step 6: Verify existing tests still pass**

Run: `go test ./...`
Expected: all packages PASS. No new tests were added in this task; existing utils and command tests must still pass.

- [ ] **Step 7: Verify lint is clean**

Run: `golangci-lint run --timeout=5m`
Expected: exit 0, no output. The `regexp` import is gone so no unused-import warning. The removed inline regex no longer triggers `gocyclo` thresholds (if any) at the previous deeper-nested function.

- [ ] **Step 8: Commit**

```bash
git add commands/afsm.go
git commit -m "$(cat <<'EOF'
fix(commands): re-route /afsm milpac lookup to username, skip-on-error

Per-member errors (milpac fetch, record/award date parse, uniform URL
parse) no longer abort the whole report — they skip the affected
member, capture to Sentry, and increment a skipped-count footer.
Milpac lookup switches from the removed Keycloak service to
GetMilpacByUsername using the forum username.

Member-evaluation logic is extracted into evaluateAFSMMember so the
handler loop is a thin caller.

Closes part of #79.
EOF
)"
```

---

## Task 2: Refactor `/s6-it-check` handler

**Files:**
- Modify: `commands/s6_trackers.go` (whole `handleS6ITCheckCommand` body + new `evaluateS6Member` helper + import list)

- [ ] **Step 1: Replace the imports block**

Current imports (top of `commands/s6_trackers.go`):

```go
import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"sort"
	"strings"
	"time"
)
```

Replace with (drop `"regexp"` — `evaluateS6Member` calls `utils.ExtractMilpacIDFromUniformURL` instead):

```go
import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"sort"
	"strings"
	"time"
)
```

- [ ] **Step 2: Replace `handleS6ITCheckCommand` (lines 32-142) with the slim version**

```go
func handleS6ITCheckCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting S6 IT Check", "command", "S6ITCheck", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Fetching S6 IT Check data...",
		},
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s6Members, err := utils.GetRosterByFuzzyPositionSearch(ctx, "S6")
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch S6 Members: %v", err))
		return
	}

	if len(s6Members.LiteProfiles) == 0 {
		utils.CaptureError(
			"S6 IT roster lookup returned zero members",
			fmt.Errorf("empty roster for S6 fuzzy search"),
		)
		utils.HandleError(utils.NewSessionResponder(s), i, "⚠️ The S6 roster came back empty — this shouldn't happen. The issue has been reported.")
		return
	}

	currentDate := time.Now()
	eligibleMembers := []ITMember{}
	skippedCount := 0
	for _, member := range s6Members.LiteProfiles {
		rows, err := evaluateS6Member(ctx, member, currentDate)
		if err != nil {
			utils.CaptureError(
				"S6 IT member evaluation failed",
				err,
				"username", member.User.Username,
			)
			skippedCount++
			continue
		}
		eligibleMembers = append(eligibleMembers, rows...)
	}

	sort.Slice(eligibleMembers, func(i, j int) bool {
		if eligibleMembers[i].PositionDate.IsZero() {
			return false
		}
		if eligibleMembers[j].PositionDate.IsZero() {
			return true
		}
		return eligibleMembers[i].PositionDate.Before(eligibleMembers[j].PositionDate)
	})

	ITUserOutput := make([]string, 0)
	for _, user := range eligibleMembers {
		ITUserOutput = append(ITUserOutput, fmt.Sprintf("[%s](<%s>) (%s) - %s", user.Username, user.MilpacUrl, user.Position, user.TimeSince))
	}
	response := fmt.Sprintf("The following S6 members are eligible for Full Status:\n%s", strings.Join(ITUserOutput, "\n"))

	if skippedCount > 0 {
		noun := "members"
		if skippedCount == 1 {
			noun = "member"
		}
		response += fmt.Sprintf("\n⚠️ %d %s skipped due to errors (reported)", skippedCount, noun)
		utils.Info("⚠️ Members skipped during S6 IT evaluation", "count", skippedCount)
	}

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "S6ITCheck")
}
```

Note the stray `var matches []string` declaration that existed above the old loop (around line 66) is gone — it was a leftover from the inline-regex pattern that the helper no longer needs.

- [ ] **Step 3: Add `evaluateS6Member` helper above `normalizePositionWords`**

Insert immediately after the closing `}` of `handleS6ITCheckCommand` and before `func normalizePositionWords(...)`:

```go
// evaluateS6Member returns the zero-to-N ITMember rows produced by a single
// roster entry (each matching IT/S6 position yields its own row, preserving
// the previous primary-then-secondary behavior). Returns an error if any
// internal step fails — caller should skip the member and report to Sentry.
func evaluateS6Member(
	ctx context.Context,
	member utils.LiteProfileResponse,
	currentDate time.Time,
) ([]ITMember, error) {
	fullProfile, err := utils.GetMilpacByUsername(ctx, member.User.Username)
	if err != nil {
		return nil, fmt.Errorf("milpac fetch failed: %w", err)
	}

	var rows []ITMember

	appendIfMatch := func(positionTitle string) error {
		position, timeInPosition, positionDate, err := determineITPositionTime(positionTitle, fullProfile)
		if err != nil {
			return fmt.Errorf("position time computation failed for %q: %w", positionTitle, err)
		}
		milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
		if err != nil {
			return fmt.Errorf("uniform URL parse failed: %w", err)
		}
		milpacUrl := fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID)
		if positionDate.IsZero() {
			rows = append(rows, ITMember{
				Username:     member.User.Username,
				MilpacUrl:    milpacUrl,
				Position:     position,
				TimeSince:    "⚠️ No matching assignment record found",
				PositionDate: positionDate,
			})
		} else if positionDate.Before(currentDate.AddDate(0, -6, 0)) {
			rows = append(rows, ITMember{
				Username:     member.User.Username,
				MilpacUrl:    milpacUrl,
				Position:     position,
				TimeSince:    timeInPosition,
				PositionDate: positionDate,
			})
		}
		return nil
	}

	if strings.Contains(fullProfile.Primary.PositionTitle, "IT") && strings.Contains(fullProfile.Primary.PositionTitle, "S6") {
		if err := appendIfMatch(fullProfile.Primary.PositionTitle); err != nil {
			return nil, err
		}
	}

	for _, secondary := range fullProfile.Secondary {
		if strings.Contains(secondary.PositionTitle, "IT") && strings.Contains(secondary.PositionTitle, "S6") {
			if err := appendIfMatch(secondary.PositionTitle); err != nil {
				return nil, err
			}
		}
	}

	return rows, nil
}
```

The uniform-URL parse stays *inside* `appendIfMatch` (not hoisted to the top of `evaluateS6Member`) on purpose: in the original code, a member who matched zero positions never had their uniform URL parsed, so a malformed URL there was invisible. Keeping the parse per-match preserves that exact behavior. Yes, this re-parses the same URL once per matching position — that's faithful to the original.

- [ ] **Step 4: Verify `commands/s6_trackers.go` compiles**

Run: `go build -o /tmp/cavbot2-build .`
Expected: exit 0, no output. Remove the binary: `rm -f /tmp/cavbot2-build`.

- [ ] **Step 5: Verify `go vet ./...` is clean**

Run: `go vet ./...`
Expected: exit 0, no output.

- [ ] **Step 6: Verify existing tests still pass**

Run: `go test ./...`
Expected: all packages PASS.

- [ ] **Step 7: Verify lint is clean**

Run: `golangci-lint run --timeout=5m`
Expected: exit 0, no output.

- [ ] **Step 8: Commit**

```bash
git add commands/s6_trackers.go
git commit -m "$(cat <<'EOF'
fix(commands): re-route /s6-it-check milpac lookup to username, skip-on-error

Same shape as the /afsm fix: per-member errors no longer abort the
whole report. Milpac lookup switches from the removed Keycloak
service to GetMilpacByUsername using the forum username.
Member-evaluation logic is extracted into evaluateS6Member so the
handler loop is a thin caller.

Closes part of #79.
EOF
)"
```

---

## Task 3: Delete dead Keycloak code in `utils/milpacs.go`

After Tasks 1 and 2 land, both call sites of `GetMilpacByKeycloakID` are gone and neither handler references `KeycloakID`. Time to delete the dead code.

**Files:**
- Modify: `utils/milpacs.go`

- [ ] **Step 1: Confirm no remaining references**

Run: `grep -rn "KeycloakID\|GetMilpacByKeycloakID\|keycloakId\|/milpac/keycloak/" /home/syniron/repos/cavbot2/ --include="*.go"`
Expected: only matches inside `utils/milpacs.go` itself (the struct fields at lines 27 and 47, and the function at lines 165-169). If anything else turns up, stop and investigate — Tasks 1 or 2 may not be complete.

- [ ] **Step 2: Remove `KeycloakID` from `ProfileResponse`**

In `utils/milpacs.go`, find the `ProfileResponse` struct (around line 14-30). Delete this single line:

```go
	KeycloakID        string     `json:"keycloakId"`
```

The struct after deletion:

```go
type ProfileResponse struct {
	User              User       `json:"user"`
	Gamertag          string     `json:"gamertag"`
	Rank              Rank       `json:"rank"`
	RealName          string     `json:"realName"`
	UniformUrl        string     `json:"uniformUrl"`
	Roster            string     `json:"roster"`
	Primary           Position   `json:"primary"`
	Secondary         []Position `json:"secondaries"`
	Records           []Record   `json:"records"`
	Awards            []Award    `json:"awards"`
	JoinDate          string     `json:"joinDate"`
	PromotionDate     string     `json:"promotionDate"`
	DiscordID         string     `json:"discordId"`
	LastForumPostDate string     `json:"lastForumPostDate"`
}
```

- [ ] **Step 3: Remove `KeycloakID` from `LiteProfileResponse`**

Find the `LiteProfileResponse` struct (around line 36-52). Delete this single line:

```go
	KeycloakID        string     `json:"keycloakId"`
```

The struct after deletion:

```go
type LiteProfileResponse struct {
	User              User       `json:"user"`
	Gamertag          string     `json:"gamertag"`
	Rank              Rank       `json:"rank"`
	RealName          string     `json:"realName"`
	UniformUrl        string     `json:"uniformUrl"`
	Roster            string     `json:"roster"`
	Primary           Position   `json:"primary"`
	Secondary         []Position `json:"secondaries"`
	JoinDate          string     `json:"joinDate"`
	PromotionDate     string     `json:"promotionDate"`
	DiscordID         string     `json:"discordId"`
	AwardDate         string     `json:"awardDate"`
	RecordDate        string     `json:"recordDate"`
	LastForumPostDate string     `json:"lastForumPostDate"`
}
```

(Go's JSON unmarshaller silently ignores unknown fields, so the API continuing to send `keycloakId` is harmless.)

- [ ] **Step 4: Remove `GetMilpacByKeycloakID`**

Find and delete the entire function block at the bottom of the file (currently lines 165-169):

```go
func GetMilpacByKeycloakID(ctx context.Context, keycloakID string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpac/keycloak/%s", keycloakID),
		keycloakID)
}
```

- [ ] **Step 5: Verify compile**

Run: `go build -o /tmp/cavbot2-build .`
Expected: exit 0, no output. `rm -f /tmp/cavbot2-build`.

- [ ] **Step 6: Verify `go vet ./...` is clean**

Run: `go vet ./...`
Expected: exit 0, no output.

- [ ] **Step 7: Verify existing tests still pass**

Run: `go test ./...`
Expected: all packages PASS. The `utils` package's `makeAPIRequest` tests (`utils/milpacs_test.go`) do not reference `GetMilpacByKeycloakID` — verify with `grep -n "GetMilpacByKeycloakID" /home/syniron/repos/cavbot2/utils/*_test.go` (expected: no matches). If any test does reference it, stop and adjust before deleting.

- [ ] **Step 8: Verify lint is clean and coverage floors hold**

Run: `golangci-lint run --timeout=5m`
Expected: exit 0, no output.

Coverage check (matches CI): run from the repo root:

```bash
go test -cover ./... 2>&1 | bash .github/scripts/check-coverage-floors.sh
```

Expected: exit 0. Removing untested code (`GetMilpacByKeycloakID`) reduces the `utils` package's total statement count and removes only uncovered statements, so coverage percentage goes **up**, not down. If the floor check fails, something else is wrong.

- [ ] **Step 9: Commit**

```bash
git add utils/milpacs.go
git commit -m "$(cat <<'EOF'
chore(utils): remove dead Keycloak milpac lookup + struct fields

GetMilpacByKeycloakID and the KeycloakID fields on ProfileResponse /
LiteProfileResponse are no longer referenced after the /afsm and
/s6-it-check refactors. Delete them. Go's JSON unmarshaller silently
ignores the keycloakId field the API still returns.

Closes #79.
EOF
)"
```

---

## Task 4: Final whole-branch verification

After Tasks 1–3 are committed, run the full CI-equivalent suite end-to-end against the branch.

- [ ] **Step 1: Confirm three commits on the branch on top of develop**

Run: `git log --oneline develop..HEAD`
Expected: four lines — the spec commit (`85d268a`-ish), then three new commits matching the messages above. (Spec wording fix and plan commit may appear depending on when this task runs.)

- [ ] **Step 2: Full build**

Run: `go build -o /tmp/cavbot2-build .`
Expected: exit 0. `rm -f /tmp/cavbot2-build`.

- [ ] **Step 3: Full vet**

Run: `go vet ./...`
Expected: exit 0, no output.

- [ ] **Step 4: Full test + coverage check**

Run: `go test -cover ./... 2>&1 | tee /tmp/cov.txt | bash .github/scripts/check-coverage-floors.sh`
Expected: all PASS, coverage script exits 0. Floors are `utils=20%` / `commands=1%`; both should comfortably hold.

- [ ] **Step 5: Full lint**

Run: `golangci-lint run --timeout=5m`
Expected: exit 0, no output.

- [ ] **Step 6: Final grep for `Keycloak`**

Run: `grep -rn "Keycloak\|keycloak" /home/syniron/repos/cavbot2/ --include="*.go"`
Expected: no matches. If anything turns up, audit and clean up.

- [ ] **Step 7: Push and open PR**

```bash
git push -u origin fix/afsm-keycloak-refactor
gh pr create --title "fix: re-route /afsm + /s6-it-check off removed Keycloak service, stop nuking reports on per-member errors" --body "$(cat <<'EOF'
## Summary
- Milpac lookup at the two remaining Keycloak-ID call sites (`/afsm`, `/s6-it-check`) re-routed to `GetMilpacByUsername`. Newer Cav members (no `KeycloakID` populated) no longer 404 the lookup.
- Per-member errors (milpac fetch, record/award date parse, uniform URL parse) now skip the affected member and increment a `⚠️ N members skipped due to errors (reported)` footer instead of aborting the whole report.
- Member-evaluation logic extracted into `evaluateAFSMMember` / `evaluateS6Member` so the handler loops are thin callers.
- `GetMilpacByKeycloakID` and the `KeycloakID` struct fields on `ProfileResponse` / `LiteProfileResponse` are deleted.

Closes #79. Resolves Sentry CAVBOT2-4.

## Test plan
- [ ] Test guild: `/afsm S6` returns the eligibles list for a roster that includes at least one member without a populated `KeycloakID` (pre-fix this aborted with `❌ Failed to fetch milpac: no milpac found`).
- [ ] Test guild: `/s6-it-check` returns the eligibles list under the same conditions.
- [ ] CI green: build, vet, lint, tests, coverage floor.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR created, returns URL.

---

## Self-review notes (for the implementer)

- **Behavioral equivalence:** The AFSM eligibility ladder (secondary-contains-dept AND primary-doesn't-contain-dept → milpac fetch → record parse → ≥1yr served → ≥1yr since last AFSM award → render) is preserved exactly; the only change is `KeycloakID → User.Username` and `return → return err / continue`. The S6 ladder (IT-and-S6 in primary or secondary → milpac fetch → position-time computation → render) is likewise preserved.
- **One regression to watch:** the AFSM `member.Secondary` loop in the original code re-runs the "primary contains dept → reset eligible to false" check on every secondary iteration, which means a member with a primary like `S6 IT Specialist` matching department `S6` is correctly excluded only if at least one secondary is iterated. If `member.Secondary` is empty, the primary-exclusion never fires. The refactor preserves this exactly (same loop structure). Don't try to "fix" it in this PR — it's a separate question.
- **Determinism:** `Members.LiteProfiles` is a `map`, so iteration order is non-deterministic. The original code already lived with this; the refactor doesn't change ordering semantics. Final `sort.Slice` on `eligibleMembers` keeps output stable.
- **Sentry consolidation:** as noted in the spec, all per-member errors fingerprint under a single Sentry issue per command now. This is intentional. The underlying error message is preserved through `%w` wrapping and remains visible in Sentry's event detail view.
