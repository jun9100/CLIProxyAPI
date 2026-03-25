// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported for compact responses",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if streamResult.Exists() {
		if updated, err := sjson.DeleteBytes(rawJSON, "stream"); err == nil {
			rawJSON = updated
		}
	}

	c.Header("Content-Type", "application/json")
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "responses/compact")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func responsesOutputAvailable(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("[]"))
}

func isResponsesUnsupportedPreviousResponseIDError(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil || errMsg.Error == nil {
		return false
	}
	message := strings.TrimSpace(errMsg.Error.Error())
	if message == "" {
		return false
	}
	detail := strings.TrimSpace(gjson.Get(message, "detail").String())
	if detail == "" {
		detail = strings.TrimSpace(gjson.Get(message, "error.message").String())
	}
	if detail == "" {
		detail = message
	}
	return strings.Contains(strings.ToLower(detail), "unsupported parameter: previous_response_id")
}

func normalizeResponsesRequestForHistory(rawJSON []byte) []byte {
	normalized, _, errMsg := normalizeResponseCreateRequest(rawJSON)
	if errMsg == nil && len(normalized) > 0 {
		return normalized
	}
	return bytes.Clone(rawJSON)
}

func shouldRetryResponsesContinuation(rawJSON []byte, errMsg *interfaces.ErrorMessage, previousRequest []byte, previousResponseOutput []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != "" &&
		isResponsesUnsupportedPreviousResponseIDError(errMsg) &&
		len(previousRequest) > 0 &&
		responsesOutputAvailable(previousResponseOutput)
}

func buildResponsesFallbackRequest(rawJSON []byte, previousRequest []byte, previousResponseOutput []byte, stream bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	retryPayload, retryHistory, errMsg := normalizeResponseSubsequentRequest(rawJSON, previousRequest, previousResponseOutput, false)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	if stream {
		return retryPayload, retryHistory, nil
	}
	if updated, errDelete := sjson.DeleteBytes(retryPayload, "stream"); errDelete == nil {
		retryPayload = updated
	}
	return retryPayload, retryHistory, nil
}

func shouldPremergeResponsesContinuationForOpenAICompatibility(rawJSON []byte, affinityState responsesAuthAffinityState, h *OpenAIResponsesAPIHandler) bool {
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) == "" {
		return false
	}
	if strings.TrimSpace(affinityState.authID) == "" {
		return false
	}
	if len(affinityState.requestPayload) == 0 || !responsesOutputAvailable(affinityState.responseOutput) {
		return false
	}
	if h == nil || h.AuthManager == nil {
		return false
	}
	auth, ok := h.AuthManager.GetByID(strings.TrimSpace(affinityState.authID))
	if !ok || auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return true
	}
	if auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	affinityState := responsesHTTPAuthAffinity.lookupRequestState(rawJSON)
	selectedAuthID := ""
	if pinnedAuthID := affinityState.authID; pinnedAuthID != "" {
		cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
		selectedAuthID = pinnedAuthID
	}
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = strings.TrimSpace(authID)
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	requestPayload := rawJSON
	requestForBind := normalizeResponsesRequestForHistory(requestPayload)
	if shouldPremergeResponsesContinuationForOpenAICompatibility(rawJSON, affinityState, h) {
		retryPayload, retryHistory, retryErr := buildResponsesFallbackRequest(rawJSON, affinityState.requestPayload, affinityState.responseOutput, false)
		if retryErr == nil {
			requestPayload = retryPayload
			requestForBind = retryHistory
		}
	}
	modelName := gjson.GetBytes(requestPayload, "model").String()

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, requestPayload, "")
	if shouldRetryResponsesContinuation(requestPayload, errMsg, affinityState.requestPayload, affinityState.responseOutput) {
		retryPayload, retryHistory, retryErr := buildResponsesFallbackRequest(requestPayload, affinityState.requestPayload, affinityState.responseOutput, false)
		if retryErr == nil {
			resp, upstreamHeaders, errMsg = h.ExecuteWithAuthManager(
				cliCtx,
				h.HandlerType(),
				gjson.GetBytes(retryPayload, "model").String(),
				retryPayload,
				"",
			)
			if errMsg == nil {
				requestForBind = retryHistory
			}
		}
	}
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	responsesHTTPAuthAffinity.bindPayload(selectedAuthID, requestForBind, resp)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// New core execution path
	affinityState := responsesHTTPAuthAffinity.lookupRequestState(rawJSON)
	selectedAuthID := ""
	requestPayload := rawJSON
	requestForBind := normalizeResponsesRequestForHistory(requestPayload)
	if shouldPremergeResponsesContinuationForOpenAICompatibility(rawJSON, affinityState, h) {
		retryPayload, retryHistory, retryErr := buildResponsesFallbackRequest(rawJSON, affinityState.requestPayload, affinityState.responseOutput, true)
		if retryErr == nil {
			requestPayload = retryPayload
			requestForBind = retryHistory
		}
	}
	var cliCancel handlers.APIHandlerCancelFunc
	startStream := func(requestPayload []byte) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		modelName := gjson.GetBytes(requestPayload, "model").String()
		cliCtx, nextCancel := h.GetContextWithCancel(h, c, context.Background())
		cliCancel = nextCancel
		if pinnedAuthID := affinityState.authID; pinnedAuthID != "" {
			cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
			selectedAuthID = pinnedAuthID
		}
		cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
			selectedAuthID = strings.TrimSpace(authID)
		})
		dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, requestPayload, "")
		dataChan = observeResponsesAuthAffinity(
			dataChan,
			func() string { return selectedAuthID },
			func() []byte { return requestForBind },
		)
		return dataChan, upstreamHeaders, errChan
	}
	dataChan, upstreamHeaders, errChan := startStream(requestPayload)

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

	// Peek at the first chunk
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			if shouldRetryResponsesContinuation(requestPayload, errMsg, affinityState.requestPayload, affinityState.responseOutput) {
				retryPayload, retryHistory, retryErr := buildResponsesFallbackRequest(requestPayload, affinityState.requestPayload, affinityState.responseOutput, true)
				if retryErr == nil {
					requestForBind = retryHistory
					if cliCancel != nil {
						cliCancel(errMsg.Error)
					}
					dataChan, upstreamHeaders, errChan = startStream(retryPayload)
					continue
				}
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream closed without data? Send headers and done.
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			// Write first chunk logic (matching forwardResponsesStream)
			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))
			flusher.Flush()

			// Continue
			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
			return
		}
	}
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(chunk))
		},
		WriteDone: func() {
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}
