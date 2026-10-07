//go:build unix

// Command smoke runs a checkout's bot on the test guild for a smoke test:
// the bot, the panel on http://localhost:8080, the bot's Postgres, and a fake
// forum that signs the panel in as a chosen persona. docs/smoke-test.md says
// when to run one and what to report. It reads the test guild's credentials
// from the gitignored .env at the main checkout's root and never prints them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// errUsage ends a command that already printed its usage; errSilent ends one
// that already said what went wrong.
var (
	errUsage  = errors.New("usage")
	errSilent = errors.New("failed")
)

var commands = map[string]func([]string) error{
	"up":        cmdUp,
	"down":      cmdDown,
	"status":    cmdStatus,
	"logs":      cmdLogs,
	"forum":     cmdForum,
	"api":       cmdAPI,
	"supervise": cmdSupervise,
}

const usage = `Runs this checkout's bot on the test guild for a smoke test.
docs/smoke-test.md says when to run one and what to report.

usage: go run ./tools/smoke <command> [flags]

  up      build this checkout's bot, run it with the panel on http://localhost:8080,
          and return once it is ready
  down    stop the run, from any worktree
  status  say whether a run is up, from which worktree, and where its logs are
  logs    print the end of the bot's log
  forum   set what the fake forum answers: ok, expired, refused or down
  api     call the Discord API as the bot

go run ./tools/smoke <command> -h lists a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	err := cmd(os.Args[2:])
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	case errors.Is(err, errSilent):
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "smoke:", err)
		os.Exit(1)
	}
}
