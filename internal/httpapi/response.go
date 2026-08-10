package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/alikilicw/filecosystem-be/internal/queue"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to write response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeServiceError maps domain sentinels onto status codes. Unexpected errors
// are logged in full but reported generically so internals never leak.
func writeServiceError(w http.ResponseWriter, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The requested resource does not exist.")
	case errors.Is(err, domain.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", cleanMessage(err))
	case errors.Is(err, domain.ErrUnsupported):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported", cleanMessage(err))
	case errors.Is(err, queue.ErrNotConnected):
		writeError(w, http.StatusServiceUnavailable, "queue_unavailable", "The processing queue is temporarily unavailable.")
	default:
		log.Error("unhandled request error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our side.")
	}
}

// cleanMessage drops the sentinel prefix so clients see only the human part.
func cleanMessage(err error) string {
	msg := err.Error()
	for _, prefix := range []string{
		domain.ErrInvalidInput.Error() + ": ",
		domain.ErrUnsupported.Error() + ": ",
	} {
		if idx := strings.Index(msg, prefix); idx >= 0 {
			return msg[idx+len(prefix):]
		}
	}
	return msg
}
