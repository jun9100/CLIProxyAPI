package executor

import "net/http"

func classifyCodexWebsocketBootstrapFailure(resp *http.Response, payload []byte, err error) (error, bool) {
	if resp == nil || resp.StatusCode <= 0 {
		return err, false
	}

	classified := newCodexWebsocketStatusErr(resp.StatusCode, payload, resp.Header)
	return classified, shouldInvalidateCodexSessionBeforeFirstByte(classified)
}

func shouldInvalidateCodexSessionBeforeFirstByte(err error) bool {
	if err == nil {
		return false
	}

	status := 0
	if statusErr, ok := err.(interface{ StatusCode() int }); ok && statusErr != nil {
		status = statusErr.StatusCode()
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return status >= http.StatusInternalServerError
	}
}

func newCodexWebsocketStatusErr(statusCode int, payload []byte, headers http.Header) error {
	wrapped := statusErrWithHeaders{
		statusErr: newCodexStatusErr(statusCode, payload),
	}
	if headers != nil {
		wrapped.headers = headers.Clone()
	}
	return wrapped
}
