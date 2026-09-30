package commands

import (
	"net/http"
	"testing"
	"time"
)

// A Discord read through the production adapter says how it spent its time
// (#395): waiting inside discordgo before its requests went out, on trips to
// Discord and back, and in how many attempts. Discord here is the fake API
// under a real session, so the waits and retries are discordgo's own.

// timedRead is one of the adapter's timed reads, reduced to its timing.
type timedRead struct {
	name string
	// reply is a body Discord would answer the read with.
	reply string
	read  func(TempVCManager) (ReadTiming, error)
}

var guildChannelsTimedRead = timedRead{"guild channels", `[]`, func(m TempVCManager) (ReadTiming, error) {
	_, timing, err := m.GuildChannels(testTempVCGuild)
	return timing, err
}}

var guildTimedRead = timedRead{"guild", `{"id":"` + testTempVCGuild + `"}`, func(m TempVCManager) (ReadTiming, error) {
	_, timing, err := m.Guild(testTempVCGuild)
	return timing, err
}}

// A slow answer from Discord is time on the trip, in one attempt, and none
// of it is waiting inside discordgo.
func TestSessionTempVCManagerSlowAnswerIsTripTime(t *testing.T) {
	const delay = 100 * time.Millisecond
	for _, rd := range []timedRead{guildChannelsTimedRead, guildTimedRead} {
		t.Run(rd.name, func(t *testing.T) {
			api := &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
				time.Sleep(delay)
				return http.StatusOK, []byte(rd.reply)
			}}
			mgr := NewSessionTempVCManager(sessionOver(t, api, nil))

			timing, err := rd.read(mgr)

			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if timing.Attempts != 1 {
				t.Errorf("attempts = %d, want 1", timing.Attempts)
			}
			if timing.Trips < delay {
				t.Errorf("trips = %v, want at least the answer's delay %v", timing.Trips, delay)
			}
			if timing.Waiting >= delay {
				t.Errorf("waiting = %v, want the answer's delay %v on the trip, not waiting", timing.Waiting, delay)
			}
		})
	}
}

// A read discordgo retries after a 502 goes through in two attempts.
func TestSessionTempVCManagerRetriedReadCountsItsAttempts(t *testing.T) {
	var answered int
	api := &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
		answered++
		if answered == 1 {
			return http.StatusBadGateway, []byte(`{"message":"502: Bad Gateway"}`)
		}
		return http.StatusOK, []byte(guildChannelsTimedRead.reply)
	}}
	mgr := NewSessionTempVCManager(sessionOver(t, api, nil))

	timing, err := guildChannelsTimedRead.read(mgr)

	if err != nil {
		t.Fatalf("read after a 502: %v, want it to go through", err)
	}
	if timing.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", timing.Attempts)
	}
}

// A read made while its route's rate-limit bucket is empty waits inside
// discordgo for the bucket to reset before its request goes out: waiting,
// not trip time.
func TestSessionTempVCManagerReadBehindAnEmptyBucketIsWaiting(t *testing.T) {
	const reset = 200 * time.Millisecond
	emptied := http.Header{}
	emptied.Set("X-RateLimit-Remaining", "0")
	emptied.Set("X-RateLimit-Reset-After", "0.2")
	api := &fakeDiscordAPI{
		answer: func(*http.Request, []byte) (int, []byte) {
			return http.StatusOK, []byte(guildChannelsTimedRead.reply)
		},
		header: emptied,
	}
	mgr := NewSessionTempVCManager(sessionOver(t, api, nil))
	if _, err := guildChannelsTimedRead.read(mgr); err != nil {
		t.Fatalf("the read that empties the bucket: %v", err)
	}

	timing, err := guildChannelsTimedRead.read(mgr)

	if err != nil {
		t.Fatalf("read behind the empty bucket: %v", err)
	}
	atLeast := reset * 3 / 4
	if timing.Waiting < atLeast {
		t.Errorf("waiting = %v, want at least %v of the bucket's %v reset", timing.Waiting, atLeast, reset)
	}
	if timing.Trips >= atLeast {
		t.Errorf("trips = %v, want the bucket's %v reset as waiting, not on the trip", timing.Trips, reset)
	}
}
