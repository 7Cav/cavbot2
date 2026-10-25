//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
// each token refused picks, at its gateway and its REST API alike.
type refusingDiscord struct {
	refused func(token string) bool
}

func (d refusingDiscord) Open(recorder int, token string) (voice.GatewaySession, error) {
	if d.refused(token) {
		return nil, errors.New("websocket: close 4004: Authentication failed.")
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
	return r.tally
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

	lines := logged()
	for deadline := time.Now().Add(10 * time.Second); linesSaying(lines, recorderConnectedLine) < 2; lines = logged() {
		if time.Now().After(deadline) {
			t.Fatalf("the recorder never logged back in:\n%s", strings.Join(lines, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, want := tallyOf(lines), (recorderTally{Connected: 1}); !reflect.DeepEqual(got, want) {
		t.Errorf("tally %+v, want %+v", got, want)
	}
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
