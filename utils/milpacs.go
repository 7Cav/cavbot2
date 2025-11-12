package utils

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

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
	KeycloakID        string     `json:"keycloakId"`
	DiscordID         string     `json:"discordId"`
	LastForumPostDate string     `json:"lastForumPostDate"`
}

type LiteRosterResponse struct {
	LiteProfiles map[string]LiteProfileResponse `json:"profiles"`
}

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
	KeycloakID        string     `json:"keycloakId"`
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

func makeAPIRequest[T any](ctx context.Context, path string, identifier string) (*T, error) {
	start := time.Now()
	client := resty.New()

	var result T
	response, err := client.R().
		SetContext(ctx).
		SetAuthToken(os.Getenv("BEARER")).
		SetResult(&result).
		Get(fmt.Sprintf("https://api.7cav.us/api/v1/%s", path))

	if response != nil {
		Info("API Call Finished", "duration", time.Since(start), "status", response.StatusCode(), "path", strings.Split(path, "/")[0], "identifier", identifier)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", strings.Split(path, "/")[0], err)
	}
	if response != nil && response.StatusCode() == 404 {
		return nil, fmt.Errorf("no %s found", strings.Split(path, "/")[0])
	}

	return &result, nil
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

func GetRosterByFuzzyPositionSearch(ctx context.Context, position string) (*LiteRosterResponse, error) {
	return makeAPIRequest[LiteRosterResponse](ctx,
		fmt.Sprintf("milpacs/position/search/%s", position),
		position)
}

func GetMilpacByKeycloakID(ctx context.Context, keycloakID string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpac/keycloak/%s", keycloakID),
		keycloakID)
}

func GetUserByGamertag(ctx context.Context, gamertag string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpac/gamertag/%s", gamertag),
		gamertag)
}
