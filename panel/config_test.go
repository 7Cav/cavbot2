package panel

import (
	"strings"
	"testing"
)

// clearPanelEnv empties every PANEL_* variable for the test, so the host's
// own environment cannot leak into a case.
func clearPanelEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PANEL_ADDR", "PANEL_BASE_URL", "PANEL_OAUTH_CLIENT_ID", "PANEL_OAUTH_CLIENT_SECRET",
		"PANEL_OAUTH_AUTHORIZE_URL", "PANEL_OAUTH_TOKEN_URL", "PANEL_OAUTH_USERINFO_URL", "PANEL_GROUP_IDS",
	} {
		t.Setenv(name, "")
	}
}

func TestConfigFromEnvDisabledWithoutAddr(t *testing.T) {
	clearPanelEnv(t)
	t.Setenv("PANEL_OAUTH_CLIENT_ID", "left-over")

	cfg, err := ConfigFromEnv()

	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Enabled() {
		t.Error("panel reports enabled with PANEL_ADDR unset")
	}
}

func TestConfigFromEnvNamesTheMissingVariable(t *testing.T) {
	clearPanelEnv(t)
	t.Setenv("PANEL_ADDR", ":8080")
	t.Setenv("PANEL_OAUTH_CLIENT_ID", "id")
	t.Setenv("PANEL_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("PANEL_OAUTH_AUTHORIZE_URL", "https://forum.test/oauth2/authorize")
	t.Setenv("PANEL_OAUTH_TOKEN_URL", "https://forum.test/api/oauth2/token")
	t.Setenv("PANEL_OAUTH_USERINFO_URL", "https://forum.test/api/me")

	_, err := ConfigFromEnv()

	if err == nil || !strings.Contains(err.Error(), "PANEL_BASE_URL") {
		t.Fatalf("ConfigFromEnv error = %v, want one naming PANEL_BASE_URL", err)
	}
}

func TestConfigFromEnvRefusesGroupIDsThatDoNotParse(t *testing.T) {
	clearPanelEnv(t)
	t.Setenv("PANEL_ADDR", ":8080")
	t.Setenv("PANEL_BASE_URL", "https://panel.test")
	t.Setenv("PANEL_OAUTH_CLIENT_ID", "id")
	t.Setenv("PANEL_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("PANEL_OAUTH_AUTHORIZE_URL", "https://forum.test/oauth2/authorize")
	t.Setenv("PANEL_OAUTH_TOKEN_URL", "https://forum.test/api/oauth2/token")
	t.Setenv("PANEL_OAUTH_USERINFO_URL", "https://forum.test/api/me")
	t.Setenv("PANEL_GROUP_IDS", "71, forty-seven")

	_, err := ConfigFromEnv()

	if err == nil || !strings.Contains(err.Error(), "PANEL_GROUP_IDS") {
		t.Fatalf("ConfigFromEnv error = %v, want one naming PANEL_GROUP_IDS", err)
	}
}
