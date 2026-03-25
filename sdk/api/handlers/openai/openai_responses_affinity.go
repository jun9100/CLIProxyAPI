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

type responsesAuthAffinityEntry struct {
	authID         string
	requestPayload []byte
	responseOutput []byte
	expiresAt      time.Time
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
	return s.lookupState(responseID, promptCacheKey).authID
}

type responsesAuthAffinityState struct {
	authID         string
	requestPayload []byte
	responseOutput []byte
}

func cloneResponsesAffinityBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	return append([]byte(nil), src...)
}

func (s *responsesAuthAffinityStore) lookupRequestState(rawJSON []byte) responsesAuthAffinityState {
	if s == nil || len(rawJSON) == 0 {
		return responsesAuthAffinityState{}
	}
	responseID, promptCacheKey := responsesAuthAffinityRequestIdentifiers(rawJSON)
	return s.lookupState(responseID, promptCacheKey)
}

func (s *responsesAuthAffinityStore) bindPayload(authID string, requestPayload []byte, payload []byte) {
	if s == nil || len(payload) == 0 {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	for _, candidate := range responsesAuthAffinityPayloads(payload) {
		responseID, promptCacheKey := responsesAuthAffinityResponseIdentifiers(candidate)
		s.bind(authID, responseID, promptCacheKey, requestPayload, responsesOutputFromPayload(candidate))
	}
}

func (s *responsesAuthAffinityStore) bind(authID, responseID, promptCacheKey string, requestPayload []byte, responseOutput []byte) {
	if s == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	responseID = strings.TrimSpace(responseID)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	if authID == "" || (responseID == "" && promptCacheKey == "") {
		return
	}

	now := time.Now()
	expiresAt := now.Add(s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked(now)
	if responseID != "" {
		s.entries["resp:"+responseID] = responsesAuthAffinityEntry{
			authID:         authID,
			requestPayload: cloneResponsesAffinityBytes(requestPayload),
			responseOutput: cloneResponsesAffinityBytes(responseOutput),
			expiresAt:      expiresAt,
		}
	}
	if promptCacheKey != "" {
		s.entries["cache:"+promptCacheKey] = responsesAuthAffinityEntry{
			authID:         authID,
			requestPayload: cloneResponsesAffinityBytes(requestPayload),
			responseOutput: cloneResponsesAffinityBytes(responseOutput),
			expiresAt:      expiresAt,
		}
	}
}

func (s *responsesAuthAffinityStore) lookupState(responseID, promptCacheKey string) responsesAuthAffinityState {
	if s == nil {
		return responsesAuthAffinityState{}
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactLocked(now)

	if responseID = strings.TrimSpace(responseID); responseID != "" {
		if entry, ok := s.entries["resp:"+responseID]; ok && entry.expiresAt.After(now) {
			return responsesAuthAffinityState{
				authID:         entry.authID,
				requestPayload: cloneResponsesAffinityBytes(entry.requestPayload),
				responseOutput: cloneResponsesAffinityBytes(entry.responseOutput),
			}
		}
	}
	if promptCacheKey = strings.TrimSpace(promptCacheKey); promptCacheKey != "" {
		if entry, ok := s.entries["cache:"+promptCacheKey]; ok && entry.expiresAt.After(now) {
			return responsesAuthAffinityState{
				authID:         entry.authID,
				requestPayload: cloneResponsesAffinityBytes(entry.requestPayload),
				responseOutput: cloneResponsesAffinityBytes(entry.responseOutput),
			}
		}
	}
	return responsesAuthAffinityState{}
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

func responsesOutputFromPayload(payload []byte) []byte {
	if output := gjson.GetBytes(payload, "response.output"); output.Exists() && output.IsArray() {
		return cloneResponsesAffinityBytes([]byte(output.Raw))
	}
	if output := gjson.GetBytes(payload, "output"); output.Exists() && output.IsArray() {
		return cloneResponsesAffinityBytes([]byte(output.Raw))
	}
	return nil
}

func observeResponsesAuthAffinity(data <-chan []byte, selectedAuthID func() string, selectedRequest func() []byte) <-chan []byte {
	if data == nil {
		return nil
	}
	out := make(chan []byte)
	go func() {
		defer close(out)
		for chunk := range data {
			if selectedAuthID != nil {
				var requestPayload []byte
				if selectedRequest != nil {
					requestPayload = selectedRequest()
				}
				responsesHTTPAuthAffinity.bindPayload(selectedAuthID(), requestPayload, chunk)
			}
			out <- chunk
		}
	}()
	return out
}
