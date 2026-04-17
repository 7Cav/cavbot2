package utils

import (
	"database/sql"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	reUsername  = regexp.MustCompile(`\[B\]\[COLOR=rgb\(213,\s*185,\s*0\)\]Username\[/COLOR\]\[/B\]\s*:\s*(\S+)`)
	reStartDate = regexp.MustCompile(`\[B\]\[COLOR=rgb\(213,\s*185,\s*0\)\]Start Date\[/COLOR\]\[/B\]\s*[\r\n]+([^\r\n]+)`)
	reEndDate   = regexp.MustCompile(`\[B\]\[COLOR=rgb\(213,\s*185,\s*0\)\]End Date\[/COLOR\]\[/B\]\s*[\r\n]+([^\r\n]+)`)
)

const loaDateLayout = "Jan 2, 2006"

type LOAEntry struct {
	Username  string
	StartDate time.Time
	EndDate   time.Time
}

type LOACache struct {
	mu                 sync.RWMutex
	entries            map[string]LOAEntry
	lastSyncedPostDate int64
}

var GlobalLOACache = &LOACache{
	entries: make(map[string]LOAEntry),
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

// Refresh fetches LOA posts from the forum DB incrementally and updates the cache.
// On first call it fetches posts from the past year; subsequent calls fetch only newer posts.
func (c *LOACache) Refresh(db *sql.DB, nodeID int) {
	c.mu.RLock()
	since := c.lastSyncedPostDate
	c.mu.RUnlock()

	if since == 0 {
		since = time.Now().AddDate(-1, 0, 0).Unix()
	}

	rows, err := db.Query(`
		SELECT p.message, t.post_date
		FROM xf_thread t
		JOIN xf_post p ON p.post_id = t.first_post_id
		WHERE t.node_id = ?
		  AND t.discussion_state = 'visible'
		  AND t.post_date > ?
		ORDER BY t.post_date ASC
	`, nodeID, since)
	if err != nil {
		Warn("LOA cache refresh failed", "error", err)
		return
	}
	defer rows.Close()

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
	parsed := 0
	for rows.Next() {
		var message string
		var postDate int64
		if err := rows.Scan(&message, &postDate); err != nil {
			Warn("LOA row scan failed", "error", err)
			continue
		}
		entry, ok := parseLOAPost(message)
		if !ok {
			Debug("LOA post skipped (parse failed)", "post_date", postDate)
			continue
		}
		c.entries[strings.ToLower(entry.Username)] = entry
		if postDate > maxPostDate {
			maxPostDate = postDate
		}
		parsed++
	}

	c.lastSyncedPostDate = maxPostDate
	Info("LOA cache refreshed", "new_parsed", parsed, "total_active", len(c.entries))
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
