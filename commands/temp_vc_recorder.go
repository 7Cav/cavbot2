package commands

// Temp VC ignores recorders (spec #381, #384). A recorder (GLOSSARY.md) is a
// Discord account of its own that joins a voice channel to record it. Temp
// VC treats it as absent from every hub and spawned channel: it never spawns
// a channel, never keeps one alive, never lands on a guest list, and so is
// never elected owner.
//
// The set is fixed before any gateway event can reach the runtime:
// StartTempVC sets it before it registers a handler. A recorder the record
// already counted would stay an occupant, since its own events are dropped,
// so the set is never changed afterwards.

// ignoreRecorders sets the recorder user IDs. StartTempVC calls it once,
// before it registers any handler, and the tests call it at the same point.
// It is read without the lock, since nothing writes it after that.
func (t *TempVC) ignoreRecorders(userIDs []string) {
	t.recorders = make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		t.recorders[id] = struct{}{}
	}
}

// isRecorder reports whether a user ID is one of the recorders.
func (t *TempVC) isRecorder(userID string) bool {
	_, ok := t.recorders[userID]
	return ok
}

// voiceStatesWithoutRecorders reads a fresh snapshot of the cache, as
// VoiceStates does, with the recorders left out, so a channel holding only
// a recorder reads as empty. A read that counts who is in a channel goes
// through it: the delete check, the lock's guest list and the restart
// sweep. A read that asks where one member already in the record is, or
// the member an event is for, needs no filter, since the drop at the top
// of HandleVoiceStateUpdate keeps every recorder out of both. The snapshot
// is a copy of its own, so leaving them out touches nothing shared.
func (t *TempVC) voiceStatesWithoutRecorders() VoiceSnapshot {
	snap := t.mgr.VoiceStates(t.guildID)
	for userID := range snap.ChannelByUser {
		if t.isRecorder(userID) {
			delete(snap.ChannelByUser, userID)
		}
	}
	return snap
}
