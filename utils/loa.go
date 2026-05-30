package utils

import (
	"database/sql"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	reUsername  = regexp.MustCompile(`\[B\]\[COLOR=rgb\(213,\s*185,\s*0\)\]Username\[/COLOR\]\[/B\]\s*:?\s*(\S+)`)
	reStartDate = regexp.MustCompile(`\[B\]\[COLOR=rgb\(213,\s*185,\s*0\)\]Start Date\[/COLOR\]\[/B\]\s*[\r\n]+([^\r\n]+)`)
	reEndDate   = regexp.MustCompile(`\[B\]\[COLOR=rgb\(213,\s*185,\s*0\)\]End Date\[/COLOR\]\[/B\]\s*[\r\n]+([^\r\n]+)`)
)

const loaDateLayout = "Jan 2, 2006"

type LOAEntry struct {
	Username  string
	StartDate time.Time
	EndDate   time.Time
	ThreadID  int64
}

type LOACache struct {
	mu                    sync.RWMutex
	entries               map[string]LOAEntry
	lastSyncedPostDate    int64
	lastSuccessfulRefresh time.Time // zero == never
}

var GlobalLOACache = &LOACache{
	entries: make(map[string]LOAEntry),
}

// GetEntry returns the LOA entry for a username if one exists.
func (c *LOACache) GetEntry(username string) (LOAEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[strings.ToLower(username)]
	return e, ok
}

// IsOnLOA returns true if the username has a currently active LOA.
func (c *LOACache) IsOnLOA(username string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[strings.ToLower(username)]
	if !ok {
		return false
	}
	now := time.Now()
	return !now.Before(entry.StartDate) && !now.After(entry.EndDate)
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
// On first call it fetches posts from the past year; subsequent calls fetch only newer posts.
// Multiple node IDs are supported to cover all LOA forum sections; each node is queried
// separately so per-node parse counts can be logged for diagnostics.
func (c *LOACache) Refresh(db *sql.DB, nodeIDs []int) {
	c.refresh(sqlLOAFetcher{db: db}, nodeIDs)
}

// refresh is the fetcher-agnostic core of Refresh: it computes the incremental
// `since` cursor, prunes ended entries, then applies each node's fetched posts.
// Refresh wires in the production sqlLOAFetcher; tests supply a fake.
func (c *LOACache) refresh(fetcher loaPostFetcher, nodeIDs []int) {
	c.mu.RLock()
	since := c.lastSyncedPostDate
	c.mu.RUnlock()

	if since == 0 {
		since = time.Now().AddDate(-1, 0, 0).Unix()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Prune entries whose LOA has ended.
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.EndDate) {
			delete(c.entries, k)
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
			c.entries[strings.ToLower(entry.Username)] = entry
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
		c.lastSuccessfulRefresh = time.Now()
	}
	Info("LOA cache refreshed",
		"new_parsed", totalParsed,
		"total_active", len(c.entries),
		"successful_nodes", successfulNodes,
		"total_nodes", len(nodeIDs),
	)
}

func parseLOAPost(msg string) (LOAEntry, bool) {
	usernameMatch := reUsername.FindStringSubmatch(msg)
	startMatch := reStartDate.FindStringSubmatch(msg)
	endMatch := reEndDate.FindStringSubmatch(msg)

	if len(usernameMatch) < 2 || len(startMatch) < 2 || len(endMatch) < 2 {
		return LOAEntry{}, false
	}

	username := strings.TrimSpace(usernameMatch[1])
	startDate, err := time.Parse(loaDateLayout, strings.TrimSpace(startMatch[1]))
	if err != nil {
		return LOAEntry{}, false
	}
	endDate, err := time.Parse(loaDateLayout, strings.TrimSpace(endMatch[1]))
	if err != nil {
		return LOAEntry{}, false
	}

	return LOAEntry{
		Username:  username,
		StartDate: startDate,
		EndDate:   endDate,
	}, true
}
