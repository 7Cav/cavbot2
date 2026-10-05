package panel

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
)

// clearPanelEnv empties every PANEL_* variable for the test, so the host's
// own environment cannot leak into a case.
func clearPanelEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PANEL_ADDR", "PANEL_BASE_URL", "PANEL_OAUTH_CLIENT_ID", "PANEL_OAUTH_CLIENT_SECRET",
		"PANEL_OAUTH_AUTHORIZE_URL", "PANEL_OAUTH_TOKEN_URL", "PANEL_OAUTH_USERINFO_URL", "PANEL_GROUP_IDS",
		"FOXHOLE_GROUP_ID",
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

// setPanelEnv sets the panel's variables to a panel signed in through the
// fake forum, every optional one left unset.
func setPanelEnv(t *testing.T, f *fakeForum) {
	t.Helper()
	clearPanelEnv(t)
	t.Setenv("PANEL_ADDR", ":0")
	t.Setenv("PANEL_BASE_URL", testBaseURL)
	t.Setenv("PANEL_OAUTH_CLIENT_ID", testClientID)
	t.Setenv("PANEL_OAUTH_CLIENT_SECRET", testClientSecret)
	t.Setenv("PANEL_OAUTH_AUTHORIZE_URL", f.srv.URL+"/oauth2/authorize")
	t.Setenv("PANEL_OAUTH_TOKEN_URL", f.srv.URL+"/api/oauth2/token")
	t.Setenv("PANEL_OAUTH_USERINFO_URL", f.srv.URL+"/api/me")
}

// unsetEnv unsets a variable for the test and restores it after.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
}

// worldFromEnv builds the panel from the configuration ConfigFromEnv reads,
// over the fake forum.
func worldFromEnv(t *testing.T, f *fakeForum) *testWorld {
	t.Helper()
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	return newTestWorldConfigured(t, store.NewFake(), f, cfg)
}

// FOXHOLE_GROUP_ID unset or empty leaves the Foxhole group at 323, the
// forum's Foxhole group. The production host never receives the repo's
// compose file, so this default is what it runs on, and 323 is spelled by
// hand here on purpose: a user in it lands on the Foxhole page.
func TestFoxholeGroupUnsetOrEmptyIs323(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T)
	}{
		{"unset", func(t *testing.T) { unsetEnv(t, "FOXHOLE_GROUP_ID") }},
		{"empty", func(t *testing.T) { t.Setenv("FOXHOLE_GROUP_ID", "") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeForum(t)
			setPanelEnv(t, f)
			tc.set(t)
			w := worldFromEnv(t, f)
			user := f.addUser(2468, "Smith.F", 2, []int{35, 323})

			res := follow(t, w.b, signInAs(t, f, w.b, user))

			if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != "foxhole" {
				t.Errorf("a user in group 323 lands on page %q with status %d, want foxhole with 200", got, res.StatusCode)
			}
		})
	}
}

// FOXHOLE_GROUP_ID set names the Foxhole group in place of 323: a user in
// the group it names lands on the Foxhole page, and a user in 323 alone gets
// the no-access page.
func TestFoxholeGroupIsTheOneTheVariableNames(t *testing.T) {
	f := newFakeForum(t)
	setPanelEnv(t, f)
	t.Setenv("FOXHOLE_GROUP_ID", "400")
	w := worldFromEnv(t, f)
	named := f.addUser(2468, "Smith.F", 2, []int{35, 400})
	other := f.addUser(1357, "Roe.R", 2, []int{35, 323})

	res := follow(t, w.b, signInAs(t, f, w.b, named))

	if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != "foxhole" {
		t.Errorf("a user in group 400 lands on page %q with status %d, want foxhole with 200", got, res.StatusCode)
	}
	b := newBrowser(t, w.p)
	assertNoAccessPage(t, parseHTML(t, follow(t, b, signInAs(t, f, b, other))))
}

// A FOXHOLE_GROUP_ID that isn't a number stops startup with an error naming
// the variable: a typo fails loudly instead of locking every Foxhole
// manager out.
func TestConfigFromEnvRefusesAFoxholeGroupThatIsNotANumber(t *testing.T) {
	setPanelEnv(t, newFakeForum(t))
	t.Setenv("FOXHOLE_GROUP_ID", "foxhole")

	_, err := ConfigFromEnv()

	if err == nil || !strings.Contains(err.Error(), "FOXHOLE_GROUP_ID") {
		t.Fatalf("ConfigFromEnv error = %v, want one naming FOXHOLE_GROUP_ID", err)
	}
}
