package utils

import (
	"database/sql"
	"regexp"
	"strings"
	"sync"
	"time"
)

// reFormatBBCode matches formatting-only BBCode tags (bold/italic/underline/
// color/size/font, open or close, with or without an attribute). Forum LOA posts
// wrap the field labels and values in an inconsistent mix of these — the canonical
// template uses [B][COLOR=rgb(213, 185, 0)]…[/COLOR][/B], but real posts use plain
// [B]…[/B], no formatting at all, a different color, or [SIZE]-wrapped values.
// Stripping these tags before matching makes label/value extraction agnostic to the
// formatting, which is the whole source of historical silent parse failures
// (cf. the brittleness note in CLAUDE.md / utils/loa.go header).
var reFormatBBCode = regexp.MustCompile(`(?i)\[/?(?:b|i|u|s|color|size|font)(?:=[^\]]*)?\]`)

var (
	reUsername  = regexp.MustCompile(`(?i)Username\s*:?\s*(\S+)`)
	reStartDate = regexp.MustCompile(`(?i)Start Date\s*:?\s*[\r\n]+\s*([^\r\n]+)`)
	reEndDate   = regexp.MustCompile(`(?i)End Date\s*:?\s*[\r\n]+\s*([^\r\n]+)`)
)

const loaDateLayout = "Jan 2, 2006" // canonical; also used by tests for fixed-instant boundary cases

// loaDateLayouts are the date formats accepted for the Start/End values, tried in
// order. The forum has no input mask, so troopers file dates in several shapes:
// abbreviated or full month name, with or without the comma after the day.
var loaDateLayouts = []string{
	loaDateLayout,
	"January 2, 2006",
	"Jan 2 2006",
	"January 2 2006",
}

type LOAEntry struct {
	Username  string
	StartDate time.Time
	EndDate   time.Time
	ThreadID  int64
}

// loaRetentionYears is the single shared horizon for both the cold-start backfill
// lookback and retention pruning. The cold cache backfills posts from this far
// back, and a window is retained until its EndDate is older than this same
// horizon — so a warm cache and a freshly cold-started cache converge on the
// identical retained window set (ADR 0008).
const loaRetentionYears = 1

// historyHorizon is the cutoff instant `loaRetentionYears` before `now`: the
// cold-start backfill lower bound and the retention prune cutoff. A window whose
// EndDate is before this instant is dropped; the cold backfill ignores posts
// older than it. Both callers go through this one helper so the two can't drift.
func historyHorizon(now time.Time) time.Time {
	return now.AddDate(-loaRetentionYears, 0, 0)
}

type LOACache struct {
	mu                    sync.RWMutex
	entries               map[string][]LOAEntry
	lastSyncedPostDate    int64
	lastSuccessfulRefresh time.Time // zero == never
}

var GlobalLOACache = &LOACache{
	entries: make(map[string][]LOAEntry),
}

// mostRelevant picks the single window to surface for the legacy GetEntry
// contract: an active window (latest-ending if several) wins; otherwise the
// soonest upcoming window; otherwise the most recently ended. This preserves the
// pre-history-store behavior of the [[LOA]] link in /awol and the active/upcoming
// split in /loa now that a username can hold multiple windows.
func mostRelevant(windows []LOAEntry, now time.Time) (LOAEntry, bool) {
	var (
		best  LOAEntry
		found bool
		state int // 0 none, 1 ended, 2 upcoming, 3 active
	)
	for _, w := range windows {
		var (
			s   int
			tie time.Time
		)
		switch {
		case w.isActiveAt(now):
			s, tie = 3, w.EndDate // latest-ending active wins
		case now.Before(w.StartDate):
			s, tie = 2, w.StartDate // soonest upcoming wins (earliest start)
		default:
			s, tie = 1, w.EndDate // most recently ended wins (latest end)
		}
		if !found || s > state {
			best, state, found = w, s, true
			continue
		}
		if s != state {
			continue
		}
		switch s {
		case 2: // upcoming: prefer earliest start
			if tie.Before(best.StartDate) {
				best = w
			}
		default: // active or ended: prefer latest end
			if tie.After(best.EndDate) {
				best = w
			}
		}
	}
	return best, found
}

// GetEntry returns the most-relevant retained LOA window for a username if one
// exists (see mostRelevant). Preserved unchanged for /awol's [[LOA]] link and
// /loa's active/upcoming rendering; GetEntries exposes the full history.
func (c *LOACache) GetEntry(username string) (LOAEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return mostRelevant(c.entries[strings.ToLower(username)], time.Now())
}

// GetEntries returns all retained LOA windows for a username, or an empty slice
// when none. The returned slice is a copy — callers may not mutate cache state.
func (c *LOACache) GetEntries(username string) []LOAEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	windows := c.entries[strings.ToLower(username)]
	out := make([]LOAEntry, len(windows))
	copy(out, windows)
	return out
}

// hasEnded reports whether the entry's EndDate is strictly before `now`. It is
// the single source of truth for the active window's upper boundary used by
// isActiveAt: an entry on its EndDate (End==now) has NOT ended and is still
// active; one whose EndDate is already past has ended. Retention (drop on
// horizon) is a separate, looser cutoff owned by isRetired — an ended window is
// not dropped, only one past historyHorizon is.
func (e LOAEntry) hasEnded(now time.Time) bool {
	return now.After(e.EndDate)
}

// isActiveAt reports whether the entry's LOA window is active at the instant
// `now`. The window is inclusive at both bounds: an entry is active from its
// StartDate through its EndDate (so Start==now and End==now both count as active).
// The upper bound is hasEnded. Clock-injected so the boundary semantics are
// unit-testable at the exact edge (cf. PR #135); the production callers pass
// time.Now(). IsActive is the exported wrapper used by /awol's single-snapshot read.
func (e LOAEntry) isActiveAt(now time.Time) bool {
	return !now.Before(e.StartDate) && !e.hasEnded(now)
}

// IsActive reports whether the entry's LOA window is active at `now`. Exported so
// /awol can derive its On LOA verdict from the same single GetEntry snapshot it
// uses for the [[LOA]] link, instead of a second IsOnLOA lock acquisition that a
// concurrent refresh could make inconsistent (now that ended windows are retained).
func (e LOAEntry) IsActive(now time.Time) bool {
	return e.isActiveAt(now)
}

// isRetired reports whether the window's EndDate is older than the retention
// horizon (loaRetentionYears before now) and so should be dropped from the
// history store. This is the retention cutoff that replaces prune-on-expiry: a
// recently-ended window is kept, one whose EndDate predates the horizon is gone.
// Shares historyHorizon with the cold-start backfill so warm and cold caches
// converge on the identical window set.
func (e LOAEntry) isRetired(now time.Time) bool {
	return e.EndDate.Before(historyHorizon(now))
}

// IsOnLOA returns true if the username has any currently active LOA window.
func (c *LOACache) IsOnLOA(username string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	for _, w := range c.entries[strings.ToLower(username)] {
		if w.isActiveAt(now) {
			return true
		}
	}
	return false
}

// IsHealthy reports whether the cache has been successfully refreshed within
// maxAge. The returned timestamp is the last-successful-refresh time (zero if
// the cache has never refreshed successfully) so callers can render diagnostic
// messages without a second lock acquisition.
func (c *LOACache) IsHealthy(maxAge time.Duration) (bool, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastSuccessfulRefresh.IsZero() {
		return false, time.Time{}
	}
	return time.Since(c.lastSuccessfulRefresh) <= maxAge, c.lastSuccessfulRefresh
}

// loaPostRow is one forum post row fetched for a node: the raw message body and
// the post/thread identifiers the core refresh loop needs.
type loaPostRow struct {
	message  string
	postDate int64
	threadID int64
}

// loaPostFetcher abstracts the per-node forum fetch so the refresh/prune core can
// be exercised with a fake instead of a live MySQL connection. The production
// implementation (sqlLOAFetcher) wraps *sql.DB and preserves the exact query and
// row/stream-error semantics the cache relies on. This is a narrow test seam only;
// it does not replace the process-global GlobalLOACache singleton with DI.
type loaPostFetcher interface {
	// fetchLOAPosts returns all visible LOA posts in nodeID with post_date > since,
	// ordered oldest-first. A non-nil error means the node fetch failed (including a
	// mid-stream rows error) and must not count as a successful node.
	fetchLOAPosts(nodeID int, since int64) ([]loaPostRow, error)
}

// sqlLOAFetcher is the production loaPostFetcher backed by the Xenforo forum DB.
type sqlLOAFetcher struct {
	db *sql.DB
}

func (f sqlLOAFetcher) fetchLOAPosts(nodeID int, since int64) ([]loaPostRow, error) {
	rows, err := f.db.Query(`
		SELECT p.message, t.post_date, t.thread_id
		FROM xf_thread t
		JOIN xf_post p ON p.post_id = t.first_post_id
		WHERE t.node_id = ?
		  AND t.discussion_state = 'visible'
		  AND t.post_date > ?
		ORDER BY t.post_date ASC
	`, nodeID, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []loaPostRow
	for rows.Next() {
		var r loaPostRow
		if err := rows.Scan(&r.message, &r.postDate, &r.threadID); err != nil {
			Warn("LOA row scan failed", "node_id", nodeID, "error", err)
			continue
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Refresh fetches LOA posts from the forum DB incrementally and updates the cache.
// On a cold cache it backfills from the shared retention horizon (loaRetentionYears
// before now, via historyHorizon); subsequent calls fetch only newer posts.
// Multiple node IDs are supported to cover all LOA forum sections; each node is queried
// separately so per-node parse counts can be logged for diagnostics.
func (c *LOACache) Refresh(db *sql.DB, nodeIDs []int) {
	c.refresh(sqlLOAFetcher{db: db}, nodeIDs)
}

// refresh is the fetcher-agnostic core of Refresh: it computes the incremental
// `since` cursor, drops windows past the retention horizon (keeping recently-ended
// ones as history; see isRetired/historyHorizon), then applies each node's fetched
// posts. Refresh wires in the production sqlLOAFetcher; tests supply a fake.
func (c *LOACache) refresh(fetcher loaPostFetcher, nodeIDs []int) {
	c.mu.RLock()
	since := c.lastSyncedPostDate
	c.mu.RUnlock()

	now := time.Now()
	if since == 0 {
		// Cold start: backfill from the shared retention horizon so the cold cache
		// holds the same window set a warm one would have retained.
		since = historyHorizon(now).Unix()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Retention prune: drop only windows whose EndDate predates the horizon. Ended
	// (but recent) windows are kept as history for the accountable-day calc (#159).
	for k, windows := range c.entries {
		kept := windows[:0]
		for _, w := range windows {
			if !w.isRetired(now) {
				kept = append(kept, w)
			}
		}
		if len(kept) == 0 {
			delete(c.entries, k)
		} else {
			c.entries[k] = kept
		}
	}

	maxPostDate := c.lastSyncedPostDate
	totalParsed := 0
	successfulNodes := 0

	for _, nodeID := range nodeIDs {
		posts, err := fetcher.fetchLOAPosts(nodeID, since)
		if err != nil {
			Warn("LOA cache refresh failed", "node_id", nodeID, "error", err)
			continue
		}

		nodeParsed := 0
		for _, p := range posts {
			entry, ok := parseLOAPost(p.message)
			if !ok {
				Debug("LOA post skipped (parse failed)", "node_id", nodeID, "post_date", p.postDate)
				continue
			}
			entry.ThreadID = p.threadID
			c.upsertWindow(entry)
			if p.postDate > maxPostDate {
				maxPostDate = p.postDate
			}
			nodeParsed++
		}

		Info("LOA node refreshed", "node_id", nodeID, "new_parsed", nodeParsed)
		totalParsed += nodeParsed
		successfulNodes++
	}

	c.lastSyncedPostDate = maxPostDate
	if successfulNodes > 0 {
		c.lastSuccessfulRefresh = now
	}

	totalWindows := 0
	for _, w := range c.entries {
		totalWindows += len(w)
	}
	Info("LOA cache refreshed",
		"new_parsed", totalParsed,
		"total_windows", totalWindows,
		"total_users", len(c.entries),
		"successful_nodes", successfulNodes,
		"total_nodes", len(nodeIDs),
	)
}

// upsertWindow adds a parsed window to the per-username history, deduping by
// ThreadID: one forum thread is exactly one LOA, so re-scanning the same thread
// (cold-restart backfill or overlapping incremental fetch) updates the existing
// window in place rather than appending a duplicate. Caller holds c.mu.
//
// ThreadID 0 is treated as non-dedupable: the live query keys on a non-null PK so
// a real thread is never 0, but if one ever appeared, deduping on 0 would silently
// fold every 0-thread LOA for a user into a single window. Such an entry is always
// appended (never matched), and the anomaly is logged so it doesn't pass unnoticed.
func (c *LOACache) upsertWindow(entry LOAEntry) {
	key := strings.ToLower(entry.Username)
	windows := c.entries[key]
	if entry.ThreadID == 0 {
		Warn("LOA window with zero ThreadID; appending without dedup", "username", entry.Username)
		c.entries[key] = append(windows, entry)
		return
	}
	for i := range windows {
		if windows[i].ThreadID == entry.ThreadID {
			windows[i] = entry
			c.entries[key] = windows
			return
		}
	}
	c.entries[key] = append(windows, entry)
}

func parseLOAPost(msg string) (LOAEntry, bool) {
	// Strip formatting-only BBCode first so label/value matching is agnostic to the
	// post's bold/color/size wrapping (the source of historical silent failures).
	msg = reFormatBBCode.ReplaceAllString(msg, "")

	usernameMatch := reUsername.FindStringSubmatch(msg)
	startMatch := reStartDate.FindStringSubmatch(msg)
	endMatch := reEndDate.FindStringSubmatch(msg)

	if len(usernameMatch) < 2 || len(startMatch) < 2 || len(endMatch) < 2 {
		return LOAEntry{}, false
	}

	username := strings.TrimSpace(usernameMatch[1])
	startDate, ok := parseLOADate(startMatch[1])
	if !ok {
		return LOAEntry{}, false
	}
	endDate, ok := parseLOADate(endMatch[1])
	if !ok {
		return LOAEntry{}, false
	}

	return LOAEntry{
		Username:  username,
		StartDate: startDate,
		EndDate:   endDate,
	}, true
}

// parseLOADate parses a Start/End date value against each accepted layout. The raw
// value is trimmed first; a value that matches no layout (free-text like
// "probably May 8, 2026") yields ok=false and the whole post is skipped.
func parseLOADate(raw string) (time.Time, bool) {
	v := strings.TrimSpace(raw)
	for _, layout := range loaDateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
