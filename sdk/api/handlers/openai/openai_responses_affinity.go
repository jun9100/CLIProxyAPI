package openai

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const responsesAuthAffinityTTL = 2 * time.Hour

var responsesHTTPAuthAffinity = newResponsesAuthAffinityStore(responsesAuthAffinityTTL)

// ClearResponsesAuthAffinityOwner removes response-affinity entries owned by one client API key.
func ClearResponsesAuthAffinityOwner(owner string) {
	responsesHTTPAuthAffinity.clearOwner(owner)
}

type responsesAuthAffinityEntry struct {
	owner     string
	authID    string
	expiresAt time.Time
}

type responsesAuthAffinityStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]responsesAuthAffinityEntry
}

func newResponsesAuthAffinityStore(ttl time.Duration) *responsesAuthAffinityStore {
	if ttl <= 0 {
		ttl = responsesAuthAffinityTTL
	}
	return &responsesAuthAffinityStore{
		ttl:     ttl,
		entries: make(map[string]responsesAuthAffinityEntry),
	}
}

func (s *responsesAuthAffinityStore) lookupRequest(rawJSON []byte) string {
	if s == nil || len(rawJSON) == 0 {
		return ""
	}
	responseID, promptCacheKey := responsesAuthAffinityRequestIdentifiers(rawJSON)
	return s.lookup(responseID, promptCacheKey)
}

func (s *responsesAuthAffinityStore) bindPayload(owner, authID string, payload []byte) {
	if s == nil || len(payload) == 0 {
		return
	}
	owner = strings.TrimSpace(owner)
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	for _, candidate := range responsesAuthAffinityPayloads(payload) {
		responseID, promptCacheKey := responsesAuthAffinityResponseIdentifiers(candidate)
		s.bind(owner, authID, responseID, promptCacheKey)
	}
}

func (s *responsesAuthAffinityStore) bind(owner, authID, responseID, promptCacheKey string) {
	if s == nil {
		return
	}
	owner = strings.TrimSpace(owner)
	authID = strings.TrimSpace(authID)
	responseID = strings.TrimSpace(responseID)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	if authID == "" || (responseID == "" && promptCacheKey == "") {
		return
	}

	now := time.Now()
	entry := responsesAuthAffinityEntry{
		owner:     owner,
		authID:    authID,
		expiresAt: now.Add(s.ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked(now)
	if responseID != "" {
		s.entries["resp:"+responseID] = entry
	}
	if promptCacheKey != "" {
		s.entries["cache:"+promptCacheKey] = entry
	}
}

func (s *responsesAuthAffinityStore) lookup(responseID, promptCacheKey string) string {
	if s == nil {
		return ""
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked(now)

	if responseID = strings.TrimSpace(responseID); responseID != "" {
		if entry, ok := s.entries["resp:"+responseID]; ok && entry.expiresAt.After(now) {
			return entry.authID
		}
	}
	if promptCacheKey = strings.TrimSpace(promptCacheKey); promptCacheKey != "" {
		if entry, ok := s.entries["cache:"+promptCacheKey]; ok && entry.expiresAt.After(now) {
			return entry.authID
		}
	}
	return ""
}

func (s *responsesAuthAffinityStore) clearOwner(owner string) {
	if s == nil {
		return
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked(now)
	for key, entry := range s.entries {
		if entry.owner == owner {
			delete(s.entries, key)
		}
	}
}

func (s *responsesAuthAffinityStore) compactLocked(now time.Time) {
	for key, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

func (s *responsesAuthAffinityStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.entries)
}

func responsesAuthAffinityRequestIdentifiers(rawJSON []byte) (string, string) {
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()),
		strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_key").String())
}

func responsesAuthAffinityResponseIdentifiers(payload []byte) (string, string) {
	responseID := strings.TrimSpace(gjson.GetBytes(payload, "response.id").String())
	if responseID == "" {
		responseID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "response.prompt_cache_key").String())
	if promptCacheKey == "" {
		promptCacheKey = strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String())
	}
	return responseID, promptCacheKey
}

func responsesAuthAffinityPayloads(chunk []byte) [][]byte {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return websocketJSONPayloadsFromChunk(trimmed)
	}
	return [][]byte{trimmed}
}

func observeResponsesAuthAffinity(owner string, data <-chan []byte, selectedAuthID func() string) <-chan []byte {
	if data == nil {
		return nil
	}
	out := make(chan []byte)
	go func() {
		defer close(out)
		for chunk := range data {
			if selectedAuthID != nil {
				responsesHTTPAuthAffinity.bindPayload(owner, selectedAuthID(), chunk)
			}
			out <- chunk
		}
	}()
	return out
}
