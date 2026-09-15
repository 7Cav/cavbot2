package panel_test

import (
	"slices"
	"testing"

	"github.com/7cav/cavbot2/panel"
)

// clearPanelEnv unsets every panel variable for the test.
func clearPanelEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PANEL_ADDR", "PANEL_BASE_URL", "PANEL_OAUTH_CLIENT_ID", "PANEL_OAUTH_CLIENT_SECRET",
		"PANEL_OAUTH_AUTHORIZE_URL", "PANEL_OAUTH_TOKEN_URL", "PANEL_OAUTH_USERINFO_URL", "PANEL_GROUP_IDS",
	} {
		t.Setenv(name, "")
	}
}

// setPanelEnv sets every required variable but the group list.
func setPanelEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PANEL_ADDR", ":8080")
	t.Setenv("PANEL_BASE_URL", "https://cavbot2.7cav.us")
	t.Setenv("PANEL_OAUTH_CLIENT_ID", "id")
	t.Setenv("PANEL_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("PANEL_OAUTH_AUTHORIZE_URL", "https://forum.test/oauth2/authorize")
	t.Setenv("PANEL_OAUTH_TOKEN_URL", "https://forum.test/api/oauth2/token")
	t.Setenv("PANEL_OAUTH_USERINFO_URL", "https://forum.test/api/me")
}

func TestConfigFromEnv_NoAddrMeansNoPanel(t *testing.T) {
	clearPanelEnv(t)
	if _, on := panel.ConfigFromEnv(); on {
		t.Fatalf("ConfigFromEnv reported the panel on with PANEL_ADDR unset")
	}
}

func TestConfigFromEnv_GroupIDs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []int
	}{
		{name: "default allowlist when unset", raw: "", want: []int{71, 47, 44}},
		{name: "parsed from the variable", raw: "1, 2,3", want: []int{1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearPanelEnv(t)
			setPanelEnv(t)
			t.Setenv("PANEL_GROUP_IDS", tc.raw)

			cfg, on := panel.ConfigFromEnv()

			if !on {
				t.Fatalf("ConfigFromEnv reported the panel off with PANEL_ADDR set")
			}
			if !slices.Equal(cfg.GroupIDs, tc.want) {
				t.Errorf("GroupIDs = %v, want %v", cfg.GroupIDs, tc.want)
			}
			if cfg.Addr != ":8080" || cfg.BaseURL != "https://cavbot2.7cav.us" {
				t.Errorf("Addr = %q BaseURL = %q", cfg.Addr, cfg.BaseURL)
			}
		})
	}
}
