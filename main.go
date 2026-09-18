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
// binary never runs against a schema it does not understand (ADR 0012). The
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
// the gateway. The hub page reads the store on every load, so a panel with
// no store is a misconfiguration too.
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
		panic("Panel misconfigured: PANEL_ADDR is set but BOT_DB_DSN is empty")
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
	defer utils.InitSentry(Version)()

	utils.Info("CavBot2 starting", "version", Version)

	utils.Info("Warden role base name resolved", "base_name", commands.WardenRoleBaseName())

	initLOACache()

	// Database and migrations come before the Discord session opens, so a
	// failed migration never leaves a half-started bot on the gateway.
	botStore := initBotStore()
	if botStore != nil {
		defer func() { _ = botStore.Close() }()
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

	registry := commands.NewRegistry()

	// Temporary voice channels (spec #285): handlers must be registered before
	// dg.Open() so the initial GUILD_CREATE seeds voice-state tracking and
	// runs the restart sweep. Without a store there are no hubs, so the
	// feature stays inert.
	var webPanel *panel.Panel
	if botStore != nil {
		tempVC, err := commands.StartTempVC(dg, GuildID, botStore)
		if err != nil {
			panic(fmt.Sprintf("Bot store unavailable: %v", err))
		}
		// The panel's hub page saves through the runtime and reads the guild
		// through the session, so it is built once both exist.
		webPanel = initPanel(panelCfg, panel.Deps{
			Store:   botStore,
			Runtime: tempVC,
			Manager: commands.NewSessionTempVCManager(dg),
			GuildID: GuildID,
		})
	}

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

	// Startup checks (spec #285): rank ladder drift and a missing Administrator
	// each capture to Sentry, once per process start. Their own goroutine, so
	// neither fetch holds up command registration or a gateway handler. They
	// need no store. The checks write nothing, and Administrator protects
	// /warden as much as spawning.
	go func() {
		defer utils.RecoverPanic("startup-checks")
		commands.RunStartupChecks(context.Background(), commands.NewSessionTempVCManager(dg), GuildID, dg.State.User.ID)
	}()

	registeredCommandNames := make(map[string]struct{}, len(registry.GetCommands()))
	for _, cmd := range registry.GetCommands() {
		registeredCommandNames[cmd.Name] = struct{}{}
	}
	utils.Info("Removing deprecated commands")
	existingCommands, err := dg.ApplicationCommands(dg.State.User.ID, GuildID)
	if err != nil {
		utils.Warn("Warning: Could not fetch existing commands:", "error", err)
	} else {
		for _, cmd := range existingCommands {
			if _, exists := registeredCommandNames[cmd.Name]; !exists {
				err := dg.ApplicationCommandDelete(dg.State.User.ID, GuildID, cmd.ID)
				if err != nil {
					utils.Warn("Warning: Could not delete deprecated command", "command", cmd.Name, "error", err)
				} else {
					utils.Info("Removed deprecated command", "command", cmd.Name)
				}
			}
		}
	}

	utils.Info("Registering commands")
	registeredCommands := make([]*discordgo.ApplicationCommand, len(registry.GetCommands()))

	for i, cmd := range registry.GetCommands() {
		rcmd, err := dg.ApplicationCommandCreate(dg.State.User.ID, GuildID, cmd)
		if err != nil {
			panic(fmt.Sprintf("Cannot create command %v: %v", cmd.Name, err))
		}
		registeredCommands[i] = rcmd
	}

	commands.StartJoinerReportScheduler(dg, GuildID)

	// Panel (spec #285): the web UI listens only now, with the
	// session READY, since its later pages act through the Discord session.
	// A port it cannot bind is a deploy error and stops the bot, so the
	// failure is seen rather than found as a 502 later.
	if webPanel != nil {
		if err := webPanel.Start(); err != nil {
			panic(fmt.Sprintf("Panel unavailable: %v", err))
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := webPanel.Stop(ctx); err != nil {
				utils.Warn("Panel did not stop cleanly", "error", err)
			}
		}()
	}

	utils.Info("Bot is now running. Press CTRL-C to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	utils.Info("Shutting down")
}
