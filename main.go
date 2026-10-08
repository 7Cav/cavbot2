package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/7cav/cavbot2/utils"

	_ "github.com/go-sql-driver/mysql"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/panel"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

var Version = "dev"

var (
	Token    string
	GuildID  string
	LogLevel string
	BMToken  string
)

func init() {
	// Load .env before the reads below so `go run .` works from a filled-in
	// .env alone. godotenv.Load never overwrites a variable already in the
	// environment, so Docker and CI — which inject env directly and ship no
	// .env — behave exactly as they did before. A missing file is the normal
	// case there and not an error; anything else means the file exists but
	// could not be read, which is worth failing on rather than starting with
	// half the configuration silently missing.
	//
	// This must stay in init(), not main(): the panics below run first.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		panic(fmt.Sprintf("Found .env but could not load it: %v", err))
	}

	Token = os.Getenv("DISCORD_TOKEN")
	GuildID = os.Getenv("GUILD_ID")
	LogLevel = os.Getenv("LOG_LEVEL")
	BMToken = os.Getenv("BM_TOKEN")

	if Token == "" {
		panic("No token provided. Please set DISCORD_TOKEN environment variable")
	}
	if GuildID == "" {
		panic("No GuildID provided. Please set GUILD_ID environment variable")
	}
	if BMToken == "" {
		panic("No BM_TOKEN provided. Please set BM_TOKEN environment variable")
	}
	if LogLevel == "" {
		LogLevel = "default"
	}

	utils.InitLogger(LogLevel)
}

func initLOACache() {
	dsn := os.Getenv("FORUM_DB_DSN")
	if dsn == "" {
		utils.Warn("FORUM_DB_DSN not set, LOA cache disabled")
		return
	}

	nodeIDs := []int{180}
	if s := os.Getenv("LOA_NODE_IDS"); s != "" {
		nodeIDs = nil
		for _, part := range strings.Split(s, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				nodeIDs = append(nodeIDs, id)
			}
		}
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		utils.Warn("Failed to open forum DB connection, LOA cache disabled", "error", err)
		return
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxIdleTime(30 * time.Second)

	utils.GlobalLOACache.Refresh(db, nodeIDs)

	go func() {
		defer utils.RecoverPanic("loa-refresh")
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			utils.GlobalLOACache.Refresh(db, nodeIDs)
		}
	}()
}

// initBotStore opens the bot's own Postgres database and runs its migrations,
// or returns nil when BOT_DB_DSN is unset so the feature stays inert and the
// bot runs as it did before the store existed. A configured database that
// cannot be reached or migrated stops the bot here, before the Discord
// session opens: with restart: unless-stopped the container retries, and a
// binary never runs against a schema it does not understand. The
// DSN carries a password and is never logged.
func initBotStore() *store.Postgres {
	dsn := os.Getenv("BOT_DB_DSN")
	if dsn == "" {
		utils.Warn("BOT_DB_DSN not set, bot store disabled")
		return nil
	}
	utils.Info("Bot store configured")

	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		panic(fmt.Sprintf("Bot store unavailable: %v", err))
	}
	return s
}

// initPanelConfig reads the PANEL_* variables, or returns a disabled config
// when PANEL_ADDR is unset so the feature stays inert. It runs before the
// Discord session opens so a half-filled .env stops the bot before it is on
// the gateway. The hub page reads the store on every load, so with no store
// the panel is disabled too: one WARN, no listener, as the spec has it for a
// host with no BOT_DB_DSN.
func initPanelConfig(storeConfigured bool) panel.Config {
	cfg, err := panel.ConfigFromEnv()
	if err != nil {
		panic(fmt.Sprintf("Panel misconfigured: %v", err))
	}
	if !cfg.Enabled() {
		utils.Warn("PANEL_ADDR not set, panel disabled")
		return cfg
	}
	if !storeConfigured {
		utils.Warn("BOT_DB_DSN not set, panel disabled")
		return panel.Config{}
	}
	return cfg
}

// initPanel builds the panel over the store, the runtime and the Discord
// session, or returns nil when the config is disabled. It runs before the
// Discord session opens so a broken template stops the bot before it is on
// the gateway; the listener itself starts after READY. A configured panel
// that cannot be built stops the bot, the same rule the store follows.
func initPanel(cfg panel.Config, deps panel.Deps) *panel.Panel {
	if !cfg.Enabled() {
		return nil
	}
	p, err := panel.New(cfg, Version, deps)
	if err != nil {
		panic(fmt.Sprintf("Panel unavailable: %v", err))
	}
	return p
}

func main() {
	// Signal handling comes first, so a stop at any point during startup
	// shuts down like one after it (#470): main returns and the deferred
	// steps run. Installing it also turns SIGINT back on when a
	// non-interactive shell started the bot with it ignored.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// The first signal starts the shutdown. A second one gets the default
	// action and ends the process, for a step that hangs.
	context.AfterFunc(ctx, stop)
	// A startup step below that fails panics. The watch sends that failed start
	// to Sentry and lets the panic go on (#473).
	start := utils.NewStartWatch(time.Now)
	// shuttingDown reports a stop that arrived while a startup step ran, and
	// logs "Shutting down" when one did. A step such as a migration finishes
	// first, and startup goes no further. A panic in the shutdown that follows
	// is no failed start, so the watch hears of the stop.
	shuttingDown := func() bool {
		if ctx.Err() == nil {
			return false
		}
		utils.Info("Shutting down")
		start.Stopping()
		return true
	}

	defer utils.InitSentry(Version)()
	// Deferred after InitSentry, so it runs while the client is still live.
	defer start.ReportFailure()

	utils.Info("CavBot2 starting", "version", Version)

	commands.LogFoxholeRoleBaseName()

	initLOACache()
	if shuttingDown() {
		return
	}

	// Database and migrations come before the Discord session opens, so a
	// failed migration never leaves a half-started bot on the gateway.
	botStore := initBotStore()
	if botStore != nil {
		defer func() { _ = botStore.Close() }()
	}
	if shuttingDown() {
		return
	}

	panelCfg := initPanelConfig(botStore != nil)

	// Route discordgo's own logging through slog before the session exists, so
	// nothing it emits escapes to the stdlib logger.
	utils.InstallDiscordgoLogger()

	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		panic(fmt.Sprintf("Error creating Discord session: %v", err))
	}
	dg.LogLevel = utils.DiscordgoLogLevel()
	// IntentsGuildMembers is a Privileged Gateway Intent — must be toggled on
	// in the Discord Developer Portal for this bot application, otherwise
	// dg.Open() fails at runtime with no compile-time signal.
	dg.Identify.Intents = discordgo.IntentsAllWithoutPrivileged | discordgo.IntentsGuildMembers

	// Temporary voice channels (spec #285): handlers must be registered before
	// dg.Open() so the initial GUILD_CREATE seeds voice-state tracking and
	// runs the restart sweep. Without a store there are no hubs, so the
	// feature stays inert: no runtime, no panel, and /voice-rename,
	// /voice-lock and /voice-unlock refuse.
	var (
		tempVC   *commands.TempVC
		foxhole  *commands.FoxholeRuntime
		webPanel *panel.Panel
	)
	// Built before dg.Open(), so the Connect event tells it the gateway
	// connection is up, and the guild's GUILD_CREATE makes it ask for the
	// member list (#440). The panel's hub page says when the connection is
	// down. The one manager serves the runtime, the panel and the startup
	// checks, so the bot asks Discord for the list once.
	discordManager := commands.NewSessionTempVCManager(dg, GuildID)
	if botStore != nil {
		var err error
		tempVC, err = commands.StartTempVC(dg, discordManager, GuildID, botStore)
		if err != nil {
			panic(fmt.Sprintf("Bot store unavailable: %v", err))
		}
		// The Foxhole page's actions read the member list through the
		// manager and change roles through the commands' role calls, which
		// URL-encode the audit log reason. Building the runtime ends any
		// action the last process left running as stopped by a restart.
		foxhole, err = commands.NewFoxholeRuntime(discordManager, commands.NewSessionGuildManager(dg), botStore, GuildID)
		if err != nil {
			panic(fmt.Sprintf("Bot store unavailable: %v", err))
		}
		// The panel's hub page saves through the runtime and reads the guild
		// through the session, so it is built once both exist.
		webPanel = initPanel(panelCfg, panel.Deps{
			Store:   botStore,
			Runtime: tempVC,
			Manager: discordManager,
			Foxhole: foxhole,
			GuildID: GuildID,
		})
	}

	// The registry takes the runtimes, so it is built after them. The
	// Foxhole commands share the one-action-at-a-time rule with the
	// Foxhole page through the Foxhole runtime.
	registry := commands.NewRegistry(tempVC, foxhole)

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		// Backstop only. Registered slash-command handlers are wrapped with
		// their own recover that names the failing command (commands/telemetry.go),
		// so what reaches here is a panic from the routing below or from a
		// component interaction.
		defer utils.RecoverPanic("interaction-handler")
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := registry.GetHandler(i.ApplicationCommandData().Name); ok {
				h(s, i)
			}
		case discordgo.InteractionMessageComponent:
			customID := i.MessageComponentData().CustomID
			parts := strings.Split(customID, "::")
			if len(parts) > 0 {
				if h, ok := registry.GetHandler(parts[0]); ok {
					h(s, i)
				}
			}
		}
	})

	if shuttingDown() {
		return
	}

	// Not dg.Open() directly: a session can open without ever reaching READY,
	// which leaves dg.State.User nil for the command registration below.
	if err := utils.OpenSession(dg); err != nil {
		panic(fmt.Sprintf("Discord session unavailable: %v", err))
	}
	defer func() {
		err := dg.Close()
		if err != nil {
			panic(fmt.Sprintf("Error closing Discord connection: %v", err))
		}
	}()
	if shuttingDown() {
		return
	}

	// Startup checks (spec #285): rank ladder drift and a missing Administrator
	// each capture to Sentry, once per process start. Their own goroutine, so
	// neither fetch holds up command registration or a gateway handler. They
	// need no store. The checks write nothing, and Administrator protects
	// /foxhole as much as spawning.
	go func() {
		defer utils.RecoverPanic("startup-checks")
		commands.RunStartupChecks(context.Background(), discordManager, GuildID, dg.State.User.ID)
	}()

	// Panel (spec #285): the web UI listens once the session is READY, since
	// its later pages act through the Discord session, and before the
	// commands register, which Discord's rate limit can hold for up to about
	// a minute after restarts seconds apart (#470). A port it cannot bind is
	// a deploy error and stops the bot, so the failure is seen rather than
	// found as a 502 later.
	if webPanel != nil {
		if err := webPanel.Start(); err != nil {
			panic(fmt.Sprintf("Panel unavailable: %v", err))
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := webPanel.Stop(stopCtx); err != nil {
				utils.Warn("Panel did not stop cleanly", "error", err)
			}
		}()
	}

	// The startup sync (ADR 0006). A stop while Discord holds it ends startup
	// here. Any other failure, such as a command Discord rejects, stops the
	// bot, and the deferred shutdown still runs.
	utils.Info("Registering commands")
	if err := registry.Sync(ctx, dg, dg.State.User.ID, GuildID); err != nil {
		if shuttingDown() {
			return
		}
		panic(fmt.Sprintf("Cannot register commands: %v", err))
	}
	utils.Info("Commands registered", "count", len(registry.GetCommands()))

	commands.StartJoinerReportScheduler(dg, GuildID)

	start.MarkRunning()
	utils.Info("Bot is now running. Press CTRL-C to exit")
	<-ctx.Done()
	utils.Info("Shutting down")
}
