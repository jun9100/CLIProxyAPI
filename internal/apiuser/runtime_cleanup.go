package apiuser

import (
	"strings"
	"sync"
)

// RuntimeCleanupRegistry tracks runtime resources owned by one client API key.
type RuntimeCleanupRegistry struct {
	mu            sync.Mutex
	sessionsByKey map[string]map[string]struct{}
	keyBySession  map[string]string
	closeSession  func(string)
	clearAffinity func(string)
}

// NewRuntimeCleanupRegistry creates a cleanup registry for API-key-scoped runtime state.
func NewRuntimeCleanupRegistry(closeSession func(string), clearAffinity func(string)) *RuntimeCleanupRegistry {
	return &RuntimeCleanupRegistry{
		sessionsByKey: make(map[string]map[string]struct{}),
		keyBySession:  make(map[string]string),
		closeSession:  closeSession,
		clearAffinity: clearAffinity,
	}
}

// TrackSession registers one long-lived execution session under a client API key.
func (r *RuntimeCleanupRegistry) TrackSession(apiKey, sessionID string) {
	if r == nil {
		return
	}
	apiKey = strings.TrimSpace(apiKey)
	sessionID = strings.TrimSpace(sessionID)
	if apiKey == "" || sessionID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if previousKey, exists := r.keyBySession[sessionID]; exists && previousKey != apiKey {
		deleteSessionLocked(r.sessionsByKey, previousKey, sessionID)
	}
	sessions := r.sessionsByKey[apiKey]
	if sessions == nil {
		sessions = make(map[string]struct{})
		r.sessionsByKey[apiKey] = sessions
	}
	sessions[sessionID] = struct{}{}
	r.keyBySession[sessionID] = apiKey
}

// UntrackSession removes one execution session from the registry.
func (r *RuntimeCleanupRegistry) UntrackSession(sessionID string) {
	if r == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	apiKey, exists := r.keyBySession[sessionID]
	if !exists {
		return
	}
	delete(r.keyBySession, sessionID)
	deleteSessionLocked(r.sessionsByKey, apiKey, sessionID)
}

// CleanupAPIKey clears runtime resources for one client API key only.
func (r *RuntimeCleanupRegistry) CleanupAPIKey(apiKey string) {
	if r == nil {
		return
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}

	var sessions []string

	r.mu.Lock()
	if tracked := r.sessionsByKey[apiKey]; len(tracked) > 0 {
		sessions = make([]string, 0, len(tracked))
		for sessionID := range tracked {
			sessions = append(sessions, sessionID)
			delete(r.keyBySession, sessionID)
		}
		delete(r.sessionsByKey, apiKey)
	}
	r.mu.Unlock()

	for _, sessionID := range sessions {
		if r.closeSession != nil {
			r.closeSession(sessionID)
		}
	}
	if r.clearAffinity != nil {
		r.clearAffinity(apiKey)
	}
}

func deleteSessionLocked(sessionsByKey map[string]map[string]struct{}, apiKey, sessionID string) {
	sessions := sessionsByKey[apiKey]
	if len(sessions) == 0 {
		return
	}
	delete(sessions, sessionID)
	if len(sessions) == 0 {
		delete(sessionsByKey, apiKey)
	}
}

// QuotaRecoveryTracker emits exactly one recovery signal when a key transitions
// from quota-blocked to the first subsequent allowed decision.
type QuotaRecoveryTracker struct {
	mu         sync.Mutex
	quotaBlock map[string]struct{}
}

// NewQuotaRecoveryTracker creates a tracker for quota recovery transitions.
func NewQuotaRecoveryTracker() *QuotaRecoveryTracker {
	return &QuotaRecoveryTracker{
		quotaBlock: make(map[string]struct{}),
	}
}

// Observe updates tracked state and reports whether this decision is the first
// clean pass after a quota-exceeded block for the same API key.
func (t *QuotaRecoveryTracker) Observe(apiKey string, decision Decision) bool {
	if t == nil {
		return false
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if decision.Blocked && decision.Code == ErrorCodeQuotaExceeded {
		t.quotaBlock[apiKey] = struct{}{}
		return false
	}

	if _, blocked := t.quotaBlock[apiKey]; !blocked {
		return false
	}
	if decision.Blocked {
		return false
	}

	delete(t.quotaBlock, apiKey)
	return true
}
