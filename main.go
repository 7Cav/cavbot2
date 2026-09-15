package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/7cav/cavbot2/panel"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"

	_ "github.com/go-sql-driver/mysql"

	"github.com/7cav/cavbot2/commands"
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

// storeStartupTimeout bounds the wait for the bot's database at startup: the
// ping retry inside store.Open plus the migrations. Postgres restarting under
// a compose `up` answers well inside it; a database that never answers fails
// the start, and the restart policy retries.
const storeStartupTimeout = 60 * time.Second

// initStore opens the bot's Postgres database and applies its migrations
// before anything else runs. Without BOT_DB_DSN the feature that needs the
// store is inert: one WARN line and a nil store. With it, a database that
// never answers or a migration that fails ends the process non-zero, so a
// Watchtower pull never runs a binary against a schema it does not understand;
// the next start retries (ADR 0012).
func initStore() store.Store {
	dsn := os.Getenv("BOT_DB_DSN")
	if dsn == "" {
		utils.Warn("BOT_DB_DSN not set, store disabled")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeStartupTimeout)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		panic(fmt.Sprintf("Bot database unavailable: %v", err))
	}
	if err := store.Migrate(ctx, db); err != nil {
		panic(fmt.Sprintf("Bot database migration failed: %v", err))
	}
	utils.Info("Bot database migrations applied")
	return store.NewPostgres(db)
}

// panelShutdownTimeout is how long in-flight panel requests get to finish at
// exit before the listener is closed under them.
const panelShutdownTimeout = 5 * time.Second

// startPanel serves the panel on PANEL_ADDR until ctx ends. With PANEL_ADDR
// unset it logs one line and returns; the panel is inert (#288). Called after
// READY so the panel never answers before the bot can act on the guild.
func startPanel(ctx context.Context) {
	cfg, on := panel.ConfigFromEnv()
	if !on {
		utils.Info("PANEL_ADDR not set, panel disabled")
		return
	}
	utils.Info("Panel configured",
		"addr", cfg.Addr, "base_url", cfg.BaseURL, "group_ids", cfg.GroupIDs)

	srv := panel.New(cfg)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go srv.Run(ctx)
	go func() {
		defer utils.RecoverPanic("panel-listener")
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			utils.CaptureError("Panel listener stopped", err, "addr", cfg.Addr)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), panelShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			utils.Warn("Panel shutdown did not finish cleanly", "error", err)
		}
	}()
}

func main() {
	defer utils.InitSentry(Version)()

	utils.Info("CavBot2 starting", "version", Version)

	utils.Info("Warden role base name resolved", "base_name", commands.WardenRoleBaseName())

	// Opened and migrated before the Discord session, so a failed migration
	// never leaves a half-started bot on the gateway. The temporary voice
	// channel runtime takes the store over in #289; until then main only
	// reports whether one exists.
	botStore := initStore()
	utils.Info("Bot database resolved", "configured", botStore != nil)

	initLOACache()

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

	// Temp voice channels (issue #100): handlers must be registered before
	// dg.Open() so the initial GUILD_CREATE seeds voice-state tracking and
	// sweeps orphaned temp channels.
	if tempVCCfg, ok := commands.LoadTempVCConfig(GuildID); ok {
		commands.StartTempVC(dg, tempVCCfg)
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

	// Rank ladder drift and a missing Administrator are Sentry events, not log
	// lines (#287). Off the main goroutine so the gateway handlers never wait
	// on the milpacs API.
	go commands.RunStartupChecks(commands.NewSessionTempVCManager(dg), GuildID, dg.State.User.ID)

	// The panel listens last, once the bot is on the gateway and its checks
	// are running. runCtx ends at the shutdown signal and stops the listener.
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	startPanel(runCtx)

	utils.Info("Bot is now running. Press CTRL-C to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	utils.Info("Shutting down")
}
