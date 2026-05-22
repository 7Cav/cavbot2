package utils

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestIsHealthy(t *testing.T) {
	tests := []struct {
		name       string
		seed       time.Time // zero value means "never refreshed"
		maxAge     time.Duration
		wantOK     bool
		wantSeeded bool // true means returned timestamp must equal seed; false means must be zero
	}{
		{
			name:       "never refreshed returns false and zero time",
			seed:       time.Time{},
			maxAge:     30 * time.Minute,
			wantOK:     false,
			wantSeeded: false,
		},
		{
			name:       "just inside the threshold is healthy",
			seed:       time.Now().Add(-30*time.Minute + 5*time.Second),
			maxAge:     30 * time.Minute,
			wantOK:     true,
			wantSeeded: true,
		},
		{
			name:       "just outside the threshold is unhealthy",
			seed:       time.Now().Add(-30*time.Minute - 5*time.Second),
			maxAge:     30 * time.Minute,
			wantOK:     false,
			wantSeeded: true,
		},
		{
			name:       "well past threshold is unhealthy",
			seed:       time.Now().Add(-2 * time.Hour),
			maxAge:     30 * time.Minute,
			wantOK:     false,
			wantSeeded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &LOACache{entries: map[string]LOAEntry{}}
			c.lastSuccessfulRefresh = tt.seed

			gotOK, gotTime := c.IsHealthy(tt.maxAge)
			if gotOK != tt.wantOK {
				t.Fatalf("IsHealthy ok = %v, want %v", gotOK, tt.wantOK)
			}
			if tt.wantSeeded {
				if !gotTime.Equal(tt.seed) {
					t.Fatalf("IsHealthy time = %v, want %v", gotTime, tt.seed)
				}
			} else if !gotTime.IsZero() {
				t.Fatalf("IsHealthy time = %v, want zero", gotTime)
			}
		})
	}
}

// loaQueryRegex is the regex sqlmock matches against the raw SELECT Refresh issues.
// QueryMatcherRegexp matches against the raw query string (whitespace/newlines intact),
// so we pin a substring that lives on a single line within the actual SQL — the
// column-list line — and escape the dots. That stays robust against later changes
// to FROM/WHERE/ORDER BY formatting while still rejecting an entirely different query.
const loaQueryRegex = `SELECT p\.message, t\.post_date, t\.thread_id`

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// validLOAMessage returns a forum post body that parseLOAPost accepts.
func validLOAMessage(username string) string {
	return "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: " + username + "\n" +
		"[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\nJan 1, 2099\n" +
		"[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\nJan 31, 2099\n"
}

func TestRefresh_AllNodesFail_DoesNotUpdateTimestamp(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	nodeIDs := []int{180, 400, 540}
	for range nodeIDs {
		mock.ExpectQuery(loaQueryRegex).WillReturnError(sql.ErrConnDone)
	}

	c.Refresh(db, nodeIDs)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.Equal(seed) {
		t.Fatalf("lastSuccessfulRefresh moved to %v, want unchanged %v",
			c.lastSuccessfulRefresh, seed)
	}
}

func TestRefresh_PartialSuccess_UpdatesTimestamp(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	rows := sqlmock.NewRows([]string{"message", "post_date", "thread_id"}).
		AddRow(validLOAMessage("alice"), time.Now().Unix(), int64(12345))
	mock.ExpectQuery(loaQueryRegex).WillReturnRows(rows)
	mock.ExpectQuery(loaQueryRegex).WillReturnError(sql.ErrConnDone)

	before := time.Now()
	c.Refresh(db, []int{180, 400})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.After(seed) {
		t.Fatalf("lastSuccessfulRefresh = %v, expected later than seed %v",
			c.lastSuccessfulRefresh, seed)
	}
	if c.lastSuccessfulRefresh.Before(before) {
		t.Fatalf("lastSuccessfulRefresh = %v, expected >= %v",
			c.lastSuccessfulRefresh, before)
	}
}

func TestRefresh_RowsErrMidStream_DoesNotCount(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	// Node 1: Query succeeds, returns a row, then rows.Err() is non-nil at end.
	// Node 2: Query fails outright.
	streamErr := errors.New("simulated stream failure")
	rows := sqlmock.NewRows([]string{"message", "post_date", "thread_id"}).
		AddRow(validLOAMessage("bob"), time.Now().Unix(), int64(999)).
		RowError(0, streamErr) // injected: surfaces via rows.Err() after iteration
	mock.ExpectQuery(loaQueryRegex).WillReturnRows(rows)
	mock.ExpectQuery(loaQueryRegex).WillReturnError(sql.ErrConnDone)

	c.Refresh(db, []int{180, 400})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.Equal(seed) {
		t.Fatalf("lastSuccessfulRefresh moved to %v, want unchanged %v "+
			"(mid-stream failures must not count as success)",
			c.lastSuccessfulRefresh, seed)
	}
}

func TestRefresh_RowParseFailure_StillCountsNodeAsSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	// Single node returns one well-formed row + one row whose body parseLOAPost rejects.
	// Both rows complete; rows.Err() is nil. The node should still count as successful.
	rows := sqlmock.NewRows([]string{"message", "post_date", "thread_id"}).
		AddRow(validLOAMessage("carol"), time.Now().Unix(), int64(1)).
		AddRow("not-a-loa-post-body", time.Now().Unix(), int64(2))
	mock.ExpectQuery(loaQueryRegex).WillReturnRows(rows)

	c.Refresh(db, []int{180})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.After(seed) {
		t.Fatalf("lastSuccessfulRefresh = %v, expected updated (row-level parse "+
			"noise must not invalidate the node)", c.lastSuccessfulRefresh)
	}
}
