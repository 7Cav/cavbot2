package panel

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/store"
)

// A hub page that runs out of time reports how its store reads used the
// time budget (#395). The error names only the read that returned after the
// budget ran out. The report adds each read's time, so an operator can say
// which read used it up.

// pageReads records the store reads one hub page load makes, in the order
// it makes them, against its time budget.
type pageReads struct {
	start    time.Time
	deadline time.Time
	reads    []pageRead
}

// pageRead is one read as the page made it.
type pageRead struct {
	name   string
	took   time.Duration
	ranOut bool
}

// newPageReads starts the record of a page load whose budget started at
// start and ends at deadline.
func newPageReads(start, deadline time.Time) *pageReads {
	return &pageReads{start: start, deadline: deadline}
}

// add records a read that started at started and has just returned. A read
// that returned at or after the deadline is the one the budget ran out
// during.
func (r *pageReads) add(name string, started time.Time) {
	end := panelClock.Now()
	r.reads = append(r.reads, pageRead{name: name, took: end.Sub(started), ranOut: !end.Before(r.deadline)})
}

// timeStoreRead makes a store read and records it under name, whatever it
// returns.
func timeStoreRead[T any](r *pageReads, name string, read func() (T, error)) (T, error) {
	started := panelClock.Now()
	v, err := read()
	r.add(name, started)
	return v, err
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

// report is the record at the page's failure, with the budget it ran under.
func (r *pageReads) report(budget time.Duration) *budgetReport {
	out := &budgetReport{BudgetMS: budget.Milliseconds(), ElapsedMS: panelClock.Now().Sub(r.start).Milliseconds(),
		Reads: make([]readReport, 0, len(r.reads))}
	for _, rd := range r.reads {
		rr := readReport{Read: rd.name, Outcome: outcomeFinished, TookMS: rd.took.Milliseconds()}
		if rd.ranOut {
			rr.Outcome = outcomeRanOut
		}
		out.Reads = append(out.Reads, rr)
	}
	return out
}

// forPage is the service as a page load runs it: the same deps, with every
// store read the page makes recorded in reads. Nothing else about the reads
// changes: they take the page's context as before.
func (s *hubService) forPage(reads *pageReads) *hubService {
	page := *s
	page.deps.Store = pageStore{Store: s.deps.Store, reads: reads}
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
