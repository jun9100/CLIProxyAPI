package api

import (
	"reflect"
	"testing"

	apiuser "github.com/router-for-me/CLIProxyAPI/v6/internal/apiuser"
)

func TestObserveAPIUserQuotaRecoveryCleansRecoveredKeyOnce(t *testing.T) {
	var cleaned []string

	server := &Server{
		apiUserQuotaRecovery: apiuser.NewQuotaRecoveryTracker(),
		apiUserRuntimeCleanup: apiuser.NewRuntimeCleanupRegistry(nil, func(owner string) {
			cleaned = append(cleaned, owner)
		}),
	}

	server.observeAPIUserQuotaRecovery("sk-a", apiuser.Decision{Blocked: true, Code: apiuser.ErrorCodeQuotaExceeded})
	server.observeAPIUserQuotaRecovery("sk-a", apiuser.Decision{})
	server.observeAPIUserQuotaRecovery("sk-a", apiuser.Decision{})

	if !reflect.DeepEqual(cleaned, []string{"sk-a"}) {
		t.Fatalf("cleaned owners = %v, want [sk-a]", cleaned)
	}
}

func TestObserveAPIUserQuotaRecoveryScopesCleanupToRecoveredKey(t *testing.T) {
	var cleaned []string

	server := &Server{
		apiUserQuotaRecovery: apiuser.NewQuotaRecoveryTracker(),
		apiUserRuntimeCleanup: apiuser.NewRuntimeCleanupRegistry(nil, func(owner string) {
			cleaned = append(cleaned, owner)
		}),
	}

	server.observeAPIUserQuotaRecovery("sk-a", apiuser.Decision{Blocked: true, Code: apiuser.ErrorCodeQuotaExceeded})
	server.observeAPIUserQuotaRecovery("sk-b", apiuser.Decision{})
	server.observeAPIUserQuotaRecovery("sk-a", apiuser.Decision{})

	if !reflect.DeepEqual(cleaned, []string{"sk-a"}) {
		t.Fatalf("cleaned owners = %v, want [sk-a]", cleaned)
	}
}
