package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// memberDoe is the member the Foxhole cases write notes on.
const memberDoe = "100000000000000001"

// noteEntry is a note save's change log entry, its diff naming the note's
// new text so a test can tell entries apart. The panel decides the diff's
// shape; the store keeps the bytes.
func noteEntry(after string) ChangeLogEntry {
	return ChangeLogEntry{
		ForumUserID:   1234,
		ForumUsername: "Doe.J",
		Action:        ChangeNote,
		Diff:          json.RawMessage(fmt.Sprintf(`{"note": {"after": %q}}`, after)),
	}
}

// saveNote saves a note on a member of guild-1 over the note given as
// loaded, with the member's names fixed, and its noteEntry.
func saveNote(s Store, memberID, before, note string) error {
	return s.SaveFoxholeNote(context.Background(), "guild-1",
		NoteSave{MemberID: memberID, Before: before, Note: note, DisplayName: "SGT Doe.J", Username: "jdoe"}, noteEntry(note))
}

// foxholeMembers lists guild-1's Foxhole records keyed by member ID.
func foxholeRecords(t *testing.T, s Store) map[string]FoxholeRecord {
	t.Helper()
	members, err := s.ListFoxholeRecords(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("ListFoxholeRecords: %v", err)
	}
	out := map[string]FoxholeRecord{}
	for _, m := range members {
		out[m.MemberID] = m
	}
	return out
}

// foxholeChanges lists the Foxhole change log, newest first.
func foxholeChanges(t *testing.T, s Store) []ChangeLogEntry {
	t.Helper()
	entries, err := s.ListFoxholeChanges(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListFoxholeChanges: %v", err)
	}
	return entries
}

// A note save reads back: the member's record holds the note and the names
// the save gave, and the Foxhole change log holds the save's entry as given,
// with a time the store set. Another guild has no record.
func TestNoteSaveReadsBack(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		save := NoteSave{MemberID: memberDoe, Note: "discharged 12 Sep, ask before re-adding", DisplayName: "SGT Doe.J", Username: "jdoe"}
		entry := noteEntry(save.Note)

		if err := s.SaveFoxholeNote(ctx, "guild-1", save, entry); err != nil {
			t.Fatalf("SaveFoxholeNote: %v", err)
		}

		members := foxholeRecords(t, s)
		got, ok := members[memberDoe]
		if len(members) != 1 || !ok {
			t.Fatalf("records = %+v, want one, for %s", members, memberDoe)
		}
		if got.Note != save.Note || got.DisplayName != save.DisplayName || got.Username != save.Username {
			t.Errorf("record = %+v, want note %q under %q @%s", got, save.Note, save.DisplayName, save.Username)
		}
		entries := foxholeChanges(t, s)
		if len(entries) != 1 {
			t.Fatalf("the Foxhole change log holds %d entries, want 1", len(entries))
		}
		e := entries[0]
		if e.ForumUserID != entry.ForumUserID || e.ForumUsername != entry.ForumUsername || e.Action != ChangeNote {
			t.Errorf("entry = %+v, want forum user %d %s and action %s", e, entry.ForumUserID, entry.ForumUsername, ChangeNote)
		}
		if e.At.IsZero() {
			t.Error("At is zero, want a time set by the store")
		}
		if diff, want := decodeDiff(t, e.Diff), decodeDiff(t, entry.Diff); !reflect.DeepEqual(diff, want) {
			t.Errorf("diff = %v, want %v", diff, want)
		}
		other, err := s.ListFoxholeRecords(ctx, "guild-2")
		if err != nil {
			t.Fatalf("ListFoxholeRecords(guild-2): %v", err)
		}
		if len(other) != 0 {
			t.Errorf("guild-2's records = %+v, want none", other)
		}
	})
}

// foxholeState is everything a note save can write, read back through the
// store: guild-1's Foxhole records and the Foxhole change log.
type foxholeState struct {
	Members map[string]FoxholeRecord
	Entries []ChangeLogEntry
}

func readFoxholeState(t *testing.T, s Store) foxholeState {
	t.Helper()
	return foxholeState{Members: foxholeRecords(t, s), Entries: foxholeChanges(t, s)}
}

// A note save writes only over the note its saver loaded. One whose Before
// isn't the stored note, because another save changed the note or started
// it since, is ErrStale and writes neither the note nor its entry. With
// TestNoteSaveWithADiffThatIsNotJSONWritesNothing, this holds the note and
// its entry together whichever the store writes first.
func TestNoteSaveOverAnotherNoteIsStaleAndWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		before string
	}{
		{"loaded another note", "ask before re-adding"},
		{"loaded no note, and another save started one", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				if err := saveNote(s, memberDoe, "", "discharged 12 Sep"); err != nil {
					t.Fatalf("seed save: %v", err)
				}
				before := readFoxholeState(t, s)

				err := saveNote(s, memberDoe, tc.before, "rejoined, fine to re-add")

				if !errors.Is(err, ErrStale) {
					t.Errorf("the save returned %v, want ErrStale", err)
				}
				if after := readFoxholeState(t, s); !reflect.DeepEqual(after, before) {
					t.Errorf("the store after the stale save = %+v, want it as before, %+v", after, before)
				}
			})
		})
	}
}

// Saving an empty note on a member who isn't an approved collaborator
// removes their record, since the store holds one only for a member with a
// note or an approval, and the save's entry lands with the old text the
// panel put in it.
func TestEmptyNoteSaveRemovesTheRecordAndAppendsItsEntry(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if err := saveNote(s, memberDoe, "", "discharged 12 Sep"); err != nil {
			t.Fatalf("seed save: %v", err)
		}

		if err := saveNote(s, memberDoe, "discharged 12 Sep", ""); err != nil {
			t.Fatalf("SaveFoxholeNote with an empty note: %v", err)
		}

		if members := foxholeRecords(t, s); len(members) != 0 {
			t.Errorf("records = %+v, want none", members)
		}
		entries := foxholeChanges(t, s)
		if len(entries) != 2 {
			t.Fatalf("the Foxhole change log holds %d entries, want 2", len(entries))
		}
		if diff, want := decodeDiff(t, entries[0].Diff), decodeDiff(t, noteEntry("").Diff); !reflect.DeepEqual(diff, want) {
			t.Errorf("the newest entry's diff = %v, want the clear's, %v", diff, want)
		}
	})
}

// A note save whose entry's diff is not a JSON value fails, and the store
// holds neither its note nor its entry: the note and its entry land together
// or not at all. With TestNoteSaveOverAnotherNoteIsStaleAndWritesNothing,
// this holds whichever the store writes first. A regression pin: the save
// wrote both in one transaction from its first case, and fails this once
// they go in two.
func TestNoteSaveWithADiffThatIsNotJSONWritesNothing(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if err := saveNote(s, memberDoe, "", "discharged 12 Sep"); err != nil {
			t.Fatalf("seed save: %v", err)
		}
		before := readFoxholeState(t, s)

		err := s.SaveFoxholeNote(context.Background(), "guild-1",
			NoteSave{MemberID: memberDoe, Before: "discharged 12 Sep", Note: "rejoined", DisplayName: "SGT Doe.J", Username: "jdoe"}, notJSON())

		if err == nil {
			t.Error("the save returned no error, want the diff refused")
		}
		if after := readFoxholeState(t, s); !reflect.DeepEqual(after, before) {
			t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
		}
	})
}

// A name refresh replaces the last-seen names of each member given who has
// a record, starts no record for a member who has none, and appends no
// entry: no person made the change.
func TestNameRefreshUpdatesOnlyMembersWithARecord(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if err := saveNote(s, memberDoe, "", "discharged 12 Sep"); err != nil {
			t.Fatalf("seed save: %v", err)
		}
		const stranger = "100000000000000009"

		err := s.SetFoxholeRecordNames(ctx, "guild-1", []MemberNames{
			{MemberID: memberDoe, DisplayName: "CPL Doe.J", Username: "jdoe_cav"},
			{MemberID: stranger, DisplayName: "Ghost", Username: "ghost"},
		})

		if err != nil {
			t.Fatalf("SetFoxholeRecordNames: %v", err)
		}
		members := foxholeRecords(t, s)
		if got := members[memberDoe]; got.DisplayName != "CPL Doe.J" || got.Username != "jdoe_cav" || got.Note != "discharged 12 Sep" {
			t.Errorf("%s's record = %+v, want CPL Doe.J @jdoe_cav with the note kept", memberDoe, got)
		}
		if got, ok := members[stranger]; ok {
			t.Errorf("a record was started for %s, %+v, want none", stranger, got)
		}
		if entries := foxholeChanges(t, s); len(entries) != 1 {
			t.Errorf("the Foxhole change log holds %d entries, want 1, the seed's", len(entries))
		}
	})
}

// noteOf reads the new note text back out of a noteEntry diff.
func noteOf(t *testing.T, e ChangeLogEntry) string {
	t.Helper()
	var diff struct{ Note struct{ After string } }
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatalf("decode diff %s: %v", e.Diff, err)
	}
	return diff.Note.After
}

// The Foxhole change log is kept apart from the hub page's. ListFoxholeChanges
// returns at most limit note entries, newest first, and none of a hub save,
// a remove or a guild-wide moderator save. No hub page list returns a note
// entry. A regression pin: the note entries had their own log from the
// first note case.
func TestFoxholeChangesAreKeptApartFromTheHubPagesLog(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		removed := storeHub(t, s, "hub-2")
		if err := s.RemoveHub(ctx, removed, removeEntry(1)); err != nil {
			t.Fatalf("RemoveHub: %v", err)
		}
		if err := saveGuildRoles(t, s, []string{"role-mp"}, moderatorsEntry(2)); err != nil {
			t.Fatalf("SaveGuildModeratorRoles: %v", err)
		}
		loaded := ""
		for i := 1; i <= 12; i++ {
			note := fmt.Sprintf("note %d", i)
			if err := saveNote(s, memberDoe, loaded, note); err != nil {
				t.Fatalf("save %d: %v", i, err)
			}
			loaded = note
		}

		entries, err := s.ListFoxholeChanges(ctx, 10)

		if err != nil {
			t.Fatalf("ListFoxholeChanges: %v", err)
		}
		var notes []string
		for _, e := range entries {
			if e.Action != ChangeNote {
				t.Errorf("the Foxhole change log lists a %s entry, want note entries alone", e.Action)
				continue
			}
			notes = append(notes, noteOf(t, e))
		}
		if len(notes) != 10 || notes[0] != "note 12" || notes[9] != "note 3" {
			t.Errorf("the Foxhole change log lists %v, want the last ten, note 12 down to note 3", notes)
		}
		hubLists := map[string]func() ([]ChangeLogEntry, error){
			"ListChangeLog(hub-1)":  func() ([]ChangeLogEntry, error) { return s.ListChangeLog(ctx, hubID, 100) },
			"ListChangeLog(no hub)": func() ([]ChangeLogEntry, error) { return s.ListChangeLog(ctx, 0, 100) },
			"ListModeratorChanges":  func() ([]ChangeLogEntry, error) { return s.ListModeratorChanges(ctx, 100) },
		}
		for name, list := range hubLists {
			entries, err := list()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, e := range entries {
				if e.Action == ChangeNote {
					t.Errorf("%s lists a note entry, %s", name, e.Diff)
				}
			}
		}
	})
}
