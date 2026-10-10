package commands

// Temp VC ignores recorders (spec #381, #384). A recorder (GLOSSARY.md) is a
// Discord account of its own that joins a voice channel to record it. Temp
// VC treats it as absent from every hub and spawned channel: it never spawns
// a channel, never keeps one alive, never lands on a guest list, and so is
// never elected owner.

// IgnoreRecorders sets the recorder user IDs, replacing any set before. The
// recorder sessions pass them in at startup; with none set, temp VC counts
// every account.
func (t *TempVC) IgnoreRecorders(userIDs []string) {
	set := make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		set[id] = struct{}{}
	}
	t.recorders.Store(&set)
}

// isRecorder reports whether a user ID is one of the recorders.
func (t *TempVC) isRecorder(userID string) bool {
	set := t.recorders.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[userID]
	return ok
}

// voiceStates reads a fresh snapshot of the cache, as VoiceStates does,
// with the recorders left out, so a channel holding only a recorder reads
// as empty. The snapshot is a copy of its own, so leaving them out touches
// nothing shared.
func (t *TempVC) voiceStates() VoiceSnapshot {
	snap := t.mgr.VoiceStates(t.guildID)
	for userID := range snap.ChannelByUser {
		if t.isRecorder(userID) {
			delete(snap.ChannelByUser, userID)
		}
	}
	return snap
}
