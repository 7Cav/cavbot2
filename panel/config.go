// Package panel is cavbot2's web UI: an HTTP server inside the bot process,
// signed in through the forum's OAuth2, gated by a forum group allowlist on
// every request. This package holds sign-in, the panel session and the group
// check; the hub page and its service layer arrive with later tickets.
package panel

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the panel's resolved configuration. Every absolute URL the panel
// builds comes from BaseURL; it reads no proxy header.
type Config struct {
	// Addr is the listen address, such as ":8080".
	Addr string
	// BaseURL is the panel's public origin, such as https://cavbot2.7cav.us,
	// with no trailing slash. The OAuth redirect URI is BaseURL plus
	// /auth/callback and must match the forum's registered URI exactly.
	BaseURL string
	// ClientID and ClientSecret identify the panel to the forum as a
	// confidential client.
	ClientID     string
	ClientSecret string
	// AuthorizeURL, TokenURL and UserinfoURL are the forum's endpoints. Read
	// from the environment so a test can point them at a local stub.
	AuthorizeURL string
	TokenURL     string
	UserinfoURL  string
	// GroupIDs is the forum group allowlist. A user passes the group check
	// when their primary or any secondary group is in it.
	GroupIDs []int
}

// Environment variable names. Each is listed in .env.example and in the
// compose environment allowlist.
const (
	envAddr         = "PANEL_ADDR"
	envBaseURL      = "PANEL_BASE_URL"
	envClientID     = "PANEL_OAUTH_CLIENT_ID"
	envClientSecret = "PANEL_OAUTH_CLIENT_SECRET"
	envAuthorizeURL = "PANEL_OAUTH_AUTHORIZE_URL"
	envTokenURL     = "PANEL_OAUTH_TOKEN_URL"
	envUserinfoURL  = "PANEL_OAUTH_USERINFO_URL"
	envGroupIDs     = "PANEL_GROUP_IDS"
)

// defaultGroupIDs are the forum groups Genstaff (71), S6 HQ (47) and
// Regimental Technical Aides (44) hold, from the forum groups research
// branch. The allowlist lives in the environment so a forum change can never lock
// the maintainer out.
var defaultGroupIDs = []int{71, 47, 44}

// ConfigFromEnv reads the panel configuration. With PANEL_ADDR unset the
// panel is off and the second result is false. With it set, every other
// variable except PANEL_GROUP_IDS is required, and a missing one panics: a
// panel that listens but cannot sign anyone in is a misconfiguration worth
// failing on at startup, the same stance main takes on DISCORD_TOKEN.
func ConfigFromEnv() (Config, bool) {
	addr := os.Getenv(envAddr)
	if addr == "" {
		return Config{}, false
	}
	cfg := Config{
		Addr:         addr,
		BaseURL:      strings.TrimRight(requireEnv(envBaseURL), "/"),
		ClientID:     requireEnv(envClientID),
		ClientSecret: requireEnv(envClientSecret),
		AuthorizeURL: requireEnv(envAuthorizeURL),
		TokenURL:     requireEnv(envTokenURL),
		UserinfoURL:  requireEnv(envUserinfoURL),
		GroupIDs:     defaultGroupIDs,
	}
	if raw := os.Getenv(envGroupIDs); strings.TrimSpace(raw) != "" {
		cfg.GroupIDs = parseGroupIDs(raw)
	}
	return cfg, true
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		panic(fmt.Sprintf("%s is set but %s is not. Please set every PANEL_ variable, or unset %s to run without the panel", envAddr, name, envAddr))
	}
	return v
}

// parseGroupIDs reads a comma-separated list of forum group IDs, skipping
// blanks and anything that is not an integer, the way LOA_NODE_IDS is read.
func parseGroupIDs(raw string) []int {
	var ids []int
	for _, part := range strings.Split(raw, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
