//go:build unix

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	stateStarting = "starting"
	stateReady    = "ready"
	stateStopped  = "stopped"
	stateFailed   = "failed"
)

// startupTimeout bounds how long up waits. A start registers every slash
// command in one request, which Discord holds for up to about 40 s when it
// is the third within a minute.
const startupTimeout = 5 * time.Minute

// runState is the run file: what a run is, where it came from, and how far it
// got. The supervisor writes it; every other command reads it.
type runState struct {
	State      string    `json:"state"`
	Detail     string    `json:"detail,omitempty"`
	Worktree   string    `json:"worktree"`
	Branch     string    `json:"branch"`
	Version    string    `json:"version,omitempty"`
	Forum      string    `json:"forum"`
	Started    time.Time `json:"started"`
	Supervisor int       `json:"supervisor_pid"`
	Bot        int       `json:"bot_pid,omitempty"`
	BotBinary  string    `json:"bot_binary,omitempty"`
	PanelURL   string    `json:"panel_url"`
	ForumURL   string    `json:"forum_url,omitempty"`
	ManagedDB  bool      `json:"managed_db"`
}

type statePaths struct{ dir string }

func (p statePaths) lock() string          { return filepath.Join(p.dir, "lock") }
func (p statePaths) runFile() string       { return filepath.Join(p.dir, "run.json") }
func (p statePaths) supervisorLog() string { return filepath.Join(p.dir, "supervisor.log") }
func (p statePaths) botLog() string        { return filepath.Join(p.dir, "bot.log") }
func (p statePaths) binary() string        { return filepath.Join(p.dir, "cavbot2") }

func readRun(path string) (runState, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return runState{}, false
	}
	var st runState
	if json.Unmarshal(b, &st) != nil {
		return runState{}, false
	}
	return st, true
}

func writeRun(path string, st runState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// tryLock takes the run lock without waiting; ok is false while another
// process holds it. The supervisor holds it for its whole life, and the
// kernel drops it when that process dies, so a crash never leaves a stale
// lock. One lock serves every worktree: one bot token is one gateway
// connection and one set of commands on the test guild.
func tryLock(path string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

// runHeld reports whether a supervisor holds the lock right now.
func runHeld(p statePaths) (bool, error) {
	f, ok, err := tryLock(p.lock())
	if err != nil {
		return false, err
	}
	if ok {
		_ = f.Close()
	}
	return !ok, nil
}

type envFlags []string

func (e *envFlags) String() string { return strings.Join(*e, " ") }

func (e *envFlags) Set(v string) error {
	if k, _, ok := strings.Cut(v, "="); !ok || k == "" {
		return fmt.Errorf("takes KEY=VALUE, got %q", v)
	}
	*e = append(*e, v)
	return nil
}

type upOptions struct {
	forum      string
	extra      envFlags
	freshDB    bool
	envFile    string
	allowLarge bool
}

func upFlags(name string, o *upOptions) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&o.forum, "forum", forumFake, "panel sign-in: fake (local personas, no person needed) or real (the 7cav.us forum)")
	fs.Var(&o.extra, "env", "`KEY=VALUE` for the bot, repeatable; wins over the env file and the smoke defaults. For non-secret values: it shows in ps")
	fs.BoolVar(&o.freshDB, "fresh-db", false, "start from an empty bot database")
	fs.StringVar(&o.envFile, "env-file", "", "env file holding the test guild's credentials (default: .env at the main checkout's root)")
	fs.BoolVar(&o.allowLarge, "allow-large-guild", false, "run on a guild with more than 100 members")
	return fs
}

// args are the flags again, for the supervisor's command line.
func (o upOptions) args() []string {
	a := []string{"--forum", o.forum}
	for _, kv := range o.extra {
		a = append(a, "--env", kv)
	}
	if o.freshDB {
		a = append(a, "--fresh-db")
	}
	if o.envFile != "" {
		a = append(a, "--env-file", o.envFile)
	}
	if o.allowLarge {
		a = append(a, "--allow-large-guild")
	}
	return a
}

// cmdUp starts a supervisor in its own session, so the run outlives this
// command and the shell that ran it, then relays its progress until the bot
// is ready or the start fails.
func cmdUp(args []string) error {
	var o upOptions
	fs := upFlags("up", &o)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: go run ./tools/smoke up [flags]")
		_, _ = fmt.Fprintln(fs.Output(), "Builds this checkout's bot and runs it on the test guild with the panel on "+panelBaseURL+". Returns once it is ready; the run keeps going until down.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return errUsage
	}
	if o.forum != forumFake && o.forum != forumReal {
		return fmt.Errorf("--forum is fake or real, not %q", o.forum)
	}
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	p := statePaths{g.stateDir()}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return err
	}
	held, err := runHeld(p)
	if err != nil {
		return err
	}
	if held {
		st, _ := readRun(p.runFile())
		return fmt.Errorf("a smoke run is already up from %s (%s) since %s; stop it with `go run ./tools/smoke down`",
			st.Worktree, st.Branch, st.Started.Local().Format(time.Kitchen))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.Create(p.supervisorLog())
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, append([]string{"supervise"}, o.args()...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	return followStartup(p, cmd.Process.Pid, exited)
}

// followStartup prints the supervisor's log as it grows until the run file
// says this supervisor's bot is ready, or the supervisor exits first.
func followStartup(p statePaths, pid int, exited <-chan struct{}) error {
	var offset int64
	deadline := time.Now().Add(startupTimeout)
	for {
		offset = relay(p.supervisorLog(), offset)
		if st, ok := readRun(p.runFile()); ok && st.Supervisor == pid && st.State == stateReady {
			relay(p.supervisorLog(), offset)
			return nil
		}
		select {
		case <-exited:
			relay(p.supervisorLog(), offset)
			return errSilent
		case <-time.After(300 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still starting after %s; it keeps going, and `go run ./tools/smoke status` and `logs` show where it is", startupTimeout)
		}
	}
}

// relay prints what the file gained since offset and returns the new offset.
func relay(path string, offset int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	n, _ := io.Copy(os.Stdout, f)
	return offset + n
}

// supervisor owns one run: it holds the lock, starts Postgres, the fake
// forum and the bot, and stops them all on SIGTERM, SIGINT or the bot's exit.
type supervisor struct {
	opts  upOptions
	git   gitPaths
	paths statePaths
	st    runState

	forum *http.Server
	bot   *exec.Cmd
	// botDone yields the bot's exit once; botExited records that it has.
	botDone   chan error
	botExited bool
	// logDone closes once the bot's output is all in the bot log.
	logDone chan struct{}
}

func (s *supervisor) logf(format string, args ...any) {
	fmt.Printf("smoke: "+format+"\n", args...)
}

func (s *supervisor) save() {
	if err := writeRun(s.paths.runFile(), s.st); err != nil {
		s.logf("could not write the run file: %v", err)
	}
}

func cmdSupervise(args []string) error {
	var o upOptions
	if err := upFlags("supervise", &o).Parse(args); err != nil {
		return err
	}
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	p := statePaths{g.stateDir()}
	lock, ok, err := tryLock(p.lock())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("another smoke run holds the lock")
	}
	defer func() { _ = lock.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	s := &supervisor{opts: o, git: g, paths: p}
	return s.run(ctx)
}

func (s *supervisor) run(ctx context.Context) error {
	prev, hadPrev := readRun(s.paths.runFile())
	branch, _ := gitOutput(s.git.worktree, "rev-parse", "--abbrev-ref", "HEAD")
	s.st = runState{
		State: stateStarting, Worktree: s.git.worktree, Branch: branch, Forum: s.opts.forum,
		Started: time.Now().UTC(), Supervisor: os.Getpid(), PanelURL: panelBaseURL,
	}
	if s.opts.forum == forumFake {
		s.st.ForumURL = forumBrowserURL
	}
	s.save()
	if hadPrev {
		stopLeftoverBot(prev, s.logf)
	}

	ready, err := s.start(ctx)
	if err != nil {
		s.st.State, s.st.Detail = stateFailed, err.Error()
		s.logf("start failed: %v", err)
		s.teardown()
		s.save()
		return errSilent
	}
	if ready != nil {
		select {
		case <-ready:
			s.announce()
			s.st.State = stateReady
			s.save()
		case err := <-s.botDone:
			s.botExited = true
			s.st.State, s.st.Detail = stateFailed, fmt.Sprintf("the bot exited during startup (%v)", exitText(err))
			s.logf("%s. The end of its log:", s.st.Detail)
			select {
			case <-s.logDone:
			case <-time.After(2 * time.Second):
			}
			printTail(os.Stdout, s.paths.botLog(), 30)
			s.teardown()
			s.save()
			return errSilent
		case <-ctx.Done():
		}
	}
	if s.st.State == stateReady {
		select {
		case err := <-s.botDone:
			s.botExited = true
			s.st.Detail = fmt.Sprintf("the bot exited on its own (%v)", exitText(err))
			s.logf("%s; stopping the run", s.st.Detail)
		case <-ctx.Done():
		}
	}
	s.logf("stopping")
	s.teardown()
	if s.st.State != stateFailed {
		s.st.State = stateStopped
	}
	s.save()
	s.logf("stopped")
	return nil
}

// start runs every step up to the bot's launch and returns a channel that
// closes when the bot is ready. A nil channel and nil error mean a stop
// arrived before the bot started.
func (s *supervisor) start(ctx context.Context) (<-chan struct{}, error) {
	envPath := s.opts.envFile
	if envPath == "" {
		envPath = s.git.defaultEnvFile()
	}
	vals, err := readEnvFile(envPath)
	if err != nil {
		return nil, err
	}
	effective := func(key string) string {
		if v, ok := extraValue(s.opts.extra, key); ok {
			return v
		}
		return vals[key]
	}
	required := requiredKeys
	if s.opts.forum == forumReal {
		required = append(slices.Clone(required), requiredRealKeys...)
	}
	var missing []string
	for _, k := range required {
		if effective(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s has no value for %s", envPath, strings.Join(missing, ", "))
	}
	s.logf("env file %s, %d variables (values not shown)", envPath, len(vals))

	summary, err := checkTestGuild(newDiscordClient(effective("DISCORD_TOKEN")), effective("GUILD_ID"), s.opts.allowLarge)
	if err != nil {
		return nil, err
	}
	s.logf("test guild: %s", summary)

	if err := checkPortFree(panelAddr, "the panel"); err != nil {
		return nil, err
	}
	if s.opts.forum == forumFake {
		if err := checkPortFree(forumAddr, "the fake forum"); err != nil {
			return nil, err
		}
	}

	dsn, ownDSN := extraValue(s.opts.extra, "BOT_DB_DSN")
	if !ownDSN {
		if err := ensurePostgres(ctx, s.opts.freshDB, s.logf); err != nil {
			return nil, err
		}
		dsn = pgDSN
		s.st.ManagedDB = true
		s.save()
	}

	version := "smoke-" + shortCommit(s.git.worktree)
	s.logf("building %s (%s) from %s", s.st.Branch, version, s.git.worktree)
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X main.Version="+version, "-o", s.paths.binary(), ".")
	build.Dir = s.git.worktree
	build.Stdout, build.Stderr = os.Stdout, os.Stdout
	if err := build.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, nil
		}
		return nil, fmt.Errorf("build: %w", err)
	}
	s.st.Version, s.st.BotBinary = version, s.paths.binary()

	if s.opts.forum == forumFake {
		ln, err := net.Listen("tcp", forumAddr)
		if err != nil {
			return nil, fmt.Errorf("fake forum: %w", err)
		}
		s.forum = &http.Server{Handler: newFakeForum(panelBaseURL+"/auth/callback", s.logf).handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = s.forum.Serve(ln) }()
		s.logf("fake forum listening on %s", forumBrowserURL)
	}
	if ctx.Err() != nil {
		return nil, nil
	}
	return s.startBot(botEnv(os.Environ(), vals, smokeOverrides(s.opts.forum, dsn), s.opts.extra))
}

// startBot launches the built bot in the state directory, which has no .env,
// so the environment is its whole configuration. Its output goes to the bot
// log, and the returned channel closes at "Bot is now running".
func (s *supervisor) startBot(env []string) (<-chan struct{}, error) {
	logFile, err := os.Create(s.paths.botLog())
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	cmd := exec.Command(s.paths.binary())
	cmd.Dir = s.paths.dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_ = logFile.Close()
		return nil, err
	}
	_ = pw.Close()
	s.bot = cmd
	s.botDone = make(chan error, 1)
	go func() { s.botDone <- cmd.Wait() }()
	s.st.Bot = cmd.Process.Pid
	s.save()
	s.logf("bot started (pid %d); after quick restarts, registering commands can take up to about 40 s", cmd.Process.Pid)

	ready := make(chan struct{})
	s.logDone = make(chan struct{})
	go func() {
		defer close(s.logDone)
		defer func() { _ = logFile.Close() }()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		panel, closed := false, false
		for sc.Scan() {
			line := sc.Text()
			_, _ = fmt.Fprintln(logFile, line)
			switch {
			case strings.Contains(line, "Panel listening"):
				panel = true
			case strings.Contains(line, "Bot is now running") && !closed:
				if !panel {
					s.logf("the bot is running but its panel is not listening; see %s", s.paths.botLog())
				}
				closed = true
				close(ready)
			}
		}
		_, _ = io.Copy(logFile, pr)
	}()
	return ready, nil
}

func (s *supervisor) announce() {
	s.logf("ready: %s on the test guild", s.st.Version)
	s.logf("  panel       %s", panelBaseURL)
	if s.opts.forum == forumFake {
		s.logf("  sign-in     fake forum at %s, pick Panel admin, Foxhole manager or No access", forumBrowserURL)
	} else {
		s.logf("  sign-in     the 7cav.us forum")
	}
	s.logf("  bot log     %s", s.paths.botLog())
	s.logf("  stop with   go run ./tools/smoke down")
}

// teardown stops whatever start got running: the bot with SIGTERM and a
// grace period, the fake forum, and the smoke Postgres, whose volume stays.
func (s *supervisor) teardown() {
	if s.bot != nil && !s.botExited {
		_ = s.bot.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.botDone:
		case <-time.After(20 * time.Second):
			s.logf("the bot ignored SIGTERM for 20 s; killing it")
			_ = s.bot.Process.Kill()
			<-s.botDone
		}
		s.botExited = true
	}
	if s.forum != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.forum.Shutdown(ctx)
		cancel()
	}
	if s.st.ManagedDB {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := docker(ctx, "stop", pgContainer); err != nil {
			s.logf("could not stop %s: %v", pgContainer, err)
		}
		cancel()
	}
}

// stopLeftoverBot stops a bot an earlier run started and lost, as when its
// supervisor was killed outright. It acts only on a live process still
// running the smoke binary, so a reused PID is left alone.
func stopLeftoverBot(prev runState, logf func(string, ...any)) {
	if prev.Bot <= 0 || prev.BotBinary == "" || syscall.Kill(prev.Bot, 0) != nil {
		return
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(prev.Bot)).Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), prev.BotBinary) {
		return
	}
	logf("stopping a bot an earlier run left behind (pid %d)", prev.Bot)
	_ = syscall.Kill(prev.Bot, syscall.SIGTERM)
	for range 40 {
		if syscall.Kill(prev.Bot, 0) != nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = syscall.Kill(prev.Bot, syscall.SIGKILL)
}

// checkPortFree refuses an address something already listens on. lsof comes
// first: on macOS a listener on all interfaces does not stop a bind to
// 127.0.0.1, and the browser could reach either one.
func checkPortFree(addr, what string) error {
	_, port, _ := net.SplitHostPort(addr)
	if holder := portHolder(port); holder != "" {
		return fmt.Errorf("%s needs port %s, which %s already listens on", what, port, holder)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s needs %s: %w", what, addr, err)
	}
	return ln.Close()
}

// portHolder names the process listening on the TCP port, or "" when lsof
// finds none or is not installed.
func portHolder(port string) string {
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil {
		return ""
	}
	var pid, command string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p") && pid == "":
			pid = line[1:]
		case strings.HasPrefix(line, "c") && command == "":
			command = line[1:]
		}
	}
	if pid == "" {
		return ""
	}
	return fmt.Sprintf("%s (pid %s)", command, pid)
}

func ensurePostgres(ctx context.Context, fresh bool, logf func(string, ...any)) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is not on PATH: start Docker, or pass --env BOT_DB_DSN=<a Postgres of your own>")
	}
	if fresh {
		logf("removing the smoke database for --fresh-db")
		_ = docker(ctx, "rm", "-f", pgContainer)
		_ = docker(ctx, "volume", "rm", pgVolume)
	}
	running, err := dockerOutput(ctx, "inspect", "-f", "{{.State.Running}}", pgContainer)
	switch {
	case err != nil:
		logf("starting Postgres %s on 127.0.0.1:%s", pgContainer, pgPort)
		if err := docker(ctx, "run", "-d", "--name", pgContainer, "-e", "POSTGRES_PASSWORD=smoke",
			"-p", "127.0.0.1:"+pgPort+":5432", "-v", pgVolume+":/var/lib/postgresql", pgImage); err != nil {
			return err
		}
	case running == "false":
		logf("starting the stopped Postgres %s", pgContainer)
		if err := docker(ctx, "start", pgContainer); err != nil {
			return err
		}
	default:
		logf("reusing the running Postgres %s", pgContainer)
	}
	// Over TCP, not the socket: the image's first-boot init runs a server
	// that listens on the socket alone, then restarts.
	for range 60 {
		if docker(ctx, "exec", pgContainer, "pg_isready", "-h", "127.0.0.1", "-U", "postgres") == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("postgres %s was not ready after 60 s", pgContainer)
}

func docker(ctx context.Context, args ...string) error {
	_, err := dockerOutput(ctx, args...)
	return err
}

func dockerOutput(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// shortCommit is HEAD's short hash, marked dirty when the worktree has
// uncommitted changes, so a report can say which build it tested.
func shortCommit(worktree string) string {
	sha, err := gitOutput(worktree, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "unknown"
	}
	if status, err := gitOutput(worktree, "status", "--porcelain"); err == nil && status != "" {
		sha += "-dirty"
	}
	return sha
}

func exitText(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

func printTail(w io.Writer, path string, n int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, l)
	}
}

func cmdDown(args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	p := statePaths{g.stateDir()}
	st, hasRun := readRun(p.runFile())
	held, err := runHeld(p)
	if err != nil {
		return err
	}
	if !held {
		if hasRun {
			stopLeftoverBot(st, func(f string, a ...any) { fmt.Printf("smoke: "+f+"\n", a...) })
		}
		fmt.Println("no smoke run is up")
		return nil
	}
	if !hasRun || st.Supervisor <= 0 {
		return fmt.Errorf("a run holds the lock but left no run file; see %s", p.supervisorLog())
	}
	fmt.Printf("stopping the run from %s (%s)\n", st.Worktree, st.Branch)
	offset := fileSize(p.supervisorLog())
	if err := syscall.Kill(st.Supervisor, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal the supervisor (pid %d): %w", st.Supervisor, err)
	}
	for range 120 {
		time.Sleep(500 * time.Millisecond)
		offset = relay(p.supervisorLog(), offset)
		if held, _ := runHeld(p); !held {
			return nil
		}
	}
	_ = syscall.Kill(st.Supervisor, syscall.SIGKILL)
	if st.Bot > 0 {
		_ = syscall.Kill(st.Bot, syscall.SIGKILL)
	}
	return fmt.Errorf("the run did not stop within 60 s, so its supervisor and bot were killed; %s may still be running", pgContainer)
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func cmdStatus(args []string) error {
	if len(args) > 0 {
		return errUsage
	}
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	p := statePaths{g.stateDir()}
	st, hasRun := readRun(p.runFile())
	held, err := runHeld(p)
	if err != nil {
		return err
	}
	if !held {
		fmt.Println("no smoke run is up")
		if hasRun {
			fmt.Printf("last run: %s, from %s (%s), started %s\n", st.State, st.Worktree, st.Branch, st.Started.Local().Format(time.DateTime))
			if st.Detail != "" {
				fmt.Printf("  %s\n", st.Detail)
			}
			fmt.Printf("  bot log %s\n", p.botLog())
		}
		return nil
	}
	fmt.Printf("%s: %s from %s (%s), forum %s, started %s\n", st.State, st.Version, st.Worktree, st.Branch, st.Forum, st.Started.Local().Format(time.DateTime))
	fmt.Printf("  panel          %s\n", st.PanelURL)
	if st.ForumURL != "" {
		fmt.Printf("  fake forum     %s\n", st.ForumURL)
	}
	fmt.Printf("  bot log        %s\n", p.botLog())
	fmt.Printf("  supervisor log %s\n", p.supervisorLog())
	return nil
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	n := fs.Int("n", 40, "lines to print")
	sup := fs.Bool("supervisor", false, "print the supervisor's log instead of the bot's")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	p := statePaths{g.stateDir()}
	path := p.botLog()
	if *sup {
		path = p.supervisorLog()
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no log yet at %s", path)
	}
	printTail(os.Stdout, path, *n)
	return nil
}

func cmdForum(args []string) error {
	if len(args) != 1 || !slices.Contains(forumModes, args[0]) {
		fmt.Fprintf(os.Stderr, "usage: go run ./tools/smoke forum %s\n", strings.Join(forumModes, "|"))
		fmt.Fprintln(os.Stderr, "ok signs people in; expired makes /api/me answer 401, refused 403, and down drops the token and /api/me requests.")
		return errUsage
	}
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	p := statePaths{g.stateDir()}
	st, _ := readRun(p.runFile())
	if held, _ := runHeld(p); !held || st.Forum != forumFake {
		return fmt.Errorf("no run with the fake forum is up")
	}
	res, err := http.PostForm(forumServerURL+"/smoke/mode", url.Values{"mode": {args[0]}})
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("fake forum answered %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	fmt.Printf("fake forum mode: %s", body)
	return nil
}
