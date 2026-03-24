package openai

import (
	"testing"
	"time"
)

func TestResponsesAuthAffinityClearOwnerRemovesOnlyMatchingEntries(t *testing.T) {
	store := newResponsesAuthAffinityStore(time.Hour)

	store.bindPayload("sk-a", "auth-a", []byte(`{"id":"resp-a","prompt_cache_key":"cache-a"}`))
	store.bindPayload("sk-b", "auth-b", []byte(`{"id":"resp-b","prompt_cache_key":"cache-b"}`))

	if got := store.lookup("resp-a", ""); got != "auth-a" {
		t.Fatalf("lookup resp-a = %q, want %q", got, "auth-a")
	}
	if got := store.lookup("", "cache-b"); got != "auth-b" {
		t.Fatalf("lookup cache-b = %q, want %q", got, "auth-b")
	}

	store.clearOwner("sk-a")

	if got := store.lookup("resp-a", ""); got != "" {
		t.Fatalf("lookup resp-a after clear = %q, want empty", got)
	}
	if got := store.lookup("", "cache-a"); got != "" {
		t.Fatalf("lookup cache-a after clear = %q, want empty", got)
	}
	if got := store.lookup("resp-b", ""); got != "auth-b" {
		t.Fatalf("lookup resp-b after clear = %q, want %q", got, "auth-b")
	}
}

func TestResponsesAuthAffinityClearOwnerIsIdempotent(t *testing.T) {
	store := newResponsesAuthAffinityStore(time.Hour)

	store.bindPayload("sk-a", "auth-a", []byte(`{"id":"resp-a"}`))
	store.clearOwner("sk-a")
	store.clearOwner("sk-a")

	if got := store.lookup("resp-a", ""); got != "" {
		t.Fatalf("lookup resp-a after repeated clear = %q, want empty", got)
	}
}
