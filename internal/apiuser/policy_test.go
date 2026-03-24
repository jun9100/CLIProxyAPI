package apiuser

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestNewManagerLoadsManagedUsersFromConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
api-keys:
  - unmanaged-top-level-key
api-user-management:
  version: 2
  updated-at: "2026-03-21T00:00:00Z"
  users:
    - id: user-1
      name: Team One
      api-key: managed-key-1
      enabled: true
      available-models:
        - gpt-5.4
        - gpt-5.4
        - "  gpt-5.3-codex  "
      quota-metric: requests
      quota-limit: 12
      limit-5h: 5
      limit-weekly: 9
      limit-total: 11
      created-at: "2026-03-20T00:00:00Z"
      expires-at: "2026-04-20T00:00:00Z"
    - id: user-2
      name: Team Two
      api-key: managed-key-2
`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	manager := NewManager(cfg.APIUserManagement)
	if !manager.ManagedOnly() {
		t.Fatalf("ManagedOnly() = false, want true")
	}

	user, ok := manager.Lookup("managed-key-1")
	if !ok {
		t.Fatal("Lookup(managed-key-1) = missing, want user")
	}
	if user.Name != "Team One" {
		t.Fatalf("user.Name = %q, want %q", user.Name, "Team One")
	}
	if user.QuotaMetric != QuotaMetricRequests {
		t.Fatalf("user.QuotaMetric = %q, want %q", user.QuotaMetric, QuotaMetricRequests)
	}
	if user.QuotaLimit != 12 {
		t.Fatalf("user.QuotaLimit = %d, want 12", user.QuotaLimit)
	}
	if user.Limit5h != 5 {
		t.Fatalf("user.Limit5h = %d, want 5", user.Limit5h)
	}
	if user.LimitWeekly != 9 {
		t.Fatalf("user.LimitWeekly = %d, want 9", user.LimitWeekly)
	}
	if user.LimitTotal != 11 {
		t.Fatalf("user.LimitTotal = %d, want 11", user.LimitTotal)
	}
	if len(user.AvailableModels) != 2 {
		t.Fatalf("len(user.AvailableModels) = %d, want 2", len(user.AvailableModels))
	}
	if !manager.IsModelAllowed("managed-key-1", "gpt-5.4") {
		t.Fatalf("IsModelAllowed(managed-key-1, gpt-5.4) = false, want true")
	}
	if !manager.IsModelAllowed("managed-key-1", "gpt-5.3-codex") {
		t.Fatalf("IsModelAllowed(managed-key-1, gpt-5.3-codex) = false, want true")
	}

	secondUser, ok := manager.Lookup("managed-key-2")
	if !ok {
		t.Fatal("Lookup(managed-key-2) = missing, want user")
	}
	if secondUser.QuotaMetric != QuotaMetricTokens {
		t.Fatalf("secondUser.QuotaMetric = %q, want %q", secondUser.QuotaMetric, QuotaMetricTokens)
	}
	if secondUser.QuotaLimit != DefaultQuotaLimit {
		t.Fatalf("secondUser.QuotaLimit = %d, want %d", secondUser.QuotaLimit, DefaultQuotaLimit)
	}
}

func TestCheckRuntimeAllowsTrafficWhenNoManagedSectionExists(t *testing.T) {
	t.Parallel()

	decision := NewManager(nil).CheckRuntime(Request{
		APIKey: "plain-top-level-key",
		Path:   "/v1/chat/completions",
		Method: "POST",
		Model:  "gpt-5.4",
	}, usage.StatisticsSnapshot{}, time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC))

	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false: %+v", decision)
	}
}

func TestCheckRuntimeRejectsUnmanagedDisabledExpiredAndDisallowedRequests(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC)
	manager := NewManager(&config.APIUserManagementConfig{
		Users: []config.APIUserManagementUser{
			{
				Name:            "active",
				APIKey:          "managed-active",
				AvailableModels: []string{"gpt-5.4"},
			},
			{
				Name:    "disabled",
				APIKey:  "managed-disabled",
				Enabled: boolPtr(false),
			},
			{
				Name:      "expired",
				APIKey:    "managed-expired",
				ExpiresAt: "2026-03-20T00:00:00Z",
			},
		},
	})

	tests := []struct {
		name       string
		request    Request
		wantStatus int
		wantCode   string
	}{
		{
			name: "unmanaged key",
			request: Request{
				APIKey: "plain-top-level-key",
				Path:   "/v1/models",
				Method: "GET",
			},
			wantStatus: 403,
			wantCode:   ErrorCodeAPIKeyUnmanaged,
		},
		{
			name: "disabled key",
			request: Request{
				APIKey: "managed-disabled",
				Path:   "/v1/chat/completions",
				Method: "POST",
				Model:  "gpt-5.4",
			},
			wantStatus: 403,
			wantCode:   ErrorCodeAPIKeyDisabled,
		},
		{
			name: "expired key",
			request: Request{
				APIKey: "managed-expired",
				Path:   "/v1/chat/completions",
				Method: "POST",
				Model:  "gpt-5.4",
			},
			wantStatus: 403,
			wantCode:   ErrorCodeAPIKeyExpired,
		},
		{
			name: "disallowed model",
			request: Request{
				APIKey: "managed-active",
				Path:   "/v1/chat/completions",
				Method: "POST",
				Model:  "gpt-5.3-codex",
			},
			wantStatus: 403,
			wantCode:   ErrorCodeModelNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision := manager.CheckRuntime(tt.request, usage.StatisticsSnapshot{}, now)
			if !decision.Blocked {
				t.Fatalf("decision.Blocked = false, want true")
			}
			if decision.StatusCode != tt.wantStatus {
				t.Fatalf("decision.StatusCode = %d, want %d", decision.StatusCode, tt.wantStatus)
			}
			if decision.Code != tt.wantCode {
				t.Fatalf("decision.Code = %q, want %q", decision.Code, tt.wantCode)
			}
		})
	}
}

func TestCheckRuntimeEnforcesUsageWindows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	manager := NewManager(&config.APIUserManagementConfig{
		Users: []config.APIUserManagementUser{
			{
				Name:        "requests-limited",
				APIKey:      "requests-key",
				QuotaMetric: QuotaMetricRequests,
				Limit5h:     int64Ptr(3),
				LimitWeekly: int64Ptr(5),
				LimitTotal:  int64Ptr(6),
				QuotaLimit:  int64Ptr(7),
			},
			{
				Name:        "tokens-limited",
				APIKey:      "tokens-key",
				QuotaMetric: QuotaMetricTokens,
				Limit5h:     int64Ptr(40),
				LimitWeekly: int64Ptr(90),
				LimitTotal:  int64Ptr(120),
				QuotaLimit:  int64Ptr(130),
			},
		},
	})

	snapshot := usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"requests-key": {
				TotalRequests: 6,
				Models: map[string]usage.ModelSnapshot{
					"gpt-5.4": {
						Details: []usage.RequestDetail{
							{Timestamp: now.Add(-1 * time.Hour)},
							{Timestamp: now.Add(-2 * time.Hour)},
							{Timestamp: now.Add(-3 * time.Hour)},
							{Timestamp: now.Add(-24 * time.Hour)},
							{Timestamp: now.Add(-48 * time.Hour)},
							{Timestamp: now.Add(-8 * 24 * time.Hour)},
						},
					},
				},
			},
			"tokens-key": {
				TotalTokens: 120,
				Models: map[string]usage.ModelSnapshot{
					"gpt-5.4": {
						Details: []usage.RequestDetail{
							{Timestamp: now.Add(-1 * time.Hour), Tokens: usage.TokenStats{TotalTokens: 20}},
							{Timestamp: now.Add(-2 * time.Hour), Tokens: usage.TokenStats{TotalTokens: 20}},
							{Timestamp: now.Add(-24 * time.Hour), Tokens: usage.TokenStats{TotalTokens: 30}},
							{Timestamp: now.Add(-48 * time.Hour), Tokens: usage.TokenStats{TotalTokens: 20}},
							{Timestamp: now.Add(-6 * 24 * time.Hour), Tokens: usage.TokenStats{TotalTokens: 30}},
						},
					},
				},
			},
		},
	}

	requestDecision := manager.CheckRuntime(Request{
		APIKey: "requests-key",
		Path:   "/v1/chat/completions",
		Method: "POST",
		Model:  "gpt-5.4",
	}, snapshot, now)
	if !requestDecision.Blocked {
		t.Fatalf("requestDecision.Blocked = false, want true")
	}
	if requestDecision.StatusCode != 429 {
		t.Fatalf("requestDecision.StatusCode = %d, want 429", requestDecision.StatusCode)
	}
	if requestDecision.LimitKey != "limit-5h" {
		t.Fatalf("requestDecision.LimitKey = %q, want %q", requestDecision.LimitKey, "limit-5h")
	}
	if requestDecision.Used != 3 {
		t.Fatalf("requestDecision.Used = %d, want 3", requestDecision.Used)
	}

	tokenDecision := manager.CheckRuntime(Request{
		APIKey: "tokens-key",
		Path:   "/v1/responses",
		Method: "POST",
		Model:  "gpt-5.4",
	}, snapshot, now)
	if !tokenDecision.Blocked {
		t.Fatalf("tokenDecision.Blocked = false, want true")
	}
	if tokenDecision.StatusCode != 429 {
		t.Fatalf("tokenDecision.StatusCode = %d, want 429", tokenDecision.StatusCode)
	}
	if tokenDecision.LimitKey != "limit-5h" {
		t.Fatalf("tokenDecision.LimitKey = %q, want %q", tokenDecision.LimitKey, "limit-5h")
	}
	if tokenDecision.Metric != QuotaMetricTokens {
		t.Fatalf("tokenDecision.Metric = %q, want %q", tokenDecision.Metric, QuotaMetricTokens)
	}
	if tokenDecision.Used != 40 {
		t.Fatalf("tokenDecision.Used = %d, want 40", tokenDecision.Used)
	}
}

func boolPtr(v bool) *bool { return &v }

func int64Ptr(v int64) *int64 { return &v }
