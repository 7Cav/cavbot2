package commands

import (
	"slices"
	"testing"
	"time"

	"github.com/7cav/cavbot2/metrics"
)

func TestMetricsJob(t *testing.T) {
	cases := []struct {
		name     string
		now      time.Time
		from, to time.Time
	}{
		{
			name: "rolls up from the Monday after the retention cutoff",
			now:  metricsTestNow, // cutoff Friday 2026-05-15 15:30
			from: time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "a cutoff on a Monday midnight keeps that week",
			now:  time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), // cutoff Monday 2026-05-18 00:00
			from: time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeMetricsStore()
			c, _, scheduled := newTestMetricsCollector(t, metricsTestGuild(), st, nil)
			metricsNow = func() time.Time { return tc.now }

			c.runJob()

			if want := tc.now.Add(-metrics.RawRetention); !st.prunedBefore.Equal(want) {
				t.Errorf("pruned before %v, want %v", st.prunedBefore, want)
			}
			if st.rollUp != [2]time.Time{tc.from, tc.to} {
				t.Errorf("rolled up %v, want [%v %v]", st.rollUp, tc.from, tc.to)
			}
			var names []string
			for _, s := range st.rolledUp {
				names = append(names, s.Name)
			}
			if !slices.Equal(names, []string{"A/1-7", "Operations"}) {
				t.Errorf("rolled up sections %v, want A/1-7 and Operations", names)
			}
			if len(*scheduled) != 1 {
				t.Errorf("%d next passes scheduled, want 1", len(*scheduled))
			}
		})
	}
}

func TestMetricsJobSchedulesNextPassAfterPanic(t *testing.T) {
	st := newFakeMetricsStore()
	st.panicPrune = true
	c, _, scheduled := newTestMetricsCollector(t, metricsTestGuild(), st, nil)

	c.runJob()

	if len(*scheduled) != 1 {
		t.Errorf("%d next passes scheduled, want 1", len(*scheduled))
	}
}
