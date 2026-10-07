//go:build unix

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeDiscord answers the two calls the test guild check makes, for a guild
// with the given member count.
func fakeDiscord(t *testing.T, members int) discordClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/@me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"username":"testbot"}`)
	})
	mux.HandleFunc("GET /guilds/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"name":"test guild","approximate_member_count":%d}`, members)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newDiscordClient("token")
	c.base = srv.URL
	return c
}

func TestCheckTestGuildRefusesAGuildTheSizeOfALiveServer(t *testing.T) {
	if _, err := checkTestGuild(fakeDiscord(t, 4000), "1", false); err == nil {
		t.Fatal("a 4000-member guild passed as a test guild")
	}
	if _, err := checkTestGuild(fakeDiscord(t, 4000), "1", true); err != nil {
		t.Fatalf("--allow-large-guild still refused: %v", err)
	}
	if _, err := checkTestGuild(fakeDiscord(t, 8), "1", false); err != nil {
		t.Fatalf("an 8-member guild was refused: %v", err)
	}
}
