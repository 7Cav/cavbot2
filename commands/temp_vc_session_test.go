package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The production adapter's REST calls, over a real discordgo session whose
// transport answers in place of Discord, so nothing leaves the process.

// fakeDiscordAPI answers a session's REST calls in place of Discord and
// records each request it was sent.
type fakeDiscordAPI struct {
	mu       sync.Mutex
	requests []apiRequest
	// answer builds the reply to each request, given its body.
	answer func(r *http.Request, body []byte) (status int, reply []byte)
}

// apiRequest is one request as the fake API received it.
type apiRequest struct {
	method, path, auditReason string
	body                      []byte
}

func (a *fakeDiscordAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		body = b
	}
	a.mu.Lock()
	a.requests = append(a.requests, apiRequest{
		method: r.Method, path: r.URL.Path, auditReason: r.Header.Get("X-Audit-Log-Reason"), body: body,
	})
	a.mu.Unlock()
	status, reply := a.answer(r, body)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(reply)),
		Request:    r,
	}, nil
}

// received returns the requests the fake API has been sent so far.
func (a *fakeDiscordAPI) received() []apiRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]apiRequest(nil), a.requests...)
}

// sessionOver builds a real session whose REST calls reach api, and whose
// state cache holds the test guild with chan-1 carrying the given list.
func sessionOver(t *testing.T, api *fakeDiscordAPI, cached []*discordgo.PermissionOverwrite) *discordgo.Session {
	t.Helper()
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	dg.Client = &http.Client{Transport: api}
	feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Channels: []*discordgo.Channel{{
			ID: "chan-1", GuildID: testTempVCGuild, Type: discordgo.ChannelTypeGuildVoice,
			PermissionOverwrites: cached,
		}},
	}})
	return dg
}

// sameOverwrites reports whether two lists hold the same overwrites, in any
// order.
func sameOverwrites(a, b []*discordgo.PermissionOverwrite) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]discordgo.PermissionOverwrite, len(a))
	for _, o := range a {
		byID[o.ID] = *o
	}
	for _, o := range b {
		if got, ok := byID[o.ID]; !ok || got != *o {
			return false
		}
	}
	return true
}

// Replacing a channel's overwrites sends the whole list as one edit of the
// channel, an empty list included, with the audit log reason. An empty list
// left out of the request would leave the lock in place. As soon as the
// call returns, the state cache holds the list Discord answered with, so a
// lock sent next reads it without waiting for the CHANNEL_UPDATE.
func TestSessionTempVCManagerChannelOverwritesReplace(t *testing.T) {
	locked := []*discordgo.PermissionOverwrite{
		{ID: testTempVCGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionVoiceConnect},
		{ID: "user-g", Type: discordgo.PermissionOverwriteTypeMember, Allow: discordgo.PermissionVoiceConnect},
	}
	for _, tc := range []struct {
		name string
		list []*discordgo.PermissionOverwrite
	}{
		{"a list", permCategoryOverwrites()},
		{"an empty list", []*discordgo.PermissionOverwrite{}},
		{"a nil list", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeDiscordAPI{answer: func(_ *http.Request, body []byte) (int, []byte) {
				var sent map[string]json.RawMessage
				if err := json.Unmarshal(body, &sent); err != nil {
					return http.StatusBadRequest, []byte(`{"message":"bad body","code":50035}`)
				}
				list := sent["permission_overwrites"]
				if list == nil {
					list = json.RawMessage("[]")
				}
				return http.StatusOK, []byte(`{"id":"chan-1","guild_id":"` + testTempVCGuild +
					`","type":2,"permission_overwrites":` + string(list) + `}`)
			}}
			mgr := NewSessionTempVCManager(sessionOver(t, api, locked))

			if err := mgr.ChannelOverwritesReplace("chan-1", tc.list, "unlocked by user-o"); err != nil {
				t.Fatalf("ChannelOverwritesReplace: %v", err)
			}

			requests := api.received()
			if len(requests) != 1 {
				t.Fatalf("requests = %+v, want one edit", requests)
			}
			req := requests[0]
			if req.method != http.MethodPatch || !strings.HasSuffix(req.path, "/channels/chan-1") {
				t.Errorf("request = %s %s, want an edit of chan-1", req.method, req.path)
			}
			if !strings.Contains(req.auditReason, "user-o") {
				t.Errorf("audit reason %q does not name user-o", req.auditReason)
			}
			var sent struct {
				PermissionOverwrites *[]*discordgo.PermissionOverwrite `json:"permission_overwrites"`
			}
			if err := json.Unmarshal(req.body, &sent); err != nil {
				t.Fatalf("request body %s: %v", req.body, err)
			}
			if sent.PermissionOverwrites == nil {
				t.Fatalf("request body %s carries no overwrite list, which leaves the channel's as it was", req.body)
			}
			if !sameOverwrites(*sent.PermissionOverwrites, tc.list) {
				t.Errorf("request body %s, want the list %+v", req.body, tc.list)
			}
			ch, err := mgr.Channel("chan-1")
			if err != nil {
				t.Fatalf("Channel(chan-1): %v", err)
			}
			if !sameOverwrites(ch.PermissionOverwrites, tc.list) {
				t.Errorf("the cache holds %d overwrites for chan-1 after the edit, want the %d sent", len(ch.PermissionOverwrites), len(tc.list))
			}
		})
	}
}
