package panel

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// A hub page that runs out of time reports how its reads used the time
// budget (#395). The error names only the read that returned after the
// budget ran out. The report adds each read's time, so an operator can say
// whether the store, Discord, or a wait inside discordgo used it up.

// pageReads records the reads one hub page load makes, in the order it
// makes them, against its time budget.
type pageReads struct {
	start    time.Time
	deadline time.Time
	reads    []pageRead
}

// pageRead is one read as the page made it. discord is nil for a store read.
type pageRead struct {
	name    string
	took    time.Duration
	ranOut  bool
	discord *commands.ReadTiming
}

// newPageReads starts the record of a page load whose budget started at
// start and ends at deadline.
func newPageReads(start, deadline time.Time) *pageReads {
	return &pageReads{start: start, deadline: deadline}
}

// add records a read that started at started and has just returned. A read
// that returned at or after the deadline is the one the budget ran out
// during.
func (r *pageReads) add(name string, started time.Time, discord *commands.ReadTiming) {
	end := time.Now()
	r.reads = append(r.reads, pageRead{name: name, took: end.Sub(started), ranOut: !end.Before(r.deadline), discord: discord})
}

// timeStoreRead makes a store read and records it under name, whatever it
// returns.
func timeStoreRead[T any](r *pageReads, name string, read func() (T, error)) (T, error) {
	started := time.Now()
	v, err := read()
	r.add(name, started, nil)
	return v, err
}

// timeDiscordRead makes a Discord read and records it under name with the
// manager's split of its time, whatever it returns.
func timeDiscordRead[T any](r *pageReads, name string, read func() (T, commands.ReadTiming, error)) (T, commands.ReadTiming, error) {
	started := time.Now()
	v, timing, err := read()
	r.add(name, started, &timing)
	return v, timing, err
}

// budgetReport is the record as the Sentry event carries it, under
// time_budget: the budget, the time from its start to the failure, and each
// read. Times are whole milliseconds. Its keys and outcome tokens are what
// an operator reads on the issue page, and what the tests read.
type budgetReport struct {
	BudgetMS  int64        `json:"budget_ms"`
	ElapsedMS int64        `json:"elapsed_ms"`
	Reads     []readReport `json:"reads"`
}

type readReport struct {
	Read    string      `json:"read"`
	Outcome readOutcome `json:"outcome"`
	TookMS  int64       `json:"took_ms"`
	// Discord is a value, not a pointer, so the log line CaptureError
	// writes shows the split and not an address.
	Discord discordReport `json:"discord,omitzero"`
}

// readOutcome is how a read ended against the budget, as the report
// names it.
type readOutcome string

const (
	// outcomeFinished is a read that returned within the budget.
	outcomeFinished readOutcome = "finished"
	// outcomeRanOut is the read the budget ran out during.
	outcomeRanOut readOutcome = "ran-out"
)

// discordReport is a Discord read's time split as the manager gave it. A
// store read has none.
type discordReport struct {
	WaitingMS int64 `json:"waiting_ms"`
	TripsMS   int64 `json:"trips_ms"`
	Attempts  int   `json:"attempts"`
}

// report is the record at the page's failure, with the budget it ran under.
func (r *pageReads) report(budget time.Duration) *budgetReport {
	out := &budgetReport{BudgetMS: budget.Milliseconds(), ElapsedMS: time.Since(r.start).Milliseconds(),
		Reads: make([]readReport, 0, len(r.reads))}
	for _, rd := range r.reads {
		rr := readReport{Read: rd.name, Outcome: outcomeFinished, TookMS: rd.took.Milliseconds()}
		if rd.ranOut {
			rr.Outcome = outcomeRanOut
		}
		if rd.discord != nil {
			rr.Discord = discordReport{WaitingMS: rd.discord.Waiting.Milliseconds(),
				TripsMS: rd.discord.Trips.Milliseconds(), Attempts: rd.discord.Attempts}
		}
		out.Reads = append(out.Reads, rr)
	}
	return out
}

// forPage is the service as a page load runs it: the same deps, with every
// read the page makes recorded in reads. Nothing else about the reads
// changes: the store reads take the page's context as before, and the
// Discord reads still take none.
func (s *hubService) forPage(reads *pageReads) *hubService {
	page := *s
	page.deps.Store = pageStore{Store: s.deps.Store, reads: reads}
	page.deps.Manager = pageManager{TempVCManager: s.deps.Manager, reads: reads}
	return &page
}

// pageStore records the page's store reads. Every other call passes
// through unrecorded; a page load makes none. A read added to the page needs
// its method here, or the report leaves it out.
type pageStore struct {
	store.Store
	reads *pageReads
}

func (s pageStore) ListHubs(ctx context.Context, guildID string) ([]store.Hub, error) {
	return timeStoreRead(s.reads, listHubsRead, func() ([]store.Hub, error) {
		return s.Store.ListHubs(ctx, guildID)
	})
}

func (s pageStore) GetGuildModeratorRoles(ctx context.Context, guildID string) (store.GuildModeratorRoles, error) {
	return timeStoreRead(s.reads, guildModeratorRolesRead, func() (store.GuildModeratorRoles, error) {
		return s.Store.GetGuildModeratorRoles(ctx, guildID)
	})
}

func (s pageStore) ListModeratorChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	return timeStoreRead(s.reads, moderatorChangesRead, func() ([]store.ChangeLogEntry, error) {
		return s.Store.ListModeratorChanges(ctx, limit)
	})
}

func (s pageStore) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]store.ChangeLogEntry, error) {
	return timeStoreRead(s.reads, changeLogRead, func() ([]store.ChangeLogEntry, error) {
		return s.Store.ListChangeLog(ctx, hubID, limit)
	})
}

// pageManager records the page's Discord reads with the manager's split
// of each. Every other call passes through unrecorded.
type pageManager struct {
	commands.TempVCManager
	reads *pageReads
}

func (m pageManager) GuildChannels(guildID string) ([]*discordgo.Channel, commands.ReadTiming, error) {
	return timeDiscordRead(m.reads, guildChannelsRead, func() ([]*discordgo.Channel, commands.ReadTiming, error) {
		return m.TempVCManager.GuildChannels(guildID)
	})
}

func (m pageManager) Guild(guildID string) (*discordgo.Guild, commands.ReadTiming, error) {
	return timeDiscordRead(m.reads, guildRead, func() (*discordgo.Guild, commands.ReadTiming, error) {
		return m.TempVCManager.Guild(guildID)
	})
}
