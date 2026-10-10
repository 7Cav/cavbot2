package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The recording roles (#383): the guild-wide set of roles whose holders may
// record, saved the way the guild-wide moderator roles are.

// recordingRolesEntry is a recording roles save's entry, with an ordinal in
// its diff.
func recordingRolesEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeRecordingRoles
	return e
}

// storedRecordingRoles reads guild-1's recording roles and their version.
func storedRecordingRoles(t *testing.T, s Store) RecordingRoles {
	t.Helper()
	roles, err := s.GetRecordingRoles(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetRecordingRoles: %v", err)
	}
	return roles
}

// saveRecordingRoles saves guild-1's recording roles over the version the
// store holds now, as a save from a section loaded just before it.
func saveRecordingRoles(t *testing.T, s Store, roleIDs []string, entry ChangeLogEntry) error {
	t.Helper()
	current := storedRecordingRoles(t, s)
	return s.SaveRecordingRoles(context.Background(), "guild-1", RecordingRoles{RoleIDs: roleIDs, Version: current.Version}, entry)
}

// A guild with no recording roles saved reads back none, at version 0. A
// save reads back as the set it was saved as, a second save replaces it,
// and the section's list holds each save's one entry, newest first, and
// none of a guild-wide moderator save's.
func TestRecordingRolesRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if got := storedRecordingRoles(t, s); len(got.RoleIDs) != 0 || got.Version != 0 {
			t.Errorf("GetRecordingRoles with no row = %+v, want no roles at version 0", got)
		}

		want := []string{"role-s2", "role-s6"}
		if err := saveRecordingRoles(t, s, want, recordingRolesEntry(1)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		if got := storedRecordingRoles(t, s).RoleIDs; !slices.Equal(sortedRoles(got), want) {
			t.Errorf("GetRecordingRoles = %v, want %v (as a set)", got, want)
		}

		if err := saveGuildRoles(t, s, []string{"role-mp"}, moderatorsEntry(2)); err != nil {
			t.Fatalf("SaveGuildModeratorRoles: %v", err)
		}
		if err := saveRecordingRoles(t, s, []string{"role-hq"}, recordingRolesEntry(3)); err != nil {
			t.Fatalf("second SaveRecordingRoles: %v", err)
		}
		if got := storedRecordingRoles(t, s).RoleIDs; !slices.Equal(got, []string{"role-hq"}) {
			t.Errorf("GetRecordingRoles after the second save = %v, want [role-hq]", got)
		}
		entries, err := s.ListRecordingRoleChanges(context.Background(), 10)
		if err != nil {
			t.Fatalf("ListRecordingRoleChanges: %v", err)
		}
		var ordinals []int
		for _, e := range entries {
			ordinals = append(ordinals, ordinalOf(t, e))
		}
		if !slices.Equal(ordinals, []int{3, 1}) {
			t.Errorf("recording roles changes are ordinals %v, want [3 1]", ordinals)
		}
	})
}

// A save over a stored row moves the set to another version, so a section
// loaded before the save is stale.
func TestRecordingRolesSaveOverAStoredRowMovesItsVersion(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if err := saveRecordingRoles(t, s, []string{"role-s2"}, recordingRolesEntry(1)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		loaded := storedRecordingRoles(t, s)

		saved := RecordingRoles{RoleIDs: []string{"role-s6"}, Version: loaded.Version}
		if err := s.SaveRecordingRoles(context.Background(), "guild-1", saved, recordingRolesEntry(2)); err != nil {
			t.Fatalf("SaveRecordingRoles over the stored row: %v", err)
		}

		if got := storedRecordingRoles(t, s).Version; got == loaded.Version {
			t.Errorf("after a save over version %d the set reads back at %d, want another version", loaded.Version, got)
		}
	})
}

// Recordings (#386): a recording's row is written when a recorder joins and
// closed when it leaves.

// sampleRecording is a running recording in guild-1, with every field the
// start writes set off its zero value, so a write that drops one reads back
// wrong.
func sampleRecording() Recording {
	return Recording{
		GuildID:     "guild-1",
		ChannelID:   "vc-1",
		ChannelName: "Briefing Room",
		StarterID:   "user-s",
		Title:       "S2 interview",
		RecorderID:  "user-recorder",
		StartedAt:   time.Date(2026, time.October, 10, 18, 0, 0, 0, time.UTC),
	}
}

// listRecordings reads guild-1's recordings.
func listRecordings(t *testing.T, s Store) []Recording {
	t.Helper()
	recs, err := s.ListRecordings(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	return recs
}

// A started recording reads back as it was started, under the ID the start
// returned and not yet stopped. A stop records its time and how it ended,
// and the recording then reads back stopped at it, ended that way. A second
// stop of it, and a stop of an ID no recording has, are ErrNotFound.
func TestRecordingStartsAndStops(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		want := sampleRecording()
		started, err := s.StartRecording(ctx, want)
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		want.ID = started.ID

		got := listRecordings(t, s)
		if len(got) != 1 || !sameRecording(got[0], want) {
			t.Fatalf("recordings after the start = %+v, want only %+v", got, want)
		}

		stoppedAt := want.StartedAt.Add(42 * time.Minute)
		if err := s.StopRecording(ctx, started.ID, stoppedAt, RecordingEndCap, nil); err != nil {
			t.Fatalf("StopRecording: %v", err)
		}
		want.StoppedAt, want.Ended, want.Mix = stoppedAt, RecordingEndCap, MixProcessing
		if got := listRecordings(t, s); len(got) != 1 || !sameRecording(got[0], want) {
			t.Errorf("recordings after the stop = %+v, want only %+v", got, want)
		}

		if err := s.StopRecording(ctx, started.ID, stoppedAt.Add(time.Minute), RecordingEndStopped, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("second StopRecording = %v, want ErrNotFound", err)
		}
		if err := s.StopRecording(ctx, started.ID+100, stoppedAt, RecordingEndStopped, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("StopRecording of an unknown ID = %v, want ErrNotFound", err)
		}
		if got := listRecordings(t, s); len(got) != 1 || !sameRecording(got[0], want) {
			t.Errorf("recordings after the refused stops = %+v, want only %+v", got, want)
		}
	})
}

// A stop records who spoke, and the recording reads back with them and its
// mix processing.
func TestRecordingStopKeepsItsSpeakersAndLeavesTheMixProcessing(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		started, err := s.StartRecording(ctx, sampleRecording())
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		speakers := []Speaker{{ID: "user-a", DisplayName: "SGT Doe.J"}, {ID: "user-b", DisplayName: "applicant"}}

		if err := s.StopRecording(ctx, started.ID, started.StartedAt.Add(time.Hour), RecordingEndStopped, speakers); err != nil {
			t.Fatalf("StopRecording: %v", err)
		}

		got := listRecordings(t, s)
		if len(got) != 1 || got[0].Mix != MixProcessing || !sameSpeakers(got[0].Speakers, speakers) {
			t.Errorf("recordings after the stop = %+v, want one with speakers %+v and its mix %q", got, speakers, MixProcessing)
		}
	})
}

// A recording's mix reads back in the state it was last set to. Setting the
// mix of an ID no recording has is ErrNotFound.
func TestRecordingMixReadsBackAsSet(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		started, err := s.StartRecording(ctx, sampleRecording())
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		if err := s.StopRecording(ctx, started.ID, started.StartedAt.Add(time.Hour), RecordingEndStopped, nil); err != nil {
			t.Fatalf("StopRecording: %v", err)
		}

		if err := s.SetRecordingMix(ctx, started.ID, MixReady); err != nil {
			t.Fatalf("SetRecordingMix: %v", err)
		}

		if got := listRecordings(t, s); len(got) != 1 || got[0].Mix != MixReady {
			t.Errorf("recordings after the mix was set = %+v, want one with its mix %q", got, MixReady)
		}
		if err := s.SetRecordingMix(ctx, started.ID+100, MixReady); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetRecordingMix of an unknown ID = %v, want ErrNotFound", err)
		}
	})
}

// sameRecording compares two recordings field by field, times by instant,
// since Postgres hands a time back in its own location, and speakers as a
// set.
func sameRecording(a, b Recording) bool {
	ta, tb := a, b
	ta.StartedAt, tb.StartedAt = time.Time{}, time.Time{}
	ta.StoppedAt, tb.StoppedAt = time.Time{}, time.Time{}
	ta.Speakers, tb.Speakers = nil, nil
	return reflect.DeepEqual(ta, tb) && a.StartedAt.Equal(b.StartedAt) && a.StoppedAt.Equal(b.StoppedAt) &&
		sameSpeakers(a.Speakers, b.Speakers)
}

// sameSpeakers compares two speaker lists as sets: no reader depends on
// their order.
func sameSpeakers(a, b []Speaker) bool {
	byID := func(x, y Speaker) int { return strings.Compare(x.ID, y.ID) }
	sa, sb := slices.SortedFunc(slices.Values(a), byID), slices.SortedFunc(slices.Values(b), byID)
	return slices.Equal(sa, sb)
}
