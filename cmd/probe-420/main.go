// Command probe-420 runs the three test-guild experiments of
// https://github.com/7Cav/cavbot2/issues/420 and prints what came back.
//
// Throwaway, never merged. It creates a role, grants and removes it on up to
// four human members, then deletes it. With -all it grants the role to every
// member, bots included. Run it against a test guild only: it refuses a guild
// with more than 50 members.
//
//	go run ./cmd/probe-420 [-all]
//
// It reads DISCORD_TOKEN and GUILD_ID from the environment. Members appear in
// the output as "member N" and the guild as "<guild>", never by name or ID.
package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	auditReason = "wayfinder #420 probe"
	maxMembers  = 50
	maxTargets  = 4
	apiBase     = "https://discord.com/api/v10"
)

var start = time.Now()

func logf(format string, args ...any) {
	fmt.Printf("%8.3fs  %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, args...))
}

// requestGuildMembersData copies discordgo v0.29.0's unexported struct of the
// same name (wsapi.go:438-446), so the probe can print the payload
// RequestGuildMembers writes.
type requestGuildMembersData struct {
	GuildIDs  []string  `json:"guild_id"`
	Query     *string   `json:"query,omitempty"`
	UserIDs   *[]string `json:"user_ids,omitempty"`
	Limit     int       `json:"limit"`
	Nonce     string    `json:"nonce,omitempty"`
	Presences bool      `json:"presences"`
}

type probe struct {
	s       *discordgo.Session
	token   string
	guildID string

	mu     sync.Mutex
	alias  map[string]string // user ID to "member N"
	roleID string            // the probe role, once created
	phase  string
	counts map[string]int // member updates per phase
}

func main() {
	all := flag.Bool("all", false, "grant the probe role to every member, bots included")
	flag.Parse()
	token, guildID := os.Getenv("DISCORD_TOKEN"), os.Getenv("GUILD_ID")
	if token == "" || guildID == "" {
		fmt.Fprintln(os.Stderr, "DISCORD_TOKEN and GUILD_ID must be set")
		os.Exit(2)
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMembers
	s.SyncEvents = true // handlers run in arrival order on the gateway goroutine

	p := &probe{s: s, token: token, guildID: guildID, alias: map[string]string{}, phase: "connect", counts: map[string]int{}}

	guildCreated := make(chan *discordgo.GuildCreate, 4)
	chunks := make(chan *discordgo.GuildMembersChunk, 256)
	rateLimited := make(chan string, 16)
	s.AddHandler(func(_ *discordgo.Session, g *discordgo.GuildCreate) {
		if g.ID == guildID {
			guildCreated <- g
		}
	})
	s.AddHandler(func(_ *discordgo.Session, c *discordgo.GuildMembersChunk) {
		if c.GuildID == guildID {
			chunks <- c
		}
	})
	s.AddHandler(func(_ *discordgo.Session, e *discordgo.Event) {
		if e.Type == "RATE_LIMITED" {
			rateLimited <- p.redact(string(e.RawData))
		}
	})
	s.AddHandler(p.onMemberUpdate)
	s.AddHandler(p.onRoleCreate)
	s.AddHandler(p.onRoleDelete)

	if err := s.Open(); err != nil {
		logf("gateway open failed: %v", err)
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()
	logf("gateway open")

	var g *discordgo.GuildCreate
	select {
	case g = <-guildCreated:
	case <-time.After(30 * time.Second):
		logf("no GUILD_CREATE within 30 s")
		os.Exit(1)
	}
	logf("GUILD_CREATE: member_count %d, members in payload %d", g.MemberCount, len(g.Members))
	if g.MemberCount > maxMembers {
		logf("refusing: %d members is more than a test guild's %d", g.MemberCount, maxMembers)
		os.Exit(1)
	}
	logf("state before the request: %d members", p.stateMembers())

	fmt.Println("\n== Experiment 1: opcode 8 with discordgo's array guild_id ==")
	p.memberRequest(chunks, rateLimited)

	targets := p.pickTargets(*all)
	if len(targets) == 0 {
		logf("no human members to grant the probe role to")
		os.Exit(1)
	}

	p.setPhase("create")
	role, err := s.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: "wayfinder-420 probe"}, discordgo.WithAuditLogReason(auditReason))
	if err != nil {
		logf("role create failed: %v", err)
		os.Exit(1)
	}
	p.mu.Lock()
	p.roleID = role.ID
	p.mu.Unlock()
	logf("created role %s at position %d", role.ID, role.Position)
	deleted := false
	defer func() {
		if !deleted {
			err := s.GuildRoleDelete(guildID, role.ID, discordgo.WithAuditLogReason(auditReason))
			logf("cleanup: deleted role %s, err %v", role.ID, err)
		}
	}()

	fmt.Println("\n== Experiment 3: rate-limit headers on add and remove ==")
	p.setPhase("grant/remove")
	p.headers(role.ID, targets)

	fmt.Println("\n== Experiment 2: role delete with holders ==")
	time.Sleep(3 * time.Second) // let the grants' member updates land
	p.report(role.ID, targets, "before delete")
	p.setPhase("after delete")
	t := time.Now()
	if err := s.GuildRoleDelete(guildID, role.ID, discordgo.WithAuditLogReason(auditReason)); err != nil {
		logf("role delete failed: %v", err)
		return
	}
	deleted = true
	logf("DELETE role returned after %s; listening 30 s", time.Since(t).Round(time.Millisecond))
	time.Sleep(30 * time.Second)
	p.report(role.ID, targets, "30 s after delete")
	p.mu.Lock()
	logf("member updates per phase: %v", p.counts)
	p.mu.Unlock()
}

func (p *probe) memberRequest(chunks <-chan *discordgo.GuildMembersChunk, rateLimited <-chan string) {
	query := ""
	payload, _ := json.Marshal(struct {
		Op   int                     `json:"op"`
		Data requestGuildMembersData `json:"d"`
	}{8, requestGuildMembersData{GuildIDs: []string{"<guild>"}, Query: &query, Limit: 0, Nonce: "wf420-a"}})
	logf("discordgo writes: %s", payload)

	p.awaitChunks("wf420-a", chunks, rateLimited, 30*time.Second)
	logf("state after the request: %d members", p.stateMembers())

	fmt.Println("\n-- second full-list request at once, inside the 30 s window --")
	p.awaitChunks("wf420-b", chunks, rateLimited, 10*time.Second)
}

func (p *probe) awaitChunks(nonce string, chunks <-chan *discordgo.GuildMembersChunk, rateLimited <-chan string, wait time.Duration) {
	sent := time.Now()
	if err := p.s.RequestGuildMembers(p.guildID, "", 0, nonce, false); err != nil {
		logf("RequestGuildMembers(%s) failed: %v", nonce, err)
		return
	}
	logf("opcode 8 sent, nonce %s", nonce)
	timeout := time.After(wait)
	for {
		select {
		case c := <-chunks:
			logf("GUILD_MEMBERS_CHUNK nonce %q, index %d of count %d: %d members, not_found %d, %s after the request",
				c.Nonce, c.ChunkIndex, c.ChunkCount, len(c.Members), len(c.NotFound), time.Since(sent).Round(time.Millisecond))
			if c.Nonce == nonce && c.ChunkIndex == c.ChunkCount-1 {
				return
			}
		case raw := <-rateLimited:
			logf("RATE_LIMITED %s after the request: %s", time.Since(sent).Round(time.Millisecond), raw)
			return
		case <-timeout:
			logf("no last chunk and no RATE_LIMITED within %s", wait)
			return
		}
	}
}

func (p *probe) stateMembers() int {
	g, err := p.s.State.Guild(p.guildID)
	if err != nil {
		return -1
	}
	p.s.State.RLock()
	defer p.s.State.RUnlock()
	return len(g.Members)
}

// pickTargets names up to four human members, or every member with all,
// lowest ID first.
func (p *probe) pickTargets(all bool) []string {
	g, err := p.s.State.Guild(p.guildID)
	if err != nil {
		return nil
	}
	p.s.State.RLock()
	var ids []string
	for _, m := range g.Members {
		if m.User != nil && (all || !m.User.Bot) {
			ids = append(ids, m.User.ID)
		}
	}
	p.s.State.RUnlock()
	slices.SortFunc(ids, func(a, b string) int {
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		return cmp.Compare(x, y)
	})
	if !all && len(ids) > maxTargets {
		ids = ids[:maxTargets]
	}
	p.mu.Lock()
	for i, id := range ids {
		p.alias[id] = fmt.Sprintf("member %d", i+1)
	}
	p.mu.Unlock()
	return ids
}

func (p *probe) headers(roleID string, targets []string) {
	path := func(userID string) string {
		return fmt.Sprintf("/guilds/%s/members/%s/roles/%s", p.guildID, userID, roleID)
	}
	first := targets[0]
	steps := []struct{ method, user string }{
		{http.MethodPut, first},
		{http.MethodDelete, first},
		{http.MethodPut, first},
		{http.MethodDelete, first},
		{http.MethodPut, first},
	}
	for _, id := range targets[1:] {
		steps = append(steps, struct{ method, user string }{http.MethodPut, id})
	}
	for _, st := range steps {
		p.call(st.method, path(st.user), p.name(st.user))
	}
}

// call sends one member-role request, prints its rate-limit headers, and
// sleeps out the window when the bucket is spent or Discord answers 429.
func (p *probe) call(method, path, who string) {
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequest(method, apiBase+path, nil)
		req.Header.Set("Authorization", "Bot "+p.token)
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/7cav/cavbot2, probe-420)")
		req.Header.Set("X-Audit-Log-Reason", url.PathEscape(auditReason))
		t := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logf("%s %s: %v", method, who, err)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		h := resp.Header
		logf("%-6s %s: %d in %s, limit %s, remaining %s, reset-after %s, bucket %s, scope %q",
			method, who, resp.StatusCode, time.Since(t).Round(time.Millisecond),
			h.Get("X-RateLimit-Limit"), h.Get("X-RateLimit-Remaining"), h.Get("X-RateLimit-Reset-After"),
			h.Get("X-RateLimit-Bucket"), h.Get("X-RateLimit-Scope"))
		if resp.StatusCode == http.StatusTooManyRequests {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			_ = json.Unmarshal(body, &rl)
			logf("429 body: %s", p.redact(string(body)))
			time.Sleep(time.Duration(rl.RetryAfter*float64(time.Second)) + 100*time.Millisecond)
			continue
		}
		if resp.StatusCode >= 300 {
			logf("body: %s", p.redact(string(body)))
		}
		if h.Get("X-RateLimit-Remaining") == "0" {
			after, _ := strconv.ParseFloat(h.Get("X-RateLimit-Reset-After"), 64)
			time.Sleep(time.Duration(after*float64(time.Second)) + 100*time.Millisecond)
		}
		return
	}
}

// report prints, for each target, whether the bot's cached member and
// Discord's own member record still list the role.
func (p *probe) report(roleID string, targets []string, when string) {
	for _, id := range targets {
		cached := "not cached"
		if m, err := p.s.State.Member(p.guildID, id); err == nil {
			p.s.State.RLock()
			cached = yesNo(slices.Contains(m.Roles, roleID))
			p.s.State.RUnlock()
		}
		live := "lookup failed"
		if m, err := p.s.GuildMember(p.guildID, id); err == nil {
			live = yesNo(slices.Contains(m.Roles, roleID))
		}
		logf("%s, %s: cached member lists the role: %s; Discord's member record lists it: %s", when, p.name(id), cached, live)
	}
}

func (p *probe) onMemberUpdate(_ *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	if m.GuildID != p.guildID || m.Member == nil || m.User == nil {
		return
	}
	p.mu.Lock()
	p.counts[p.phase]++
	roleID, phase := p.roleID, p.phase
	p.mu.Unlock()
	logf("GUILD_MEMBER_UPDATE (%s) %s: lists the probe role: %s", phase, p.name(m.User.ID), yesNo(roleID != "" && slices.Contains(m.Roles, roleID)))
}

func (p *probe) onRoleCreate(_ *discordgo.Session, r *discordgo.GuildRoleCreate) {
	if r.GuildID == p.guildID && r.Role != nil {
		logf("GUILD_ROLE_CREATE %s", r.Role.ID)
	}
}

func (p *probe) onRoleDelete(_ *discordgo.Session, r *discordgo.GuildRoleDelete) {
	if r.GuildID == p.guildID {
		logf("GUILD_ROLE_DELETE %s", r.RoleID)
	}
}

func (p *probe) setPhase(phase string) {
	p.mu.Lock()
	p.phase = phase
	p.mu.Unlock()
}

func (p *probe) name(userID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a, ok := p.alias[userID]; ok {
		return a
	}
	return "a non-target member"
}

func (p *probe) redact(s string) string {
	return strings.ReplaceAll(s, p.guildID, "<guild>")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
