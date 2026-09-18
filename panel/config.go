package panel

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is the panel's resolved configuration. Addr empty means the panel
// does not listen and the rest is ignored; that is how the feature stays inert
// on a host with no PANEL_* variables.
type Config struct {
	// Addr is the listen address, such as ":8080".
	Addr string
	// BaseURL is the public origin the panel is reached at, with no trailing
	// slash. Every absolute URL the panel emits, the OAuth redirect URI
	// included, is built from it. The panel reads no proxy header.
	BaseURL string

	ClientID     string
	ClientSecret string
	AuthorizeURL string
	TokenURL     string
	UserinfoURL  string

	// GroupIDs is the allowlist the group check reads: a forum user passes when
	// the primary group or any secondary group is in it.
	GroupIDs []int
}

// Enabled reports whether the panel listens at all.
func (c Config) Enabled() bool { return c.Addr != "" }

// defaultGroupIDs are Genstaff, S6 HQ and Regimental Technical Aides, the
// forum groups that may open the panel when PANEL_GROUP_IDS is unset.
var defaultGroupIDs = []int{71, 47, 44}

// ConfigFromEnv reads the PANEL_* variables. With PANEL_ADDR unset it returns a
// disabled config and no error, whatever the other variables hold. With it set,
// every other variable but PANEL_GROUP_IDS is required and the error names the
// first one missing, so a half-filled .env fails at startup instead of at the
// first sign-in.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Addr:         os.Getenv("PANEL_ADDR"),
		BaseURL:      strings.TrimRight(os.Getenv("PANEL_BASE_URL"), "/"),
		ClientID:     os.Getenv("PANEL_OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("PANEL_OAUTH_CLIENT_SECRET"),
		AuthorizeURL: os.Getenv("PANEL_OAUTH_AUTHORIZE_URL"),
		TokenURL:     os.Getenv("PANEL_OAUTH_TOKEN_URL"),
		UserinfoURL:  os.Getenv("PANEL_OAUTH_USERINFO_URL"),
		GroupIDs:     defaultGroupIDs,
	}
	if !cfg.Enabled() {
		return cfg, nil
	}
	required := []struct{ name, value string }{
		{"PANEL_BASE_URL", cfg.BaseURL},
		{"PANEL_OAUTH_CLIENT_ID", cfg.ClientID},
		{"PANEL_OAUTH_CLIENT_SECRET", cfg.ClientSecret},
		{"PANEL_OAUTH_AUTHORIZE_URL", cfg.AuthorizeURL},
		{"PANEL_OAUTH_TOKEN_URL", cfg.TokenURL},
		{"PANEL_OAUTH_USERINFO_URL", cfg.UserinfoURL},
	}
	for _, r := range required {
		if r.value == "" {
			return Config{}, fmt.Errorf("PANEL_ADDR is set but %s is empty", r.name)
		}
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return Config{}, fmt.Errorf("PANEL_BASE_URL: %w", err)
	}
	if raw := os.Getenv("PANEL_GROUP_IDS"); raw != "" {
		ids, err := parseGroupIDs(raw)
		if err != nil {
			return Config{}, fmt.Errorf("PANEL_GROUP_IDS: %w", err)
		}
		cfg.GroupIDs = ids
	}
	return cfg, nil
}

// parseGroupIDs reads a comma-separated list of forum group IDs, the
// LOA_NODE_IDS shape. Unlike that reader it refuses a value it cannot parse:
// a typo in the allowlist would otherwise lock every user out in silence.
func parseGroupIDs(raw string) ([]int, error) {
	var ids []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not a group ID", part)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no group IDs in %q", raw)
	}
	return ids, nil
}
