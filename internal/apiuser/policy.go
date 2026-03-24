package apiuser

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
	QuotaMetricTokens   = "tokens"
	QuotaMetricRequests = "requests"
	DefaultQuotaLimit   = int64(300000)

	ErrorCodeAPIKeyUnmanaged = "api_key_unmanaged"
	ErrorCodeAPIKeyDisabled  = "api_key_disabled"
	ErrorCodeAPIKeyExpired   = "api_key_expired"
	ErrorCodeModelNotAllowed = "model_not_allowed"
	ErrorCodeQuotaExceeded   = "quota_exceeded"
)

// Request describes the runtime request subject to api-user-management policy.
type Request struct {
	APIKey string
	Model  string
	Path   string
	Method string
}

// Decision is the result of evaluating one runtime request.
type Decision struct {
	Blocked    bool
	StatusCode int
	Code       string
	Message    string
	LimitKey   string
	Metric     string
	Used       int64
	Limit      int64
}

// ManagedUser is the normalized runtime representation of a managed client key.
type ManagedUser struct {
	ID              string
	Name            string
	Remark          string
	Notes           string
	APIKey          string
	Enabled         bool
	AvailableModels []string
	QuotaMetric     string
	QuotaLimit      int64
	Limit5h         int64
	LimitWeekly     int64
	LimitTotal      int64
	ValidDays       float64
	CreatedAt       string
	UpdatedAt       string
	ExpiresAt       string

	allowedModelSet map[string]struct{}
}

// Manager evaluates runtime policy for api-user-management.
type Manager struct {
	managedOnly bool
	usersByKey  map[string]ManagedUser
}

type usageTotals struct {
	requests5h     int64
	tokens5h       int64
	requestsWeekly int64
	tokensWeekly   int64
	requestsTotal  int64
	tokensTotal    int64
}

// NewManager builds a hot-reload-safe read-only manager from config state.
func NewManager(section *config.APIUserManagementConfig) *Manager {
	manager := &Manager{
		managedOnly: section != nil,
		usersByKey:  make(map[string]ManagedUser),
	}
	if section == nil {
		return manager
	}

	for index, raw := range section.Users {
		user, ok := normalizeUser(raw, index)
		if !ok {
			continue
		}
		if _, exists := manager.usersByKey[user.APIKey]; exists {
			continue
		}
		manager.usersByKey[user.APIKey] = user
	}

	return manager
}

// ManagedOnly reports whether the config explicitly contains an api-user-management section.
func (m *Manager) ManagedOnly() bool {
	return m != nil && m.managedOnly
}

// Lookup returns the normalized managed user for the given client API key.
func (m *Manager) Lookup(apiKey string) (ManagedUser, bool) {
	if m == nil {
		return ManagedUser{}, false
	}
	user, ok := m.usersByKey[strings.TrimSpace(apiKey)]
	return user, ok
}

// IsModelAllowed reports whether the given model is permitted for this key.
func (m *Manager) IsModelAllowed(apiKey, model string) bool {
	user, ok := m.Lookup(apiKey)
	if !ok {
		return false
	}
	if len(user.allowedModelSet) == 0 {
		return true
	}
	_, allowed := user.allowedModelSet[strings.TrimSpace(model)]
	return allowed
}

// AllowedModels returns a copy of the normalized allowlist for the given key.
func (m *Manager) AllowedModels(apiKey string) []string {
	user, ok := m.Lookup(apiKey)
	if !ok || len(user.AvailableModels) == 0 {
		return nil
	}
	result := make([]string, len(user.AvailableModels))
	copy(result, user.AvailableModels)
	return result
}

// CheckRuntime evaluates one runtime request against managed-key policy and usage.
func (m *Manager) CheckRuntime(req Request, snapshot usage.StatisticsSnapshot, now time.Time) Decision {
	if m == nil || !m.managedOnly {
		return Decision{}
	}

	user, ok := m.Lookup(req.APIKey)
	if !ok {
		return blockedDecision(http.StatusForbidden, ErrorCodeAPIKeyUnmanaged, "API key is not managed by api-user-management policy")
	}
	if !user.Enabled {
		return blockedDecision(http.StatusForbidden, ErrorCodeAPIKeyDisabled, "API key is disabled by api-user-management policy")
	}
	if isExpired(user.ExpiresAt, now) {
		return blockedDecision(http.StatusForbidden, ErrorCodeAPIKeyExpired, "API key is expired by api-user-management policy")
	}
	if model := strings.TrimSpace(req.Model); model != "" && !m.IsModelAllowed(req.APIKey, model) {
		return blockedDecision(http.StatusForbidden, ErrorCodeModelNotAllowed, "API key is not allowed to use model: "+model)
	}
	if !shouldApplyQuota(req.Path, req.Method) {
		return Decision{}
	}

	totals := aggregateUsage(snapshot, user.APIKey, now)
	metric := user.QuotaMetric
	used5h := totals.tokens5h
	usedWeekly := totals.tokensWeekly
	usedTotal := totals.tokensTotal
	if metric == QuotaMetricRequests {
		used5h = totals.requests5h
		usedWeekly = totals.requestsWeekly
		usedTotal = totals.requestsTotal
	}

	if user.Limit5h > 0 && used5h >= user.Limit5h {
		return quotaExceededDecision("limit-5h", metric, used5h, user.Limit5h)
	}
	if user.LimitWeekly > 0 && usedWeekly >= user.LimitWeekly {
		return quotaExceededDecision("limit-weekly", metric, usedWeekly, user.LimitWeekly)
	}
	if user.LimitTotal > 0 && usedTotal >= user.LimitTotal {
		return quotaExceededDecision("limit-total", metric, usedTotal, user.LimitTotal)
	}
	if user.QuotaLimit > 0 && usedTotal >= user.QuotaLimit {
		return quotaExceededDecision("quota-limit", metric, usedTotal, user.QuotaLimit)
	}

	return Decision{}
}

func normalizeUser(raw config.APIUserManagementUser, index int) (ManagedUser, bool) {
	name := firstNonEmpty(raw.Name, raw.User, raw.Username)
	apiKey := firstNonEmpty(raw.APIKey, raw.Key)
	if name == "" || apiKey == "" {
		return ManagedUser{}, false
	}

	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	availableModels := normalizeStringList(raw.AvailableModels, raw.AllowedModels, raw.Models)
	remark := firstNonEmpty(raw.Remark, raw.RemarkName, raw.Notes)
	createdAt := normalizeTimeString(raw.CreatedAt)
	validDays := normalizePositiveFloat64(raw.ValidDays)
	expiresAt := normalizeTimeString(firstNonEmpty(raw.ExpiresAt, raw.ExpiredAt))
	if expiresAt == "" && createdAt != "" && validDays > 0 {
		if createdTime, ok := parseTime(createdAt); ok {
			expiresAt = createdTime.Add(time.Duration(validDays * 24 * float64(time.Hour))).UTC().Format(time.RFC3339)
		}
	}

	return ManagedUser{
		ID:              fallbackString(strings.TrimSpace(raw.ID), "api-user-"+itoa(index+1)),
		Name:            name,
		Remark:          remark,
		Notes:           remark,
		APIKey:          apiKey,
		Enabled:         enabled,
		AvailableModels: availableModels,
		QuotaMetric:     normalizeQuotaMetric(firstNonEmpty(raw.QuotaMetric, raw.Metric)),
		QuotaLimit:      normalizeQuotaLimit(raw.QuotaLimit, raw.Limit),
		Limit5h:         normalizeOptionalLimit(raw.Limit5h),
		LimitWeekly:     normalizeOptionalLimit(firstNonNilInt64(raw.LimitWeekly, raw.LimitWeek)),
		LimitTotal:      normalizeOptionalLimit(raw.LimitTotal),
		ValidDays:       validDays,
		CreatedAt:       createdAt,
		UpdatedAt:       normalizeTimeString(raw.UpdatedAt),
		ExpiresAt:       expiresAt,
		allowedModelSet: makeStringSet(availableModels),
	}, true
}

func blockedDecision(statusCode int, code, message string) Decision {
	return Decision{
		Blocked:    true,
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
	}
}

func quotaExceededDecision(limitKey, metric string, used, limit int64) Decision {
	return Decision{
		Blocked:    true,
		StatusCode: http.StatusTooManyRequests,
		Code:       ErrorCodeQuotaExceeded,
		Message:    "API key exceeds " + limitKey,
		LimitKey:   limitKey,
		Metric:     metric,
		Used:       used,
		Limit:      limit,
	}
}

func shouldApplyQuota(pathname, method string) bool {
	if strings.EqualFold(strings.TrimSpace(method), http.MethodOptions) {
		return false
	}

	path := strings.TrimSpace(pathname)
	if path == "/v1/models" || strings.HasPrefix(path, "/v1/models/") {
		return false
	}
	return strings.HasPrefix(path, "/v1/")
}

func aggregateUsage(snapshot usage.StatisticsSnapshot, apiKey string, now time.Time) usageTotals {
	apiSnapshot, ok := snapshot.APIs[strings.TrimSpace(apiKey)]
	if !ok {
		return usageTotals{}
	}

	var totals usageTotals
	hasDetails := false
	cutoff5h := now.Add(-5 * time.Hour)
	cutoffWeekly := now.Add(-7 * 24 * time.Hour)

	for _, modelSnapshot := range apiSnapshot.Models {
		for _, detail := range modelSnapshot.Details {
			if detail.Timestamp.IsZero() {
				continue
			}
			timestamp := detail.Timestamp.UTC()
			if timestamp.After(now) {
				continue
			}
			hasDetails = true

			tokens := detail.Tokens.TotalTokens
			if tokens == 0 {
				tokens = detail.Tokens.InputTokens + detail.Tokens.OutputTokens + detail.Tokens.ReasoningTokens
			}

			totals.requestsTotal++
			totals.tokensTotal += tokens
			if !timestamp.Before(cutoffWeekly) {
				totals.requestsWeekly++
				totals.tokensWeekly += tokens
			}
			if !timestamp.Before(cutoff5h) {
				totals.requests5h++
				totals.tokens5h += tokens
			}
		}
	}

	if hasDetails {
		return totals
	}

	totals.requestsTotal = apiSnapshot.TotalRequests
	totals.tokensTotal = apiSnapshot.TotalTokens
	return totals
}

func isExpired(expiresAt string, now time.Time) bool {
	expiresAt = normalizeTimeString(expiresAt)
	if expiresAt == "" {
		return false
	}
	expiry, ok := parseTime(expiresAt)
	if !ok {
		return false
	}
	return !expiry.After(now)
}

func normalizeQuotaMetric(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), QuotaMetricRequests) {
		return QuotaMetricRequests
	}
	return QuotaMetricTokens
}

func normalizeQuotaLimit(primary, fallback *int64) int64 {
	value := primary
	if value == nil {
		value = fallback
	}
	if value == nil {
		return DefaultQuotaLimit
	}
	if *value < 0 {
		return DefaultQuotaLimit
	}
	return *value
}

func normalizeOptionalLimit(value *int64) int64 {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

func normalizePositiveFloat64(value *float64) float64 {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

func normalizeStringList(lists ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, list := range lists {
		for _, item := range list {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			result = append(result, trimmed)
		}
	}
	return result
}

func makeStringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func normalizeTimeString(value string) string {
	parsed, ok := parseTime(value)
	if !ok {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func parseTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func fallbackString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func firstNonNilInt64(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	buf := [20]byte{}
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	return sign + string(buf[pos:])
}
