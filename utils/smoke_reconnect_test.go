package utils

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// watchReconnectFile stands up a scripted gateway that answers the first
// login with READY and every later one with later, and opens a session on it.
// It returns the gateway and the reconnect hook file the session is watched
// by, which doesn't exist yet, once the session has sent its first
// heartbeat (sentOnLogin).
func watchReconnectFile(t *testing.T, later gatewayLogin) (*scriptedGateway, string) {
	t.Helper()
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		if login == 1 {
			return gatewayLogin{answer: "READY"}
		}
		return later
	})
	dg := openWatchedSession(t, time.Minute)
	sentOnLogin(t, g, 1)
	file := filepath.Join(t.TempDir(), "reconnect")
	// Stopped before the session closes, so no reconnect outlives the test
	// and logs in at the next test's gateway.
	t.Cleanup(WatchReconnects(dg, file, "session", "recorder"))
	return g, file
}

func writeReconnectFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the reconnect file: %v", err)
	}
}

// sentOnLogin waits for the session to have sent its first heartbeat on the
// nth login, and returns what it sent there. A session closed before its
// first heartbeat on a login reconnects on its own, so a test waits for it
// before the next close, its own end's included.
func sentOnLogin(t *testing.T, g *scriptedGateway, n int) sessionSent {
	t.Helper()
	waitUntil(func() bool { s, ok := g.sentOn(n); return ok && s.beat })
	s, ok := g.sentOn(n)
	if !ok || !s.beat {
		t.Fatalf("the session sent no heartbeat on login %d", n)
	}
	return s
}

// endsTheSession reports whether Discord ends a session closed with code:
// its gateway docs say a close with 1000 or 1001 invalidates the session,
// and one with any other code keeps it for a resume.
func endsTheSession(code int) bool {
	return code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway
}

// A smoke run makes the recorders' gateway sessions reconnect on demand
// through the file SMOKE_GATEWAY_RECONNECT_FILE names (docs/smoke-test.md).
// A file saying resume closes the connection with a code that keeps the
// session, and the login after it resumes that session, as after Discord's
// Op 7.
func TestSmokeReconnect_ResumeKeepsTheSessionAndResumesIt(t *testing.T) {
	g, file := watchReconnectFile(t, gatewayLogin{answer: "RESUMED"})

	writeReconnectFile(t, file, "resume\n")

	resume := sentOnLogin(t, g, 2)
	first, _ := g.sentOn(1)
	if first.closeCode == 0 || endsTheSession(first.closeCode) {
		t.Errorf("the session closed its connection with %d, want a code that keeps the session", first.closeCode)
	}
	if resume.op != 6 || resume.sessionID != "sess" {
		t.Errorf("the login after the close sent op %d for session %q, want a resume (6) of sess", resume.op, resume.sessionID)
	}
}

// A file saying identify closes the connection with a code that ends the
// session at Discord, and the session logs in again, so Discord answers its
// resume with Op 9 and it identifies anew.
func TestSmokeReconnect_IdentifyEndsTheSessionAndLogsInAgain(t *testing.T) {
	g, file := watchReconnectFile(t, gatewayLogin{invalid: true, answer: "READY"})

	writeReconnectFile(t, file, "identify\n")

	sentOnLogin(t, g, 2)
	if first, _ := g.sentOn(1); !endsTheSession(first.closeCode) {
		t.Errorf("the session closed its connection with %d, want a code that ends the session", first.closeCode)
	}
}

// A duration after the word keeps the session offline at least that long
// before it logs in again.
func TestSmokeReconnect_ADurationKeepsTheSessionOfflineThatLong(t *testing.T) {
	const offline = 1500 * time.Millisecond
	g, file := watchReconnectFile(t, gatewayLogin{answer: "RESUMED"})

	writeReconnectFile(t, file, "resume "+offline.String()+"\n")

	again := sentOnLogin(t, g, 2)
	first, _ := g.sentOn(1)
	if first.closedAt.IsZero() {
		t.Fatal("the session sent no close before it logged in again")
	}
	if gap := again.at.Sub(first.closedAt); gap < offline {
		t.Errorf("the session logged in again %v after its close, want at least %v", gap, offline)
	}
}

// Only the file's appearance counts. A file left in place reconnects the
// session once, and the file's next appearance reconnects it again.
func TestSmokeReconnect_OnlyTheFilesAppearanceReconnects(t *testing.T) {
	g, file := watchReconnectFile(t, gatewayLogin{answer: "RESUMED"})
	writeReconnectFile(t, file, "resume\n")
	sentOnLogin(t, g, 2)

	time.Sleep(8 * reconnectCheckEvery)
	if n := g.loginCount(); n != 2 {
		t.Fatalf("with the file left in place the gateway saw %d logins, want 2", n)
	}
	if err := os.Remove(file); err != nil {
		t.Fatalf("removing the reconnect file: %v", err)
	}
	time.Sleep(2 * reconnectCheckEvery)
	writeReconnectFile(t, file, "resume\n")

	sentOnLogin(t, g, 3)
}

// The hook says in the log which session it reconnects, and how, so a
// capture can find each reconnect among the lines of the sessions beside it.
func TestSmokeReconnect_LogsTheSessionItReconnects(t *testing.T) {
	logs := recordLogs(t)
	g, file := watchReconnectFile(t, gatewayLogin{invalid: true, answer: "READY"})

	writeReconnectFile(t, file, "identify\n")

	sentOnLogin(t, g, 2)
	if n := logs.linesSaying(smokeReconnectMessage, "session", "recorder"); n != 1 {
		t.Errorf("%d %q lines name the session, want 1", n, smokeReconnectMessage)
	}
	if n := logs.linesSaying(smokeReconnectMessage, "identify", "true"); n != 1 {
		t.Errorf("%d %q lines say it identifies, want 1", n, smokeReconnectMessage)
	}
}

// A hook file reads as the reconnect it names, and anything but identify
// resumes.
func TestSmokeReconnect_ParsesTheFile(t *testing.T) {
	tests := []struct {
		content string
		want    smokeReconnect
	}{
		{"", smokeReconnect{}},
		{"resume\n", smokeReconnect{}},
		{"identify\n", smokeReconnect{identify: true}},
		{"identify 30s\n", smokeReconnect{identify: true, offline: 30 * time.Second}},
		{"resume 2m", smokeReconnect{offline: 2 * time.Minute}},
	}
	for _, tt := range tests {
		if got := parseReconnect(tt.content); got != tt.want {
			t.Errorf("parseReconnect(%q) = %+v, want %+v", tt.content, got, tt.want)
		}
	}
}

// A session that can't reach the gateway when it logs in again keeps
// trying, as discordgo's own reconnect does, until it gets in, so a smoke
// run never leaves a recorder offline.
func TestSmokeReconnect_KeepsTryingUntilTheSessionGetsIn(t *testing.T) {
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		switch login {
		case 1:
			return gatewayLogin{answer: "READY"}
		case 2:
			return gatewayLogin{refuse: true}
		}
		return gatewayLogin{answer: "RESUMED"}
	})
	dg := openWatchedSession(t, time.Minute)
	sentOnLogin(t, g, 1)
	file := filepath.Join(t.TempDir(), "reconnect")
	t.Cleanup(WatchReconnects(dg, file, "session", "recorder"))

	writeReconnectFile(t, file, "resume\n")

	// The refused login got no upgrade, so the next to reach the session's
	// frames is the second in the gateway's record.
	if again := sentOnLogin(t, g, 2); again.op != 6 {
		t.Errorf("the login that got in sent op %d, want a resume (6)", again.op)
	}
}

// Stopping the hook while a session waits out its time offline ends the
// wait at once, so a shutdown doesn't wait for it, and the session stays
// offline.
func TestSmokeReconnect_StopEndsTheTimeOfflineAtOnce(t *testing.T) {
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		if login == 1 {
			return gatewayLogin{answer: "READY"}
		}
		return gatewayLogin{answer: "RESUMED"}
	})
	dg := openWatchedSession(t, time.Minute)
	sentOnLogin(t, g, 1)
	file := filepath.Join(t.TempDir(), "reconnect")
	stop := WatchReconnects(dg, file, "session", "recorder")
	t.Cleanup(stop)
	writeReconnectFile(t, file, "resume 1m\n")
	waitUntil(func() bool { s, _ := g.sentOn(1); return s.closeCode != 0 })

	// discordgo's close waits a second after the close frame, and the time
	// offline starts after it.
	time.Sleep(1500 * time.Millisecond)
	start := time.Now()
	stop()

	if took := time.Since(start); took > time.Second {
		t.Errorf("stop took %v, want it to end the time offline at once", took)
	}
	if n := g.loginCount(); n != 1 {
		t.Errorf("the gateway saw %d logins after the stop, want the session left offline", n)
	}
}
