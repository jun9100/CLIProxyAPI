package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	apiuser "github.com/router-for-me/CLIProxyAPI/v6/internal/apiuser"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

func TestAPIUserRuntimePolicy_ManagedOpenAIModelsFiltered(t *testing.T) {
	server := newAPIUserPolicyTestServer(t, []string{"managed-openai"}, []proxyconfig.APIUserManagementUser{
		{
			Name:            "managed-openai",
			APIKey:          "managed-openai",
			AvailableModels: []string{"gpt-5.4"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer managed-openai")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	ids := responseModelIDs(t, rr.Body.Bytes())
	if len(ids) != 1 || ids[0] != "gpt-5.4" {
		t.Fatalf("model ids = %#v, want [gpt-5.4]", ids)
	}
}

func TestAPIUserRuntimePolicy_ManagedClaudeModelsFiltered(t *testing.T) {
	server := newAPIUserPolicyTestServer(t, []string{"managed-claude"}, []proxyconfig.APIUserManagementUser{
		{
			Name:            "managed-claude",
			APIKey:          "managed-claude",
			AvailableModels: []string{"claude-sonnet-4-6"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer managed-claude")
	req.Header.Set("User-Agent", "claude-cli/1.0")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	ids := responseModelIDs(t, rr.Body.Bytes())
	if len(ids) != 1 || ids[0] != "claude-sonnet-4-6" {
		t.Fatalf("model ids = %#v, want [claude-sonnet-4-6]", ids)
	}
}

func TestAPIUserRuntimePolicy_RejectsUnmanagedDisabledAndExpiredKeys(t *testing.T) {
	server := newAPIUserPolicyTestServer(t, []string{"managed-openai", "unmanaged-key", "disabled-key", "expired-key"}, []proxyconfig.APIUserManagementUser{
		{
			Name:            "managed-openai",
			APIKey:          "managed-openai",
			AvailableModels: []string{"gpt-5.4"},
		},
		{
			Name:    "disabled",
			APIKey:  "disabled-key",
			Enabled: boolPtr(false),
		},
		{
			Name:      "expired",
			APIKey:    "expired-key",
			ExpiresAt: "2000-01-01T00:00:00Z",
		},
	})

	testCases := []struct {
		name     string
		apiKey   string
		wantCode string
	}{
		{name: "unmanaged", apiKey: "unmanaged-key", wantCode: apiuser.ErrorCodeAPIKeyUnmanaged},
		{name: "disabled", apiKey: "disabled-key", wantCode: apiuser.ErrorCodeAPIKeyDisabled},
		{name: "expired", apiKey: "expired-key", wantCode: apiuser.ErrorCodeAPIKeyExpired},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+tc.apiKey)
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
			}
			if got := gjson.GetBytes(rr.Body.Bytes(), "error").String(); got != tc.wantCode {
				t.Fatalf("error = %q, want %q; body=%s", got, tc.wantCode, rr.Body.String())
			}
		})
	}
}

func TestAPIUserRuntimePolicy_RejectsDisallowedChatModel(t *testing.T) {
	server := newAPIUserPolicyTestServer(t, []string{"managed-openai"}, []proxyconfig.APIUserManagementUser{
		{
			Name:            "managed-openai",
			APIKey:          "managed-openai",
			AvailableModels: []string{"gpt-5.4"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer managed-openai")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := gjson.GetBytes(rr.Body.Bytes(), "error").String(); got != apiuser.ErrorCodeModelNotAllowed {
		t.Fatalf("error = %q, want %q; body=%s", got, apiuser.ErrorCodeModelNotAllowed, rr.Body.String())
	}
}

func TestAPIUserRuntimePolicy_ManualQuotaRecoveryTriggersCleanupBeforeNextRequest(t *testing.T) {
	const apiKey = "managed-quota-recovery"

	server := newAPIUserPolicyTestServer(t, []string{apiKey}, []proxyconfig.APIUserManagementUser{{
		Name:            apiKey,
		APIKey:          apiKey,
		AvailableModels: []string{"gpt-5.4"},
		QuotaMetric:     apiuser.QuotaMetricRequests,
		Limit5h:         int64Ptr(1),
	}})

	var cleaned []string
	server.apiUserRuntimeCleanup = apiuser.NewRuntimeCleanupRegistry(nil, func(owner string) {
		cleaned = append(cleaned, owner)
	})

	stats := usage.GetRequestStatistics()
	stats.MergeSnapshot(usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			apiKey: {
				Models: map[string]usage.ModelSnapshot{
					"gpt-5.4": {
						Details: []usage.RequestDetail{{
							Timestamp: time.Now().UTC(),
							Tokens:    usage.TokenStats{TotalTokens: 1},
						}},
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status = %d, want 429; body=%s", rr.Code, rr.Body.String())
	}
	if len(cleaned) != 0 {
		t.Fatalf("cleanup should not run while still blocked, got %v", cleaned)
	}

	server.apiUserPolicy = apiuser.NewManager(&proxyconfig.APIUserManagementConfig{
		Users: []proxyconfig.APIUserManagementUser{{
			Name:            apiKey,
			APIKey:          apiKey,
			AvailableModels: []string{"gpt-5.4"},
			QuotaMetric:     apiuser.QuotaMetricRequests,
		}},
	})

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello again"}]}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code == http.StatusTooManyRequests {
		t.Fatalf("recovered request remained blocked; body=%s", rr.Body.String())
	}
	if !reflect.DeepEqual(cleaned, []string{apiKey}) {
		t.Fatalf("cleaned owners = %v, want [%s]", cleaned, apiKey)
	}
}

func newAPIUserPolicyTestServer(t *testing.T, apiKeys []string, users []proxyconfig.APIUserManagementUser) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: apiKeys,
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: true,
		APIUserManagement: &proxyconfig.APIUserManagementConfig{
			Users: users,
		},
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)

	modelRegistry := registry.GetGlobalRegistry()
	openAIClientID := "api-user-policy-openai-" + t.Name()
	claudeClientID := "api-user-policy-claude-" + t.Name()
	modelRegistry.RegisterClient(openAIClientID, "openai", []*registry.ModelInfo{
		{ID: "gpt-5.4", Object: "model", Created: 1, OwnedBy: "openai"},
		{ID: "gpt-5.3-codex", Object: "model", Created: 2, OwnedBy: "openai"},
	})
	modelRegistry.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-6", Object: "model", Created: 1, OwnedBy: "anthropic"},
		{ID: "claude-sonnet-4-5-20250929", Object: "model", Created: 2, OwnedBy: "anthropic"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(openAIClientID)
		modelRegistry.UnregisterClient(claudeClientID)
	})

	return server
}

func responseModelIDs(t *testing.T, body []byte) []string {
	t.Helper()

	results := gjson.GetBytes(body, "data").Array()
	ids := make([]string, 0, len(results))
	for _, result := range results {
		id := result.Get("id").String()
		if id == "" {
			t.Fatalf("model missing id in body=%s", string(body))
		}
		ids = append(ids, id)
	}
	return ids
}

func boolPtr(v bool) *bool { return &v }

func int64Ptr(v int64) *int64 { return &v }
