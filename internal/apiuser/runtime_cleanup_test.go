package apiuser

import (
	"reflect"
	"sort"
	"testing"
)

func TestRuntimeCleanupRegistryCleanupAPIKeyClosesTrackedSessionsAndAffinity(t *testing.T) {
	var (
		closedSessions []string
		clearedOwners  []string
	)

	registry := NewRuntimeCleanupRegistry(func(sessionID string) {
		closedSessions = append(closedSessions, sessionID)
	}, func(owner string) {
		clearedOwners = append(clearedOwners, owner)
	})

	registry.TrackSession("sk-a", "sess-1")
	registry.TrackSession("sk-b", "sess-2")
	registry.TrackSession("sk-a", "sess-3")

	registry.CleanupAPIKey("sk-a")

	sort.Strings(closedSessions)
	if !reflect.DeepEqual(closedSessions, []string{"sess-1", "sess-3"}) {
		t.Fatalf("closed sessions = %v, want [sess-1 sess-3]", closedSessions)
	}
	if !reflect.DeepEqual(clearedOwners, []string{"sk-a"}) {
		t.Fatalf("cleared owners = %v, want [sk-a]", clearedOwners)
	}

	registry.CleanupAPIKey("sk-a")

	sort.Strings(closedSessions)
	if !reflect.DeepEqual(closedSessions, []string{"sess-1", "sess-3"}) {
		t.Fatalf("closed sessions after repeated cleanup = %v, want [sess-1 sess-3]", closedSessions)
	}
	if !reflect.DeepEqual(clearedOwners, []string{"sk-a", "sk-a"}) {
		t.Fatalf("cleared owners after repeated cleanup = %v, want [sk-a sk-a]", clearedOwners)
	}
}

func TestRuntimeCleanupRegistryUntrackSessionRemovesOnlyThatSession(t *testing.T) {
	var closedSessions []string

	registry := NewRuntimeCleanupRegistry(func(sessionID string) {
		closedSessions = append(closedSessions, sessionID)
	}, nil)

	registry.TrackSession("sk-a", "sess-1")
	registry.TrackSession("sk-a", "sess-2")
	registry.UntrackSession("sess-1")

	registry.CleanupAPIKey("sk-a")

	if !reflect.DeepEqual(closedSessions, []string{"sess-2"}) {
		t.Fatalf("closed sessions = %v, want [sess-2]", closedSessions)
	}
}

func TestQuotaRecoveryTrackerRecoversExactlyOnceAfterQuotaBlock(t *testing.T) {
	tracker := NewQuotaRecoveryTracker()

	if recovered := tracker.Observe("sk-a", Decision{Blocked: true, Code: ErrorCodeQuotaExceeded}); recovered {
		t.Fatalf("quota block should not report recovery")
	}
	if recovered := tracker.Observe("sk-a", Decision{}); !recovered {
		t.Fatalf("first clean pass after quota block should report recovery")
	}
	if recovered := tracker.Observe("sk-a", Decision{}); recovered {
		t.Fatalf("second clean pass should not report recovery again")
	}
}

func TestQuotaRecoveryTrackerIgnoresNonQuotaBlocks(t *testing.T) {
	tracker := NewQuotaRecoveryTracker()

	if recovered := tracker.Observe("sk-a", Decision{Blocked: true, Code: ErrorCodeAPIKeyDisabled}); recovered {
		t.Fatalf("non-quota block should not report recovery")
	}
	if recovered := tracker.Observe("sk-a", Decision{}); recovered {
		t.Fatalf("clean pass after non-quota block should not report quota recovery")
	}
}
