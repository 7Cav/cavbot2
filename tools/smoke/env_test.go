//go:build unix

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// seenByProcess runs env under the environment and returns what a child
// process actually receives, the way the bot receives botEnv.
func seenByProcess(t *testing.T, env []string) map[string]string {
	t.Helper()
	cmd := exec.Command("env")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
	}
	return got
}

func TestBotEnvLayersWinInOrderShellFileSmokeFlags(t *testing.T) {
	shell := []string{"PATH=/usr/bin:/bin", "GUILD_ID=from-shell", "LOG_LEVEL=from-shell", "KEPT=from-shell"}
	file := map[string]string{"GUILD_ID": "from-file", "LOG_LEVEL": "from-file", "PANEL_ADDR": ":80"}
	smoke := map[string]string{"PANEL_ADDR": panelAddr, "SENTRY_DSN": ""}
	flags := []string{"LOG_LEVEL=from-flag"}

	got := seenByProcess(t, botEnv(shell, file, smoke, flags))

	for key, want := range map[string]string{
		"KEPT":       "from-shell",
		"GUILD_ID":   "from-file",
		"PANEL_ADDR": panelAddr,
		"LOG_LEVEL":  "from-flag",
		"SENTRY_DSN": "",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

func TestSmokeOverridesPointThePanelAtTheRegisteredLocalCallback(t *testing.T) {
	for _, forum := range []string{forumFake, forumReal} {
		o := smokeOverrides(forum, pgDSN)
		if got := o["PANEL_BASE_URL"] + "/auth/callback"; got != "http://localhost:8080/auth/callback" {
			t.Errorf("%s: redirect URI %q is not the forum client's local one", forum, got)
		}
		if o["BOT_DB_DSN"] != pgDSN {
			t.Errorf("%s: BOT_DB_DSN = %q, want the smoke database", forum, o["BOT_DB_DSN"])
		}
		for _, off := range []string{"SENTRY_DSN", "FORUM_DB_DSN"} {
			if v, set := o[off]; !set || v != "" {
				t.Errorf("%s: %s must be set empty, got %q (set %v)", forum, off, v, set)
			}
		}
	}
}

func TestSmokeOverridesSendSignInToTheFakeOnlyInFakeMode(t *testing.T) {
	fake := smokeOverrides(forumFake, pgDSN)
	if !strings.HasPrefix(fake["PANEL_OAUTH_AUTHORIZE_URL"], forumBrowserURL) ||
		!strings.HasPrefix(fake["PANEL_OAUTH_TOKEN_URL"], forumServerURL) ||
		!strings.HasPrefix(fake["PANEL_OAUTH_USERINFO_URL"], forumServerURL) {
		t.Errorf("fake mode signs in elsewhere: %v", fake)
	}
	if fake["PANEL_OAUTH_CLIENT_ID"] != fakeClientID || fake["PANEL_OAUTH_CLIENT_SECRET"] != fakeClientSecret {
		t.Errorf("fake mode's client is not the one the fake forum accepts")
	}

	realMode := smokeOverrides(forumReal, pgDSN)
	for _, key := range append(requiredRealKeys, "PANEL_GROUP_IDS", "FOXHOLE_GROUP_ID") {
		if _, set := realMode[key]; set {
			t.Errorf("real mode overrides %s; the env file's value must stand", key)
		}
	}
}
