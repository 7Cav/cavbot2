//go:build unix

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Where a smoke run listens. The panel's address is fixed because the forum's
// OAuth client registers http://localhost:8080/auth/callback for local runs,
// and --forum real has to match it.
const (
	panelAddr    = "127.0.0.1:8080"
	panelBaseURL = "http://localhost:8080"
	forumAddr    = "127.0.0.1:8091"
	// forumBrowserURL is what the browser follows to the consent page; the
	// bot calls the token and userinfo endpoints on the loopback address.
	forumBrowserURL = "http://localhost:8091"
	forumServerURL  = "http://127.0.0.1:8091"
)

// The bot's own Postgres for smoke runs. The volume keeps hubs and Foxhole
// rows between runs, as the test guild keeps its channels and roles. The
// password guards a loopback-only throwaway.
const (
	pgContainer = "cavbot2-smoke-pg"
	pgVolume    = "cavbot2-smoke-pgdata"
	pgImage     = "postgres:18-alpine"
	pgPort      = "5434"
	pgDSN       = "postgres://postgres:smoke@127.0.0.1:" + pgPort + "/postgres?sslmode=disable"
)

const (
	forumFake = "fake"
	forumReal = "real"
)

// requiredKeys are the env file variables a run cannot start without: the
// bot panics without the first three, and --forum real signs in with the
// rest.
var (
	requiredKeys     = []string{"DISCORD_TOKEN", "GUILD_ID", "BM_TOKEN"}
	requiredRealKeys = []string{"PANEL_OAUTH_CLIENT_ID", "PANEL_OAUTH_CLIENT_SECRET", "PANEL_OAUTH_AUTHORIZE_URL", "PANEL_OAUTH_TOKEN_URL", "PANEL_OAUTH_USERINFO_URL"}
)

// gitPaths are the two directories a run is keyed on: the worktree it builds
// and the repository's common git directory, which every worktree shares.
type gitPaths struct {
	worktree string
	common   string
}

func findGitPaths() (gitPaths, error) {
	top, err := gitOutput("", "rev-parse", "--show-toplevel")
	if err != nil {
		return gitPaths{}, fmt.Errorf("run this from inside a cavbot2 checkout: %w", err)
	}
	common, err := gitOutput(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return gitPaths{}, err
	}
	return gitPaths{worktree: top, common: common}, nil
}

// stateDir holds the lock, the run file, the logs and the built bot. It sits
// in the common git directory so every worktree sees the same run, and git
// never tracks it.
func (g gitPaths) stateDir() string { return filepath.Join(g.common, "cavbot2-smoke") }

// defaultEnvFile is the .env at the main checkout's root. Linked worktrees
// have none of their own, since .env is gitignored.
func (g gitPaths) defaultEnvFile() string { return filepath.Join(filepath.Dir(g.common), ".env") }

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// readEnvFile parses the env file the way the bot's godotenv.Load does. The
// values are secrets: callers pass them on and report names only.
func readEnvFile(path string) (map[string]string, error) {
	vals, err := godotenv.Read(path)
	if err != nil {
		return nil, fmt.Errorf("read the env file %s: %w", path, err)
	}
	return vals, nil
}

// missingKeys names the keys that are absent or blank in vals.
func missingKeys(vals map[string]string, keys []string) []string {
	var missing []string
	for _, k := range keys {
		if vals[k] == "" {
			missing = append(missing, k)
		}
	}
	return missing
}

// smokeOverrides is what a run changes from the env file. The panel listens
// where the forum client's local redirect URI points, the bot's store is the
// smoke Postgres, nothing reaches Sentry, and the forum database, which only
// resolves on the production Docker network, is off. In fake mode the panel
// signs in through the fake forum, whose personas carry these group IDs.
func smokeOverrides(forum, dsn string) map[string]string {
	o := map[string]string{
		"PANEL_ADDR":     panelAddr,
		"PANEL_BASE_URL": panelBaseURL,
		"BOT_DB_DSN":     dsn,
		"FORUM_DB_DSN":   "",
		"SENTRY_DSN":     "",
		"APP_ENV":        "smoke",
	}
	if forum == forumFake {
		o["PANEL_OAUTH_CLIENT_ID"] = fakeClientID
		o["PANEL_OAUTH_CLIENT_SECRET"] = fakeClientSecret
		o["PANEL_OAUTH_AUTHORIZE_URL"] = forumBrowserURL + "/oauth2/authorize"
		o["PANEL_OAUTH_TOKEN_URL"] = forumServerURL + "/api/oauth2/token"
		o["PANEL_OAUTH_USERINFO_URL"] = forumServerURL + "/api/me"
		o["PANEL_GROUP_IDS"] = strconv.Itoa(adminGroupID)
		o["FOXHOLE_GROUP_ID"] = strconv.Itoa(foxholeGroupID)
	}
	return o
}

// botEnv is the bot's environment: the caller's, then the env file, then the
// smoke overrides, then the --env flags. os/exec keeps the last value of a
// repeated key, so each layer wins over the ones before it. That inverts the
// bot's own rule, where an exported variable beats .env, so a stray export in
// the caller's shell cannot point a run at the wrong guild.
func botEnv(base []string, file, overrides map[string]string, extra []string) []string {
	env := slices.Clone(base)
	for _, layer := range []map[string]string{file, overrides} {
		keys := make([]string, 0, len(layer))
		for k := range layer {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			env = append(env, k+"="+layer[k])
		}
	}
	return append(env, extra...)
}

// extraValue is the value an --env flag gives key, if one does.
func extraValue(extra []string, key string) (string, bool) {
	val, found := "", false
	for _, kv := range extra {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			val, found = v, true
		}
	}
	return val, found
}
