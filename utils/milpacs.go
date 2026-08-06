package utils

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type ProfileResponse struct {
	User              User       `json:"user"`
	Gamertag          string     `json:"consoleGamertag"`
	Rank              Rank       `json:"rank"`
	RealName          string     `json:"realName"`
	UniformUrl        string     `json:"uniformUrl"`
	Roster            string     `json:"roster"`
	Primary           Position   `json:"primary"`
	Secondary         []Position `json:"secondaries"`
	Records           []Record   `json:"records"`
	Awards            []Award    `json:"awards"`
	Mos               string     `json:"mos"`
	JoinDate          string     `json:"joinDate"`
	PromotionDate     string     `json:"promotionDate"`
	DiscordID         string     `json:"discordId"`
	LastForumPostDate string     `json:"lastForumPostDate"`
}

type LiteRosterResponse struct {
	LiteProfiles map[string]LiteProfileResponse `json:"profiles"`
}

type LiteProfileResponse struct {
	User              User       `json:"user"`
	Gamertag          string     `json:"consoleGamertag"`
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
type Position struct {
	PositionTitle string `json:"positionTitle"`
	PositionID    string `json:"positionId"`
}

type Rank struct {
	RankShort    string `json:"rankShort"`
	RankFull     string `json:"rankFull"`
	RankImageUrl string `json:"rankImageUrl"`
	RankID       string `json:"rankId"`
}

type User struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
}

type Record struct {
	RecordDetails string `json:"recordDetails"`
	RecordType    string `json:"recordType"`
	RecordDate    string `json:"recordDate"`
	RecordUID     string `json:"recordUid"`
}

type Award struct {
	AwardDetails  string `json:"awardDetails"`
	AwardName     string `json:"awardName"`
	AwardDate     string `json:"awardDate"`
	AwardImageUrl string `json:"awardImageUrl"`
	AwardUID      string `json:"awardUid"`
}

func (r *ProfileResponse) GetRosterStatus() string {
	if status, exists := rosterMap[r.Roster]; exists {
		return status
	}
	Info("Roster status not found", "roster", r.Roster)
	return r.Roster
}

var rosterMap = map[string]string{
	"ROSTER_TYPE_COMBAT":        "Active Duty",
	"ROSTER_TYPE_RESERVE":       "Reserves",
	"ROSTER_TYPE_ELOA":          "Extended Leave of Absence",
	"ROSTER_TYPE_WALL_OF_HONOR": "Wall of Honor",
	"ROSTER_TYPE_ARLINGTON":     "Arlington National Cemetery",
	"ROSTER_TYPE_PAST_MEMBERS":  "Past Members",
}

var apiBaseURL = "https://api.7cav.us/api/v1"

func makeAPIRequest[T any](ctx context.Context, path string, identifier string) (*T, error) {
	start := time.Now()
	client := resty.New()

	var result T
	response, err := client.R().
		SetContext(ctx).
		SetAuthToken(os.Getenv("BEARER")).
		SetResult(&result).
		Get(fmt.Sprintf("%s/%s", apiBaseURL, path))

	if response != nil {
		Info("API Call Finished", "duration", time.Since(start), "status", response.StatusCode(), "path", strings.Split(path, "/")[0], "identifier", identifier)
	}

	pathPrefix := strings.Split(path, "/")[0]

	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", pathPrefix, err)
	}
	if response == nil {
		return nil, fmt.Errorf("failed to fetch %s: no response received", pathPrefix)
	}

	status := response.StatusCode()
	if status >= 200 && status < 300 {
		return &result, nil
	}

	if status == http.StatusNotFound {
		return nil, fmt.Errorf("no %s found", pathPrefix)
	}

	const maxBodyLog = 512
	body := string(response.Body())
	if len(body) > maxBodyLog {
		body = body[:maxBodyLog] + "...(truncated)"
	}
	Warn("API non-2xx response", "status", status, "path", pathPrefix, "identifier", identifier, "body", body)

	return nil, fmt.Errorf("%s API returned %d %s", pathPrefix, status, http.StatusText(status))
}

// RankInfo is one entry of the /milpacs/ranks reference list.
type RankInfo struct {
	RankShort        string `json:"rankShort"`
	RankFull         string `json:"rankFull"`
	RankDisplayOrder int    `json:"rankDisplayOrder"`
	RankID           string `json:"rankId"`
}

type RanksResponse struct {
	Ranks []RankInfo `json:"ranks"`
}

// GetRanks fetches the rank reference list (short/full names + display order).
func GetRanks(ctx context.Context) (*RanksResponse, error) {
	return makeAPIRequest[RanksResponse](ctx, "milpacs/ranks", "ranks")
}

func GetMilpacByUsername(ctx context.Context, username string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpacs/profile/username/%s", username),
		username)
}

func GetMilpacByDiscordID(ctx context.Context, discordID string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpac/discord/%s", discordID),
		discordID)
}

// GetLiteRoster fetches a whole roster by RosterType enum name (e.g.
// "ROSTER_TYPE_COMBAT" = Active Duty) with minimal per-profile data — same
// response shape as the fuzzy position search.
func GetLiteRoster(ctx context.Context, rosterType string) (*LiteRosterResponse, error) {
	return makeAPIRequest[LiteRosterResponse](ctx,
		fmt.Sprintf("roster/%s/lite", rosterType),
		rosterType)
}

func GetRosterByFuzzyPositionSearch(ctx context.Context, position string) (*LiteRosterResponse, error) {
	return makeAPIRequest[LiteRosterResponse](ctx,
		fmt.Sprintf("milpacs/position/search/%s", position),
		position)
}

func GetUserByGamertag(ctx context.Context, gamertag string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpac/gamertag/%s", gamertag),
		gamertag)
}
