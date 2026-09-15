package panel

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// panelNow is the package clock. A var so tests drive session and pending
// sign-in lifetimes without sleeping, the same reason telemetryNow is a var.
var panelNow = time.Now

const (
	// sessionLifetime matches the forum access token's two hours. No refresh
	// token is stored; the user signs in again through the forum.
	sessionLifetime = 2 * time.Hour
	// pendingLifetime matches the forum's authorization code lifetime. A
	// sign-in that has not come back through the callback in five minutes is
	// dropped.
	pendingLifetime = 5 * time.Minute
	// pruneInterval is how often expired entries are swept from memory. A
	// request against an expired entry is refused whatever the sweep timing.
	pruneInterval = time.Minute
)

// session is the panel's record that a browser is signed in as one forum
// user. Lives in memory; a restart ends every session.
type session struct {
	accessToken string
	userID      int
	username    string
	expires     time.Time
}

// pendingSignIn is a sign-in that has redirected to the forum and not yet
// come back: the state to match and the PKCE verifier to send at exchange.
type pendingSignIn struct {
	state    string
	verifier string
	expires  time.Time
}

// memoryStore is a mutex-guarded map of entries keyed by opaque IDs, with an
// expiry read through a function so one type serves both sessions and
// pending sign-ins.
type memoryStore[T any] struct {
	mu      sync.Mutex
	entries map[string]T
	expires func(T) time.Time
}

func newMemoryStore[T any](expires func(T) time.Time) *memoryStore[T] {
	return &memoryStore[T]{entries: make(map[string]T), expires: expires}
}

// put stores the entry under a fresh 128-bit random ID and returns the ID.
func (m *memoryStore[T]) put(v T) string {
	id := newID()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[id] = v
	return id
}

// get returns the entry under id, whether it exists, and whether it is still
// live. An expired entry is returned once with live false and dropped, so
// the caller can tell an ended session from an unknown one.
func (m *memoryStore[T]) get(id string) (v T, found, live bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, found = m.entries[id]
	if !found {
		return v, false, false
	}
	if !panelNow().Before(m.expires(v)) {
		delete(m.entries, id)
		return v, true, false
	}
	return v, true, true
}

func (m *memoryStore[T]) delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
}

// prune drops every expired entry.
func (m *memoryStore[T]) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := panelNow()
	for id, v := range m.entries {
		if !now.Before(m.expires(v)) {
			delete(m.entries, id)
		}
	}
}

// newID returns 128 random bits as hex, the opaque key both cookies carry.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("panel: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
