//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const discordAPI = "https://discord.com/api/v10"

// maxTestGuildMembers is the most members a guild can have before a run
// treats it as a live server and refuses it. A test guild has a handful; the
// 7Cav server has thousands.
const maxTestGuildMembers = 100

type discordClient struct {
	base   string
	token  string
	client *http.Client
}

func newDiscordClient(token string) discordClient {
	return discordClient{base: discordAPI, token: token, client: &http.Client{Timeout: 20 * time.Second}}
}

// do sends one request as the bot. reason, when set, is the audit log reason
// Discord records for a change.
func (c discordClient) do(method, path string, body []byte, reason string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/7cav/cavbot2, smoke)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if reason != "" {
		// Encoded as commands/guild_manager.go encodes the bot's own reasons.
		req.Header.Set("X-Audit-Log-Reason", strings.ReplaceAll(url.QueryEscape(reason), "+", "%20"))
	}
	res, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return res.StatusCode, out, err
}

func (c discordClient) getJSON(path string, v any) error {
	status, body, err := c.do(http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s answered %d: %s", path, status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, v)
}

// checkTestGuild confirms the token and guild ID name a bot in a guild small
// enough to be a test guild, and returns a line naming both.
func checkTestGuild(c discordClient, guildID string, allowLarge bool) (string, error) {
	var bot struct {
		Username string `json:"username"`
	}
	if err := c.getJSON("/users/@me", &bot); err != nil {
		return "", fmt.Errorf("DISCORD_TOKEN: %w", err)
	}
	var guild struct {
		Name    string `json:"name"`
		Members int    `json:"approximate_member_count"`
	}
	if err := c.getJSON("/guilds/"+guildID+"?with_counts=true", &guild); err != nil {
		return "", fmt.Errorf("GUILD_ID: %w", err)
	}
	summary := fmt.Sprintf("bot %s in guild %q (%d members)", bot.Username, guild.Name, guild.Members)
	if guild.Members > maxTestGuildMembers && !allowLarge {
		return "", fmt.Errorf("%s: more than %d members looks like a live server, not a test guild; pass --allow-large-guild if it is yours to test on", summary, maxTestGuildMembers)
	}
	return summary, nil
}

// cmdAPI is `smoke api`: one Discord REST call as the bot, for setting up
// and checking a smoke test without a Discord client. A change (anything but
// GET) first passes the test guild check.
func cmdAPI(args []string) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	reason := fs.String("reason", "smoke test", "audit log reason for a change")
	envFile := fs.String("env-file", "", "env file to read DISCORD_TOKEN and GUILD_ID from (default: .env at the main checkout's root)")
	allowLarge := fs.Bool("allow-large-guild", false, "allow a change on a guild with more than 100 members")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: go run ./tools/smoke api [flags] METHOD PATH [JSON | -]")
		_, _ = fmt.Fprintln(fs.Output(), "{guild} in PATH becomes GUILD_ID. - reads the body from stdin.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 || fs.NArg() > 3 {
		fs.Usage()
		return errUsage
	}
	method := strings.ToUpper(fs.Arg(0))
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	path := *envFile
	if path == "" {
		path = g.defaultEnvFile()
	}
	vals, err := readEnvFile(path)
	if err != nil {
		return err
	}
	if missing := missingKeys(vals, []string{"DISCORD_TOKEN", "GUILD_ID"}); len(missing) > 0 {
		return fmt.Errorf("%s has no value for %s", path, strings.Join(missing, ", "))
	}
	c := newDiscordClient(vals["DISCORD_TOKEN"])
	if method != http.MethodGet {
		if _, err := checkTestGuild(c, vals["GUILD_ID"], *allowLarge); err != nil {
			return err
		}
	}
	apiPath := strings.ReplaceAll(fs.Arg(1), "{guild}", vals["GUILD_ID"])
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	var body []byte
	switch {
	case fs.NArg() == 3 && fs.Arg(2) == "-":
		if body, err = io.ReadAll(os.Stdin); err != nil {
			return err
		}
	case fs.NArg() == 3:
		body = []byte(fs.Arg(2))
	}
	status, out, err := c.do(method, apiPath, body, *reason)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "HTTP %d\n", status)
	var pretty bytes.Buffer
	if json.Indent(&pretty, out, "", "  ") == nil {
		out = pretty.Bytes()
	}
	if len(out) > 0 {
		fmt.Println(string(out))
	}
	if status >= 400 {
		return errSilent
	}
	return nil
}
