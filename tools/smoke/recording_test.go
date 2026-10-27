//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
)

// fakeRecorder is a recorder session that connected.
type fakeRecorder struct{ id string }

func (f fakeRecorder) UserID() string { return f.id }
func (f fakeRecorder) Join(context.Context, string, string) (voice.Conn, error) {
	return nil, errors.New("not in this test")
}
func (f fakeRecorder) LogOut() error { return nil }
func (f fakeRecorder) Close() error  { return nil }

// refusingDiscord logs a recorder in for each token, with Discord refusing
// each token refused picks, at its gateway and its REST API alike. A
// refused login is voice.ErrTokenRefused, as the bot's gateway reports it.
type refusingDiscord struct {
	refused func(token string) bool
}

func (d refusingDiscord) Open(recorder int, token string) (voice.GatewaySession, error) {
	if d.refused(token) {
		return nil, fmt.Errorf("%w: %w", voice.ErrTokenRefused, errors.New("websocket: close 4004: Authentication failed."))
	}
	return fakeRecorder{id: strconv.Itoa(1000 + recorder)}, nil
}

func (d refusingDiscord) FailedToConnect(int) {}

func (d refusingDiscord) UserID(_ context.Context, token string) (string, error) {
	if d.refused(token) {
		return "", errors.New("HTTP 401 Unauthorized")
	}
	return "", errors.New("not in this test")
}

// openRefusing logs a recorder in for each token, with Discord refusing
// each token refused picks.
func openRefusing(refused func(token string) bool) voice.Opener {
	return refusingDiscord{refused: refused}
}

// botLog sets up the logger the bot sets up for itself, writing to a file,
// and returns a read of the lines it has logged so far.
func botLog(t *testing.T) func() []string {
	t.Helper()
	out, err := os.Create(t.TempDir() + "/bot.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	prevLogger, prevStdout := utils.Logger, os.Stdout
	t.Cleanup(func() { utils.Logger = prevLogger })
	os.Stdout = out
	utils.InitLogger("INFO")
	os.Stdout = prevStdout
	return func() []string {
		b, err := os.ReadFile(out.Name())
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// recorderLog runs the bot's recorder connect on raw, a RECORDER_TOKENS
// value, with Discord refusing the tokens in refused, and returns what the
// bot logs.
func recorderLog(t *testing.T, raw string, refused ...string) []string {
	t.Helper()
	logged := botLog(t)
	voice.Connect(raw, openRefusing(func(token string) bool { return slices.Contains(refused, token) })).Close()
	return logged()
}

func tallyOf(lines []string) recorderTally {
	var r recorderLines
	for _, line := range lines {
		r.read(line)
	}
	return r.snapshot()
}

func TestRecorderTallySaysRecordingIsOffWithNoToken(t *testing.T) {
	got := tallyOf(recorderLog(t, ""))
	if want := (recorderTally{Off: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("tally %+v, want %+v", got, want)
	}
}

func TestRecorderTallyCountsEveryRecorderThatConnected(t *testing.T) {
	got := tallyOf(recorderLog(t, "tok-a,tok-b"))
	if want := (recorderTally{Connected: 2}); !reflect.DeepEqual(got, want) {
		t.Errorf("tally %+v, want %+v", got, want)
	}
}

func TestRecorderTallyNamesARefusedTokenByItsPlaceInTheList(t *testing.T) {
	got := tallyOf(recorderLog(t, "tok-a,tok-bad,tok-c", "tok-bad"))
	if want := (recorderTally{Connected: 2, Failed: []int{2}}); !reflect.DeepEqual(got, want) {
		t.Errorf("tally %+v, want %+v", got, want)
	}
}

// A recorder that a cut short took offline logs out and back in, and logs
// that it connected a second time. The tally counts it once.
func TestRecorderTallyCountsARecorderThatLogsBackInOnce(t *testing.T) {
	logged := botLog(t)
	r := voice.Connect("tok-a", openRefusing(func(string) bool { return false }))
	defer r.Close()

	r.TakeOffline(r.UserIDs()[0])()

	var lines []string
	if !eventually(stubWait, func() bool {
		lines = logged()
		return linesSaying(lines, recorderConnectedLine) == 2
	}) {
		t.Fatalf("the recorder never logged back in:\n%s", strings.Join(lines, "\n"))
	}
	if got, want := tallyOf(lines), (recorderTally{Connected: 1}); !reflect.DeepEqual(got, want) {
		t.Errorf("tally %+v, want %+v", got, want)
	}
}

// refusedOnRelogin lets each token's first login through, and refuses each
// later login of a token in revoked, as Discord does once a recorder's token
// is reset while it is logged in.
func refusedOnRelogin(revoked ...string) func(token string) bool {
	var mu sync.Mutex
	tried := map[string]bool{}
	return func(token string) bool {
		mu.Lock()
		defer mu.Unlock()
		again := tried[token]
		tried[token] = true
		return again && slices.Contains(revoked, token)
	}
}

// reloginRefused runs the bot's recorder connect on raw, a RECORDER_TOKENS
// value, then takes the recorder at place offline as a cut short does, with
// Discord refusing its token as it logs back in. It returns the tally of what
// the bot logged once the tally has a recorder out.
func reloginRefused(t *testing.T, raw string, place int) recorderTally {
	t.Helper()
	logged := botLog(t)
	tokens := strings.Split(raw, ",")
	r := voice.Connect(raw, openRefusing(refusedOnRelogin(tokens[place-1])))
	defer r.Close()

	r.TakeOffline(r.UserIDs()[place-1])()
	return tallyOnceOut(t, logged)
}

// tallyOnceOut waits until the tally of what the bot has logged has a
// recorder out, and returns it.
func tallyOnceOut(t *testing.T, logged func() []string) recorderTally {
	t.Helper()
	var lines []string
	var got recorderTally
	if !eventually(stubWait, func() bool {
		lines = logged()
		got = tallyOf(lines)
		return len(got.Out) > 0
	}) {
		t.Fatalf("tally %+v never had a recorder out, from:\n%s", got, strings.Join(lines, "\n"))
	}
	return got
}

// A recorder Discord refuses as it logs back in is out until a restart, so
// no recording check can use it.
func TestRecorderTallyTakesOutARecorderDiscordRefusesAsItLogsBackIn(t *testing.T) {
	got := reloginRefused(t, "tok-a", 1)
	if got.Connected != 0 || !slices.Equal(got.Out, []int{1}) {
		t.Errorf("%d connected and %v out, want 0 connected and [1] out", got.Connected, got.Out)
	}
	line := got.line()
	if !strings.Contains(line, noRecordingCheck) {
		t.Errorf("recorders line %q does not say %q", line, noRecordingCheck)
	}
	if !strings.Contains(line, "recorder 1") || !strings.Contains(line, outBecauseRefused) {
		t.Errorf("recorders line %q does not name recorder 1 as %q", line, outBecauseRefused)
	}
}

// Discord refusing one of two recorders as it logs back in leaves the other
// counted.
func TestRecorderTallyKeepsCountingTheRecorderDiscordDidNotRefuse(t *testing.T) {
	got := reloginRefused(t, "tok-a,tok-b", 2)
	if got.Connected != 1 || !slices.Equal(got.Out, []int{2}) {
		t.Errorf("%d connected and %v out, want 1 connected and [2] out", got.Connected, got.Out)
	}
}

// A recorder whose first login failed was never counted, so Discord
// refusing its token at a later try leaves the count as it was.
func TestRecorderTallyLeavesTheCountWhenDiscordRefusesARecorderNeverCounted(t *testing.T) {
	logged := botLog(t)
	r := voice.Connect("tok-a,late-b", lateLogins(openRefusing(func(token string) bool { return token == "late-b" })))
	defer r.Close()

	got := tallyOnceOut(t, logged)
	if got.Connected != 1 || !slices.Equal(got.Out, []int{2}) {
		t.Errorf("%d connected and %v out, want 1 connected and [2] out", got.Connected, got.Out)
	}
}

// eventually reports whether done holds within d, trying it every 50 ms.
func eventually(d time.Duration, done func() bool) bool {
	for deadline := time.Now().Add(d); !done(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			return false
		}
	}
	return true
}

// linesSaying counts the lines that say msg.
func linesSaying(lines []string, msg string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, msg) {
			n++
		}
	}
	return n
}

// Encoder lists as ffmpeg -hide_banner -encoders prints them, cut to the
// legend and the Opus encoders. The native opus encoder is in every build,
// and libopus only in one built with it.
const (
	encodersWithoutLibopus = `Encoders:
 V..... = Video
 A..... = Audio
 S..... = Subtitle
 .F.... = Frame-level multithreading
 ..S... = Slice-level multithreading
 ...X.. = Codec is experimental
 ....B. = Supports draw_horiz_band
 .....D = Supports direct rendering method 1
 ------
 V....D a64multi             Multicolor charset for Commodore 64 (codec a64_multi)
 A..X.D opus                 Opus`
	encodersWithLibopus = encodersWithoutLibopus + `
 A....D libopus              libopus Opus (codec opus)`
)

// fakeFFmpeg returns a directory holding an ffmpeg that prints encoders
// when asked for its encoders, or exits 1 when encoders is empty. It uses
// only shell builtins, so it runs whatever PATH it gets.
func fakeFFmpeg(t *testing.T, encoders string) string {
	t.Helper()
	dir := t.TempDir()
	answer := "exit 1"
	if encoders != "" {
		answer = "printf '%s\\n' '" + encoders + "'; exit 0"
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = -encoders ]; then " + answer + "; fi\ndone\nexit 1\n"
	if err := os.WriteFile(dir+"/ffmpeg", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMixCheckFindsNoFFmpegOffTheBotsPath(t *testing.T) {
	t.Setenv("PATH", fakeFFmpeg(t, encodersWithLibopus))
	if got := checkMix(context.Background(), []string{"PATH=" + t.TempDir()}); got != mixNoFFmpeg {
		t.Errorf("verdict %q, want %q", got, mixNoFFmpeg)
	}
}

func TestMixCheckPassesAnFFmpegWithLibopusOnTheBotsPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := checkMix(context.Background(), []string{"PATH=" + fakeFFmpeg(t, encodersWithLibopus)}); got != mixReady {
		t.Errorf("verdict %q, want %q", got, mixReady)
	}
}

func TestMixCheckFailsAnFFmpegBuiltWithoutLibopus(t *testing.T) {
	if got := checkMix(context.Background(), []string{"PATH=" + fakeFFmpeg(t, encodersWithoutLibopus)}); got != mixNoLibopus {
		t.Errorf("verdict %q, want %q", got, mixNoLibopus)
	}
}

func TestMixCheckSaysAnFFmpegThatFailsDoesNotRun(t *testing.T) {
	if got := checkMix(context.Background(), []string{"PATH=" + fakeFFmpeg(t, "")}); got != mixFFmpegFails {
		t.Errorf("verdict %q, want %q", got, mixFFmpegFails)
	}
}

func TestMixCheckReadsThePathAnEnvFlagGivesTheBot(t *testing.T) {
	shell, flag := "PATH="+t.TempDir(), "PATH="+fakeFFmpeg(t, encodersWithLibopus)
	if got := checkMix(context.Background(), botEnv([]string{shell}, nil, nil, []string{flag})); got != mixReady {
		t.Errorf("verdict %q, want %q", got, mixReady)
	}
}
