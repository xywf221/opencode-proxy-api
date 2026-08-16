package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

// writeJSON writes a JSON payload with the given status after setting the
// Content-Type. Errors writing the body are logged, not returned.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.With("component", "proxy").Error("write json error", "error", err)
	}
}

// writeJSONError writes a standardized error envelope: {"error":{message,type}}.
func writeJSONError(w http.ResponseWriter, status int, message, errType string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"message": message,
			"type":    errType,
		},
	})
}

// allowedHeaders lists the upstream response headers forwarded verbatim to the
// client. Everything else is stripped.
var allowedHeaders = map[string]bool{
	"Content-Type":      true,
	"Cache-Control":     true,
	"Connection":        true,
	"Transfer-Encoding": true,
	"X-Request-Id":      true,
}

// forwardHeaders copies the allowed downstream headers from resp to w.
func forwardHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vv := range resp.Header {
		if !allowedHeaders[k] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
}

// parseRequestMeta extracts the model and stream flags from the request body,
// which the handler uses to route and classify a request. Malformed bodies
// yield an empty model and no stream.
func parseRequestMeta(body []byte) (model string, stream bool) {
	var raw struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &raw) == nil {
		return raw.Model, raw.Stream
	}
	return "", false
}

// maxErrorBodyLog caps how much of a failed upstream body reaches the log.
const maxErrorBodyLog = 512

// truncateBody trims b to a rune-safe prefix suitable for logging.
func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= maxErrorBodyLog {
		return s
	}
	cut := s[:maxErrorBodyLog]
	// Avoid emitting a partial multi-byte rune at the cut point.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "...(truncated)"
}

// logRejectedRequest dumps the request body that upstream rejected with a 4xx
// status, so schema/validation failures are diagnosable from the log alone.
// It also reports whether the body is valid JSON.
func logRejectedRequest(log *slog.Logger, status int, body []byte) {
	if status < 400 || status >= 500 {
		return // only validation-type rejections
	}
	var validJSON bool
	if json.Valid(body) {
		validJSON = true
	}
	log.Warn("request rejected by upstream",
		"status", status,
		"valid_json", validJSON,
		"request_body", truncateBody(body),
	)
}

const maxUpstreamBodySize = 50 << 20 // 50 MB

// readUpstreamBody reads the upstream response body, limiting to maxUpstreamBodySize.
func readUpstreamBody(body io.ReadCloser) ([]byte, error) {
	limited := io.LimitReader(body, maxUpstreamBodySize+1)
	out, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxUpstreamBodySize {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", maxUpstreamBodySize)
	}
	return out, nil
}