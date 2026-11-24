package youtube

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"
)

// maxMessageLen limits the length of an API message in logs.
const maxMessageLen = 200

// APIError is an API response with a status other than 200.
type APIError struct {
	Endpoint string
	Status   int
	Reason   string // e.g. quotaExceeded, keyInvalid, playlistNotFound
	Message  string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("YouTube API %s: HTTP %d", e.Endpoint, e.Status)
	if e.Reason != "" {
		s += " " + e.Reason
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// quotaExhausted says whether the error means the key's daily quota is exhausted.
func (e *APIError) quotaExhausted() bool {
	return e.Status == http.StatusForbidden && (e.Reason == "quotaExceeded" || e.Reason == "dailyLimitExceeded")
}

func parseAPIError(endpoint string, status int, body []byte) *APIError {
	e := &APIError{Endpoint: endpoint, Status: status}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if len(payload.Error.Errors) > 0 {
			e.Reason = payload.Error.Errors[0].Reason
		}
		e.Message = truncate(payload.Error.Message, maxMessageLen)
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
