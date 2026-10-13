package utils

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
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

// RanksResponse is the body of GET milpacs/ranks, every milpacs rank in
// display order. The list holds one entry that is not a rank, Tester, which
// the startup rank ladder check drops before it compares.
type RanksResponse struct {
	Ranks []Rank `json:"ranks"`
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

// defaultAPIOrigin is the 7Cav API's public address, which goes through
// Cloudflare.
const defaultAPIOrigin = "https://api.7cav.us"

var apiBaseURL = defaultAPIOrigin + "/api/v1"

// newAPIClient builds the client for one 7Cav API call. Every call gets a
// fresh one.
var newAPIClient = resty.New

// InitAPIBaseURL points every 7Cav API lookup at the origin CAV_API_BASE_URL
// holds, plus /api/v1, and logs the address in use. main calls it once at
// startup, after InitSentry. Unset or empty keeps the public address. So does
// a value that isn't an http or https origin, with a warning and a Sentry
// event: a typo in .env shouldn't leave members with a bot whose lookups all
// fail while the public address still serves them.
func InitAPIBaseURL() {
	origin := defaultAPIOrigin
	if raw := os.Getenv("CAV_API_BASE_URL"); raw != "" {
		if parsed, ok := parseAPIOrigin(raw); ok {
			origin = parsed
		} else {
			shown := redactPassword(raw)
			Warn("CAV_API_BASE_URL rejected, using the public address", "value", shown, "address", defaultAPIOrigin)
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetLevel(sentry.LevelWarning)
				setExtras(scope, []any{"value", shown})
				sentry.CaptureMessage("CAV_API_BASE_URL rejected")
			})
		}
	}
	apiBaseURL = origin + "/api/v1"
	Info("7Cav API address", "address", origin)
}

// redactPassword masks the password in raw's user info, as url.URL.Redacted
// does, without needing raw to parse. It takes the user info to run up to
// the last @, so a password holding a / or an @ is masked whole.
func redactPassword(raw string) string {
	prefix, rest := "", raw
	if i := strings.Index(raw, "://"); i >= 0 {
		prefix, rest = raw[:i+3], raw[i+3:]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return raw
	}
	user, _, hasPassword := strings.Cut(rest[:at], ":")
	if !hasPassword {
		return raw
	}
	return prefix + user + ":xxxxx" + rest[at:]
}

// parseAPIOrigin returns raw as scheme://host[:port] with any trailing slash
// dropped, or false when raw is anything else.
func parseAPIOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// ErrNotFound is the error a 7Cav API lookup wraps when the API answers 404:
// nothing matches what was asked for.
var ErrNotFound = errors.New("the 7Cav API answered 404")

func makeAPIRequest[T any](ctx context.Context, path string, identifier string) (*T, error) {
	start := time.Now()
	client := newAPIClient()

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
		return nil, fmt.Errorf("no %s found: %w", pathPrefix, ErrNotFound)
	}

	const maxBodyLog = 512
	body := string(response.Body())
	if len(body) > maxBodyLog {
		body = body[:maxBodyLog] + "...(truncated)"
	}
	Warn("API non-2xx response", "status", status, "path", pathPrefix, "identifier", identifier, "body", body)

	return nil, fmt.Errorf("%s API returned %d %s", pathPrefix, status, http.StatusText(status))
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

func GetUserByGamertag(ctx context.Context, gamertag string) (*ProfileResponse, error) {
	return makeAPIRequest[ProfileResponse](ctx,
		fmt.Sprintf("milpac/gamertag/%s", gamertag),
		gamertag)
}

// GetRanks fetches the milpacs rank list. The startup rank ladder check
// reads it once per process start.
func GetRanks(ctx context.Context) (*RanksResponse, error) {
	return makeAPIRequest[RanksResponse](ctx, "milpacs/ranks", "ranks")
}
