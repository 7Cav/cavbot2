package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
)

var (
	Token   string
	GuildID string
)

func init() {
	Token = os.Getenv("DISCORD_TOKEN")
	GuildID = os.Getenv("GUILD_ID")

	if Token == "" {
		log.Fatal("No token provided. Please set DISCORD_TOKEN environment variable")
	}
	if GuildID == "" {
		log.Fatal("No GuildID provided. Please set GUILD_ID environment variable")
	}
}

func main() {
	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	registry := commands.NewRegistry()

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := registry.GetHandler(i.ApplicationCommandData().Name); ok {
			h(s, i)
		}
	})

	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}
	defer func() {
		err := dg.Close()
		if err != nil {
			log.Printf("Error closing Discord connection: %v", err)
		}
	}()

	log.Println("Registering commands...")
	registeredCommands := make([]*discordgo.ApplicationCommand, len(registry.GetCommands()))
	for i, cmd := range registry.GetCommands() {
		rcmd, err := dg.ApplicationCommandCreate(dg.State.User.ID, GuildID, cmd)
		if err != nil {
			log.Panicf("Cannot create command %v: %v", cmd.Name, err)
		}
		registeredCommands[i] = rcmd
	}

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	if GuildID != "" {
		log.Println("Removing commands...")
		for _, cmd := range registeredCommands {
			err := dg.ApplicationCommandDelete(dg.State.User.ID, GuildID, cmd.ID)
			if err != nil {
				log.Panicf("Cannot delete command %v: %v", cmd.Name, err)
			}
		}
	}
}
