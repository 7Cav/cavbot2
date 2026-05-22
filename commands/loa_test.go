package commands

import (
	"strings"
	"testing"
	"time"
)

func TestLOAUnavailableMessage(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lastRefresh time.Time
		wantSubstr  string
	}{
		{
			name:        "never refreshed reports never",
			lastRefresh: time.Time{},
			wantSubstr:  "never successfully refreshed",
		},
		{
			name:        "31 minutes ago reports 31 minutes",
			lastRefresh: now.Add(-31 * time.Minute),
			wantSubstr:  "last refresh: 31 minutes ago",
		},
		{
			name:        "90 minutes ago reports 90 minutes",
			lastRefresh: now.Add(-90 * time.Minute),
			wantSubstr:  "last refresh: 90 minutes ago",
		},
		{
			name:        "exactly 30 minutes ago reports 30 minutes",
			lastRefresh: now.Add(-30 * time.Minute),
			wantSubstr:  "last refresh: 30 minutes ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loaUnavailableMessage(tt.lastRefresh, now)
			if !strings.Contains(got, tt.wantSubstr) {
				t.Fatalf("loaUnavailableMessage = %q, want substring %q", got, tt.wantSubstr)
			}
			if !strings.HasPrefix(got, "❌ LOA cache unavailable") {
				t.Fatalf("loaUnavailableMessage = %q, want prefix %q",
					got, "❌ LOA cache unavailable")
			}
			if !strings.HasSuffix(strings.TrimSpace(got), "Try again shortly.") {
				t.Fatalf("loaUnavailableMessage = %q, want suffix %q",
					got, "Try again shortly.")
			}
		})
	}
}
