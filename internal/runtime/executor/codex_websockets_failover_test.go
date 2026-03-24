package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestCodexWebsocketBootstrapFailureIsRetryableOnQuotaStatus(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests}
	payload := []byte(`{"error":{"type":"usage_limit_reached","message":"quota exceeded","resets_in_seconds":9}}`)

	gotErr, retryable := classifyCodexWebsocketBootstrapFailure(resp, payload, errors.New("websocket handshake failed"))

	if !retryable {
		t.Fatal("expected quota bootstrap failure to be retryable")
	}
	if gotErr == nil {
		t.Fatal("expected quota bootstrap failure error")
	}
	statusErr, ok := gotErr.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error, got %T", gotErr)
	}
	if got := statusErr.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	retryAfterErr, ok := gotErr.(interface{ RetryAfter() *time.Duration })
	if !ok {
		t.Fatalf("expected retry-after provider, got %T", gotErr)
	}
	retryAfter := retryAfterErr.RetryAfter()
	if retryAfter == nil {
		t.Fatal("expected retry-after to be preserved")
	}
	if got := *retryAfter; got != 9*time.Second {
		t.Fatalf("retry-after = %v, want %v", got, 9*time.Second)
	}
}

func TestCodexWebsocketBootstrapFailureIsRetryableOnUnauthorizedStatus(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusUnauthorized}
	payload := []byte(`{"error":{"type":"invalid_api_key","message":"unauthorized"}}`)

	gotErr, retryable := classifyCodexWebsocketBootstrapFailure(resp, payload, errors.New("websocket handshake failed"))

	if !retryable {
		t.Fatal("expected unauthorized bootstrap failure to be retryable")
	}
	if gotErr == nil {
		t.Fatal("expected unauthorized bootstrap failure error")
	}
	statusErr, ok := gotErr.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error, got %T", gotErr)
	}
	if got := statusErr.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
	retryAfterErr, ok := gotErr.(interface{ RetryAfter() *time.Duration })
	if !ok {
		t.Fatalf("expected retry-after provider, got %T", gotErr)
	}
	if got := retryAfterErr.RetryAfter(); got != nil {
		t.Fatalf("retry-after = %v, want nil", *got)
	}
}
