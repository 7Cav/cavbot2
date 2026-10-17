//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
)

const stubCrashMessage = "stub bot: start failed"

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
// refusing each token that starts with "refused". Then, as scenario says, it
// logs the panel's line and the line that says the bot is running, and exits.
func runStubBot(scenario string) {
	utils.InitLogger(os.Getenv("LOG_LEVEL"))
	utils.Debug("Stub bot line")
	utils.Info("Stub bot line")
	utils.Warn("Stub bot line")
	utils.Error("Stub bot line")
	open := func(recorder int, token string) (voice.GatewaySession, error) {
		if strings.HasPrefix(token, "refused") {
			return nil, errors.New("websocket: close 4004: Authentication failed.")
		}
		return fakeRecorder{id: strconv.Itoa(1000 + recorder)}, nil
	}
	voice.Connect(os.Getenv("RECORDER_TOKENS"), open).Close()
	if scenario == stubCrash {
		panic(stubCrashMessage)
	}
	if scenario == stubReady {
		utils.Info("Panel listening", "addr", panelAddr)
	}
	utils.Info("Bot is now running. Press CTRL-C to exit")
}

// launchStub runs the supervisor's bot launch on the stub bot doing scenario,
// with env as a run builds it, and waits until the bot's output is all in the
// bot log. It returns the supervisor, for its bot log and recorder tally,
// whether the bot got ready, and what the supervisor printed.
func launchStub(t *testing.T, scenario string, env []string) (s *supervisor, ready bool, printed string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s = &supervisor{paths: statePaths{t.TempDir()}}
	if err := os.Symlink(exe, s.paths.binary()); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(t.TempDir(), "supervisor.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	prevStdout := os.Stdout
	os.Stdout = out
	// Without atexit_sleep_ms=0, a stub built with -race waits a second as it
	// exits.
	stub := []string{stubBotScenario + "=" + scenario, "GORACE=atexit_sleep_ms=0"}
	readyCh, err := s.startBot(append(stub, env...))
	if err == nil {
		select {
		case <-s.logDone:
		case <-time.After(30 * time.Second):
			_ = s.bot.Process.Kill()
			err = errors.New("the stub bot's output did not end within 30 s")
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
	for _, l := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
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
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
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
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
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
