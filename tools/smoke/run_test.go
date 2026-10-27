//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
)

// stubBotScenario, set in a process's environment, makes this test binary the
// stub bot instead of running the tests. Its value is what the stub does.
const stubBotScenario = "SMOKE_STUB_BOT"

// What the stub bot does once its recorders have connected.
const (
	// stubReady says its panel is listening, then that it is running.
	stubReady = "ready"
	// stubNoPanel says it is running, with no word from its panel.
	stubNoPanel = "no-panel"
	// stubCrash panics with stubCrashMessage before it is running.
	stubCrash = "crash"
	// stubLateLogin says what stubReady does, then stays up until SIGTERM,
	// while each recorder whose first login failed logs in at a later try.
	stubLateLogin = "late-login"
)

// stubWait bounds how long a test waits on the stub bot.
const stubWait = 30 * time.Second

const stubCrashMessage = "stub bot: start failed"

// logLevels are the LOG_LEVEL values the bot reads, lowest first.
var logLevels = []string{"DEBUG", "INFO", "WARN", "ERROR"}

func TestMain(m *testing.M) {
	if scenario := os.Getenv(stubBotScenario); scenario != "" {
		runStubBot(scenario)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runStubBot starts the way the bot does, as far as the supervisor reads it.
// It sets up the bot's logger at LOG_LEVEL and logs a line of its own at each
// level. It connects its recorders through voice.Connect, with Discord
// refusing each token that has "refused" in it, and failing the first login
// of each that starts with "late". Then, as scenario says, it logs the
// panel's line and the line that says the bot is running, and exits.
func runStubBot(scenario string) {
	utils.InitLogger(os.Getenv("LOG_LEVEL"))
	utils.Debug("Stub bot line")
	utils.Info("Stub bot line")
	utils.Warn("Stub bot line")
	utils.Error("Stub bot line")
	refused := func(token string) bool { return strings.Contains(token, "refused") }
	recorders := voice.Connect(os.Getenv("RECORDER_TOKENS"), lateLogins(openRefusing(refused)))
	defer recorders.Close()
	if scenario == stubCrash {
		panic(stubCrashMessage)
	}
	if scenario == stubReady || scenario == stubLateLogin {
		utils.Info("Panel listening", "addr", panelAddr)
	}
	utils.Info("Bot is now running. Press CTRL-C to exit")
	if scenario == stubLateLogin {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		<-stop
	}
}

// lateDiscord fails the first login of each token that starts with "late",
// though Discord reads its user ID, so its recorder logs in at a later try.
// It hands every other call to Opener.
type lateDiscord struct {
	voice.Opener

	mu    sync.Mutex
	tried map[string]bool
}

func lateLogins(d voice.Opener) voice.Opener {
	return &lateDiscord{Opener: d, tried: map[string]bool{}}
}

func (d *lateDiscord) Open(recorder int, token string) (voice.GatewaySession, error) {
	d.mu.Lock()
	first := !d.tried[token]
	d.tried[token] = true
	d.mu.Unlock()
	if first && strings.HasPrefix(token, "late") {
		return nil, errors.New("websocket: close 4000: Unknown error.")
	}
	return d.Opener.Open(recorder, token)
}

func (d *lateDiscord) UserID(ctx context.Context, token string) (string, error) {
	if strings.HasPrefix(token, "late") {
		return "2000", nil
	}
	return d.Opener.UserID(ctx, token)
}

// newStubSupervisor is a supervisor whose bot binary is this test binary.
func newStubSupervisor(t *testing.T) *supervisor {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := &supervisor{paths: statePaths{t.TempDir()}}
	if err := os.Symlink(exe, s.paths.binary()); err != nil {
		t.Fatal(err)
	}
	return s
}

// stubEnv makes a bot the supervisor launches the stub doing scenario.
func stubEnv(scenario string) []string {
	// Without atexit_sleep_ms=0, a stub built with -race waits a second as it
	// exits.
	return []string{stubBotScenario + "=" + scenario, "GORACE=atexit_sleep_ms=0"}
}

// launchStub runs the supervisor's bot launch on the stub bot doing scenario,
// with env as a run builds it, and waits until the bot's output is all in the
// bot log. It returns the supervisor, for its bot log and recorder tally,
// whether the bot got ready, and what the supervisor printed.
func launchStub(t *testing.T, scenario string, env []string) (s *supervisor, ready bool, printed string) {
	t.Helper()
	s = newStubSupervisor(t)
	out, err := os.Create(filepath.Join(t.TempDir(), "supervisor.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	prevStdout := os.Stdout
	os.Stdout = out
	readyCh, err := s.startBot(append(stubEnv(scenario), env...))
	if err == nil {
		select {
		case <-s.logDone:
		case <-time.After(stubWait):
			_ = s.bot.Process.Kill()
			err = fmt.Errorf("the stub bot's output did not end within %s", stubWait)
		}
		<-s.botDone
	}
	os.Stdout = prevStdout
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-readyCh:
		ready = true
	default:
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	return s, ready, string(b)
}

// superviseStub launches the stub bot doing scenario, with env as a run
// builds it, and watches it as the supervisor does once the bot has
// started, until the test ends and stops the run. It returns the
// supervisor, for the run file its watch keeps.
func superviseStub(t *testing.T, scenario string, env []string) *supervisor {
	t.Helper()
	s := newStubSupervisor(t)
	ready, err := s.startBot(append(stubEnv(scenario), env...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		_ = s.watchBot(ctx, ready)
	}()
	t.Cleanup(func() {
		cancel()
		<-watched
		s.teardown()
	})
	return s
}

func TestUpGetsReadyAtAQuietLogLevel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		file  map[string]string
		flags []string
	}{
		{"WARN from the env file", map[string]string{"LOG_LEVEL": "WARN"}, nil},
		{"ERROR from --env over the env file's DEBUG", map[string]string{"LOG_LEVEL": "DEBUG"}, []string{"LOG_LEVEL=ERROR"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ready, _ := launchStub(t, stubReady, botEnv(nil, tc.file, nil, tc.flags)); !ready {
				t.Error("the bot never got ready")
			}
		})
	}
}

// loggedLevels are the levels of the logger's lines in the log at path,
// lowest first.
func loggedLevels(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[0], "time=") || !strings.HasPrefix(f[1], "level=") {
			continue
		}
		seen = append(seen, strings.TrimPrefix(f[1], "level="))
	}
	var levels []string
	for _, l := range logLevels {
		if slices.Contains(seen, l) {
			levels = append(levels, l)
		}
	}
	return levels
}

func TestBotLogKeepsOnlyTheLinesLogLevelAsksFor(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  []string
	}{
		{"DEBUG", []string{"DEBUG", "INFO", "WARN", "ERROR"}},
		{"INFO", []string{"INFO", "WARN", "ERROR"}},
		{"WARN", []string{"WARN", "ERROR"}},
		{"ERROR", []string{"ERROR"}},
	} {
		t.Run(tc.level, func(t *testing.T) {
			s, _, _ := launchStub(t, stubReady, []string{"LOG_LEVEL=" + tc.level})
			if got := loggedLevels(t, s.paths.botLog()); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("bot log has lines at %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRecorderTallyIsWholeAtEveryLogLevel(t *testing.T) {
	for _, level := range logLevels {
		for _, tc := range []struct {
			name   string
			tokens string
			want   recorderTally
		}{
			{"no token", "", recorderTally{Off: true}},
			{"second of three refused", "tok-a,refused-b,tok-c", recorderTally{Connected: 2, Failed: []int{2}}},
		} {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				s, _, _ := launchStub(t, stubReady, []string{"LOG_LEVEL=" + level, "RECORDER_TOKENS=" + tc.tokens})
				if got := s.report.Recorders; !reflect.DeepEqual(got, tc.want) {
					t.Errorf("tally %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

func TestPanelWarningOnlyWhenThePanelIsNotListening(t *testing.T) {
	for _, level := range []string{"WARN", "ERROR"} {
		t.Run(level+"/listening", func(t *testing.T) {
			_, ready, printed := launchStub(t, stubReady, []string{"LOG_LEVEL=" + level})
			if !ready {
				t.Fatal("the bot never got ready, so the panel check never ran")
			}
			if strings.Contains(printed, panelNotListening) {
				t.Errorf("warned the panel is not listening while it listens:\n%s", printed)
			}
		})
		t.Run(level+"/not listening", func(t *testing.T) {
			if _, _, printed := launchStub(t, stubNoPanel, []string{"LOG_LEVEL=" + level}); !strings.Contains(printed, panelNotListening) {
				t.Errorf("no warning that the panel is not listening:\n%s", printed)
			}
		})
	}
}

func TestBotThatExitsBeforeReadyLeavesItsPanicInTheBotLog(t *testing.T) {
	for _, level := range logLevels {
		t.Run(level, func(t *testing.T) {
			s, ready, _ := launchStub(t, stubCrash, []string{"LOG_LEVEL=" + level})
			if ready {
				t.Fatal("a bot that panicked before it was running got ready")
			}
			b, err := os.ReadFile(s.paths.botLog())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), stubCrashMessage) {
				t.Errorf("bot log lacks the panic %q:\n%s", stubCrashMessage, b)
			}
		})
	}
}

// A recorder whose first login failed is offline when the bot says it is
// running, and logs in at a later try. The run file, which status prints
// its recorders line from, counts it from then on, while the bot runs.
func TestRunFileCountsARecorderThatConnectsAfterTheBotIsRunning(t *testing.T) {
	s := superviseStub(t, stubLateLogin, []string{"RECORDER_TOKENS=late-a"})

	want := recorderTally{Connected: 1}
	var got recorderTally
	if !eventually(stubWait, func() bool {
		if st, ok := readRun(s.paths.runFile()); ok && st.Recording != nil {
			got = st.Recording.Recorders
		}
		return reflect.DeepEqual(got, want)
	}) {
		t.Errorf("run file's tally %+v after %s, want %+v", got, stubWait, want)
	}
}

// A recorder whose first login failed is offline when the bot says it is
// running, and Discord refuses its token at a later try. The run file, which
// status prints its recorders line from, stops counting it and holds it as
// out.
func TestRunFileHoldsARecorderDiscordRefusesAfterTheBotIsRunning(t *testing.T) {
	s := superviseStub(t, stubLateLogin, []string{"RECORDER_TOKENS=late-refused-a"})

	var got recorderTally
	if !eventually(stubWait, func() bool {
		if st, ok := readRun(s.paths.runFile()); ok && st.Recording != nil {
			got = st.Recording.Recorders
		}
		return len(got.Out) > 0
	}) {
		t.Fatalf("run file's tally %+v after %s has no recorder out", got, stubWait)
	}
	if got.Connected != 0 || !slices.Equal(got.Out, []int{1}) {
		t.Errorf("run file has %d connected and %v out, want 0 connected and [1] out", got.Connected, got.Out)
	}
}
