package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type responsesAffinityHTTPExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

type responsesFallbackHTTPExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	payloads [][]byte
}

type responsesSilentContinuationHTTPExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	payloads [][]byte
}

func (e *responsesAffinityHTTPExecutor) Identifier() string { return "test-provider" }

func (e *responsesAffinityHTTPExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.mu.Unlock()

	payload := fmt.Sprintf(`{"id":"resp-%s","object":"response","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"%s"}]}],"prompt_cache_key":"cache-%s"}`, authID, authID, authID)
	return coreexecutor.Response{Payload: []byte(payload)}, nil
}

func (e *responsesAffinityHTTPExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesAffinityHTTPExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesAffinityHTTPExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesAffinityHTTPExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesAffinityHTTPExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *responsesFallbackHTTPExecutor) Identifier() string { return "test-provider" }

func (e *responsesFallbackHTTPExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	payload := append([]byte(nil), req.Payload...)

	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.payloads = append(e.payloads, payload)
	e.mu.Unlock()

	if gjson.GetBytes(payload, "previous_response_id").Exists() {
		return coreexecutor.Response{}, &coreauth.Error{
			HTTPStatus: http.StatusBadRequest,
			Message:    `{"detail":"Unsupported parameter: previous_response_id"}`,
		}
	}

	input := gjson.GetBytes(payload, "input").Array()
	switch len(input) {
	case 1:
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp-auth1","object":"response","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"记住了。"}]}]}`),
		}, nil
	case 3:
		if got := input[0].Get("content.0.text").String(); got != "记住我的暗号是青山" {
			return coreexecutor.Response{}, fmt.Errorf("unexpected merged first input text: %s", got)
		}
		if got := input[1].Get("content.0.text").String(); got != "记住了。" {
			return coreexecutor.Response{}, fmt.Errorf("unexpected merged assistant text: %s", got)
		}
		if got := input[2].Get("content.0.text").String(); got != "暗号是什么" {
			return coreexecutor.Response{}, fmt.Errorf("unexpected merged follow-up text: %s", got)
		}
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp-auth1-followup","object":"response","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"青山"}]}]}`),
		}, nil
	default:
		return coreexecutor.Response{}, fmt.Errorf("unexpected input length: %d", len(input))
	}
}

func (e *responsesFallbackHTTPExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesFallbackHTTPExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesFallbackHTTPExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesFallbackHTTPExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesFallbackHTTPExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *responsesFallbackHTTPExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = append([]byte(nil), e.payloads[i]...)
	}
	return out
}

func (e *responsesSilentContinuationHTTPExecutor) Identifier() string { return "sg" }

func (e *responsesSilentContinuationHTTPExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	payload := append([]byte(nil), req.Payload...)

	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.payloads = append(e.payloads, payload)
	e.mu.Unlock()

	if gjson.GetBytes(payload, "previous_response_id").Exists() {
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp-auth1-followup","object":"response","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我不知道"}]}]}`),
		}, nil
	}

	input := gjson.GetBytes(payload, "input").Array()
	switch len(input) {
	case 1:
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp-auth1","object":"response","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"记住了。"}]}]}`),
		}, nil
	case 3:
		if got := input[0].Get("content.0.text").String(); got != "请记住一个普通事实：我最喜欢的动物是狐狸" {
			return coreexecutor.Response{}, fmt.Errorf("unexpected merged first input text: %s", got)
		}
		if got := input[1].Get("content.0.text").String(); got != "记住了。" {
			return coreexecutor.Response{}, fmt.Errorf("unexpected merged assistant text: %s", got)
		}
		if got := input[2].Get("content.0.text").String(); got != "我最喜欢的动物是什么" {
			return coreexecutor.Response{}, fmt.Errorf("unexpected merged follow-up text: %s", got)
		}
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp-auth1-followup","object":"response","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"狐狸"}]}]}`),
		}, nil
	default:
		return coreexecutor.Response{}, fmt.Errorf("unexpected input length: %d", len(input))
	}
}

func (e *responsesSilentContinuationHTTPExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesSilentContinuationHTTPExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesSilentContinuationHTTPExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesSilentContinuationHTTPExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesSilentContinuationHTTPExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *responsesSilentContinuationHTTPExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = append([]byte(nil), e.payloads[i]...)
	}
	return out
}

type responsesAffinityWebsocketExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

func (e *responsesAffinityWebsocketExecutor) Identifier() string { return "test-provider" }

func (e *responsesAffinityWebsocketExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesAffinityWebsocketExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.mu.Unlock()

	payload := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s","prompt_cache_key":"cache-%s","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"%s"}]}]}}`, authID, authID, authID)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(payload)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *responsesAffinityWebsocketExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesAffinityWebsocketExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesAffinityWebsocketExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesAffinityWebsocketExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func TestOpenAIResponsesHTTPPreviousResponseIDPinsSelectedAuth(t *testing.T) {
	responsesHTTPAuthAffinity.reset()
	t.Cleanup(responsesHTTPAuthAffinity.reset)
	gin.SetMode(gin.TestMode)

	executor := &responsesAffinityHTTPExecutor{}
	manager := coreauth.NewManager(nil, &orderedWebsocketSelector{order: []string{"auth1", "auth2"}}, nil)
	manager.RegisterExecutor(executor)

	for _, auth := range []*coreauth.Auth{
		{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive},
		{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("auth1")
		registry.GetGlobalRegistry().UnregisterClient("auth2")
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req1.Header.Set("Content-Type", "application/json")
	resp1 := httptest.NewRecorder()
	router.ServeHTTP(resp1, req1)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}
	firstID := gjson.Get(resp1.Body.String(), "id").String()
	if firstID != "resp-auth1" {
		t.Fatalf("first response id = %q, want %q", firstID, "resp-auth1")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"test-model","previous_response_id":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"follow-up"}]}]}`, firstID)))
	req2.Header.Set("Content-Type", "application/json")
	resp2 := httptest.NewRecorder()
	router.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp2.Code, http.StatusOK)
	}
	if got := gjson.Get(resp2.Body.String(), "output.0.content.0.text").String(); got != "auth1" {
		t.Fatalf("second response text = %q, want %q", got, "auth1")
	}
	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth1" || got[1] != "auth1" {
		t.Fatalf("auth selection = %v, want [auth1 auth1]", got)
	}
}

func TestResponsesWebsocketPreviousResponseIDPinsSelectedAuthAcrossReconnect(t *testing.T) {
	responsesHTTPAuthAffinity.reset()
	t.Cleanup(responsesHTTPAuthAffinity.reset)
	gin.SetMode(gin.TestMode)

	executor := &responsesAffinityWebsocketExecutor{}
	manager := coreauth.NewManager(nil, &orderedWebsocketSelector{order: []string{"auth1", "auth2"}}, nil)
	manager.RegisterExecutor(executor)

	for _, auth := range []*coreauth.Auth{
		{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}},
		{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("auth1")
		registry.GetGlobalRegistry().UnregisterClient("auth2")
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"

	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial first websocket: %v", err)
	}
	if err := conn1.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":"hello"}`)); err != nil {
		t.Fatalf("write first websocket request: %v", err)
	}
	firstPayload := readResponsesWebsocketPayload(t, conn1)
	_ = conn1.Close()

	firstID := gjson.GetBytes(firstPayload, "response.id").String()
	if firstID != "resp-auth1" {
		t.Fatalf("first websocket response id = %q, want %q", firstID, "resp-auth1")
	}

	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial second websocket: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	secondRequest := fmt.Sprintf(`{"type":"response.create","model":"test-model","previous_response_id":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"follow-up"}]}]}`, firstID)
	if err := conn2.WriteMessage(websocket.TextMessage, []byte(secondRequest)); err != nil {
		t.Fatalf("write second websocket request: %v", err)
	}
	secondPayload := readResponsesWebsocketPayload(t, conn2)
	if got := gjson.GetBytes(secondPayload, "response.output.0.content.0.text").String(); got != "auth1" {
		t.Fatalf("second websocket response text = %q, want %q", got, "auth1")
	}
	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth1" || got[1] != "auth1" {
		t.Fatalf("websocket auth selection = %v, want [auth1 auth1]", got)
	}
}

func TestOpenAIResponsesHTTPPreviousResponseIDFallsBackToMergedInputOnUnsupportedContinuation(t *testing.T) {
	responsesHTTPAuthAffinity.reset()
	t.Cleanup(responsesHTTPAuthAffinity.reset)
	gin.SetMode(gin.TestMode)

	executor := &responsesFallbackHTTPExecutor{}
	manager := coreauth.NewManager(nil, &orderedWebsocketSelector{order: []string{"auth1", "auth2"}}, nil)
	manager.RegisterExecutor(executor)

	for _, auth := range []*coreauth.Auth{
		{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive},
		{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("auth1")
		registry.GetGlobalRegistry().UnregisterClient("auth2")
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"记住我的暗号是青山"}]}]}`))
	req1.Header.Set("Content-Type", "application/json")
	resp1 := httptest.NewRecorder()
	router.ServeHTTP(resp1, req1)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}
	firstID := gjson.Get(resp1.Body.String(), "id").String()
	if firstID != "resp-auth1" {
		t.Fatalf("first response id = %q, want %q", firstID, "resp-auth1")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"test-model","previous_response_id":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"暗号是什么"}]}]}`, firstID)))
	req2.Header.Set("Content-Type", "application/json")
	resp2 := httptest.NewRecorder()
	router.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d, body=%s", resp2.Code, http.StatusOK, resp2.Body.String())
	}
	if got := gjson.Get(resp2.Body.String(), "output.0.content.0.text").String(); got != "青山" {
		t.Fatalf("second response text = %q, want %q", got, "青山")
	}

	if got := executor.AuthIDs(); len(got) != 3 || got[0] != "auth1" || got[1] != "auth1" || got[2] != "auth1" {
		t.Fatalf("auth selection = %v, want [auth1 auth1 auth1]", got)
	}

	payloads := executor.Payloads()
	if len(payloads) != 3 {
		t.Fatalf("payload count = %d, want 3", len(payloads))
	}
	if got := gjson.GetBytes(payloads[1], "previous_response_id").String(); got != "resp-auth1" {
		t.Fatalf("second attempt previous_response_id = %q, want %q", got, "resp-auth1")
	}
	if gjson.GetBytes(payloads[2], "previous_response_id").Exists() {
		t.Fatalf("fallback request still includes previous_response_id: %s", payloads[2])
	}
	input := gjson.GetBytes(payloads[2], "input").Array()
	if len(input) != 3 {
		t.Fatalf("fallback merged input len = %d, want 3", len(input))
	}
	if got := input[1].Get("content.0.text").String(); got != "记住了。" {
		t.Fatalf("fallback merged assistant text = %q, want %q", got, "记住了。")
	}
}

func TestOpenAIResponsesHTTPLocallyMergesHistoryForOpenAICompatibilitySilentContinuationFailure(t *testing.T) {
	responsesHTTPAuthAffinity.reset()
	t.Cleanup(responsesHTTPAuthAffinity.reset)
	gin.SetMode(gin.TestMode)

	executor := &responsesSilentContinuationHTTPExecutor{}
	manager := coreauth.NewManager(nil, &orderedWebsocketSelector{order: []string{"auth1", "auth2"}}, nil)
	manager.RegisterExecutor(executor)

	for _, auth := range []*coreauth.Auth{
		{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"compat_name": "SG", "provider_key": "sg"}},
		{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"compat_name": "SG", "provider_key": "sg"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("auth1")
		registry.GetGlobalRegistry().UnregisterClient("auth2")
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"请记住一个普通事实：我最喜欢的动物是狐狸"}]}]}`))
	req1.Header.Set("Content-Type", "application/json")
	resp1 := httptest.NewRecorder()
	router.ServeHTTP(resp1, req1)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}
	firstID := gjson.Get(resp1.Body.String(), "id").String()
	if firstID != "resp-auth1" {
		t.Fatalf("first response id = %q, want %q", firstID, "resp-auth1")
	}
	affinityState := responsesHTTPAuthAffinity.lookupRequestState([]byte(fmt.Sprintf(`{"previous_response_id":%q}`, firstID)))
	if affinityState.authID != "auth1" {
		t.Fatalf("affinity auth id = %q, want %q", affinityState.authID, "auth1")
	}
	if len(affinityState.requestPayload) == 0 {
		t.Fatalf("affinity request payload should not be empty")
	}
	if len(affinityState.responseOutput) == 0 {
		t.Fatalf("affinity response output should not be empty")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"test-model","previous_response_id":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"我最喜欢的动物是什么"}]}]}`, firstID)))
	req2.Header.Set("Content-Type", "application/json")
	resp2 := httptest.NewRecorder()
	router.ServeHTTP(resp2, req2)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d, body=%s", resp2.Code, http.StatusOK, resp2.Body.String())
	}
	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("payload count = %d, want 2", len(payloads))
	}
	if gjson.GetBytes(payloads[1], "previous_response_id").Exists() {
		t.Fatalf("openai-compatibility retry should not forward previous_response_id: %s", payloads[1])
	}
	input := gjson.GetBytes(payloads[1], "input").Array()
	if len(input) != 3 {
		t.Fatalf("merged input len = %d, want 3", len(input))
	}
	if got := input[1].Get("content.0.text").String(); got != "记住了。" {
		t.Fatalf("merged assistant text = %q, want %q", got, "记住了。")
	}
	if got := resp2.Body.String(); gjson.Get(got, "output.0.content.0.text").String() != "狐狸" {
		t.Fatalf("second response text = %q, want %q", gjson.Get(got, "output.0.content.0.text").String(), "狐狸")
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth1" || got[1] != "auth1" {
		t.Fatalf("auth selection = %v, want [auth1 auth1]", got)
	}
}
