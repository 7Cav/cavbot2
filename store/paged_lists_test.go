package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// samplePagedList is a three-page list run by member 4242 at ranAt.
func samplePagedList(ranAt time.Time) PagedList {
	return PagedList{
		RunnerID: "4242",
		RanAt:    ranAt,
		Pages: []ListPage{
			{Title: "LOAs for 1-7 (Page 1/3)", Description: "**Active LOAs**\nOne.A", Footer: "Active: 3"},
			{Title: "LOAs for 1-7 (Page 2/3)", Description: "**Active LOAs**\nTwo.B", Footer: "Active: 3"},
			{Title: "LOAs for 1-7 (Page 3/3)", Description: "**Active LOAs**\nThree.C", Footer: "Active: 3"},
		},
	}
}

// A saved list reads back each page with who ran it, when, and how many
// pages it has.
func TestPagedListReadsBackEachPageWithItsRun(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ranAt := time.Date(2026, 10, 1, 18, 30, 0, 0, time.UTC)
		list := samplePagedList(ranAt)
		key, err := s.SavePagedList(ctx, list)
		if err != nil {
			t.Fatalf("SavePagedList: %v", err)
		}
		for idx, want := range list.Pages {
			got, err := s.PagedListPage(ctx, key, idx)
			if err != nil {
				t.Fatalf("PagedListPage(%d): %v", idx, err)
			}
			if got.RunnerID != "4242" || !got.RanAt.Equal(ranAt) || got.Total != 3 || got.Page != want {
				t.Errorf("page %d = %+v, want runner 4242, run at %v, 3 pages and %+v", idx, got, ranAt, want)
			}
		}
	})
}

// Two saved lists keep their pages apart.
func TestPagedListsKeepTheirPagesApart(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ranAt := time.Date(2026, 10, 1, 18, 30, 0, 0, time.UTC)
		first, err := s.SavePagedList(ctx, samplePagedList(ranAt))
		if err != nil {
			t.Fatalf("SavePagedList: %v", err)
		}
		other := PagedList{RunnerID: "77", RanAt: ranAt, Pages: []ListPage{{Title: "AWOL — S1 (Page 1/2)"}, {Title: "AWOL — S1 (Page 2/2)"}}}
		second, err := s.SavePagedList(ctx, other)
		if err != nil {
			t.Fatalf("SavePagedList: %v", err)
		}
		got, err := s.PagedListPage(ctx, first, 1)
		if err != nil {
			t.Fatalf("PagedListPage: %v", err)
		}
		if got.Page.Title != "LOAs for 1-7 (Page 2/3)" {
			t.Errorf("the first list's page 2 is titled %q", got.Page.Title)
		}
		got, err = s.PagedListPage(ctx, second, 1)
		if err != nil {
			t.Fatalf("PagedListPage: %v", err)
		}
		if got.RunnerID != "77" || got.Total != 2 || got.Page.Title != "AWOL — S1 (Page 2/2)" {
			t.Errorf("the second list's page 2 = %+v", got)
		}
	})
}

// A key no list has, as after the sweep deleted its list, reads back
// ErrNotFound.
func TestPagedListPageOfAnUnknownKeyIsNotFound(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if _, err := s.PagedListPage(context.Background(), "no-such-list", 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("PagedListPage = %v, want ErrNotFound", err)
		}
	})
}

// DeletePagedListsBefore deletes every page of a list run before the
// cutoff and keeps every page of one run after it.
func TestDeletePagedListsBeforeDeletesOnlyOlderLists(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		old, err := s.SavePagedList(ctx, samplePagedList(cutoff.AddDate(0, 0, -1)))
		if err != nil {
			t.Fatalf("SavePagedList: %v", err)
		}
		recent, err := s.SavePagedList(ctx, samplePagedList(cutoff.AddDate(0, 0, 1)))
		if err != nil {
			t.Fatalf("SavePagedList: %v", err)
		}
		if err := s.DeletePagedListsBefore(ctx, cutoff); err != nil {
			t.Fatalf("DeletePagedListsBefore: %v", err)
		}
		for idx := range 3 {
			if _, err := s.PagedListPage(ctx, old, idx); !errors.Is(err, ErrNotFound) {
				t.Errorf("the older list's page %d reads back %v, want ErrNotFound", idx+1, err)
			}
			if _, err := s.PagedListPage(ctx, recent, idx); err != nil {
				t.Errorf("the newer list's page %d reads back %v, want it kept", idx+1, err)
			}
		}
	})
}
