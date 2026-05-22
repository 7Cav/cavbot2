package utils

import (
	"testing"
	"time"
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
