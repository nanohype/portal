package respond

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/nanohype/portal/internal/apperr"
)

type ErrorResponse struct {
	Error     string `json:"error"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type ListResponse[T any] struct {
	Data    []T   `json:"data"`
	Total   int64 `json:"total"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
}

func JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Error("failed to encode JSON response", "error", err)
		}
	}
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, ErrorResponse{Error: http.StatusText(status), Message: msg})
}

func ErrorWithRequest(w http.ResponseWriter, r *http.Request, status int, msg string) {
	reqID := chimw.GetReqID(r.Context())
	JSON(w, status, ErrorResponse{Error: http.StatusText(status), Message: msg, RequestID: reqID})
}

func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// FromError maps a service-layer error to a status code in one place so the same
// failure stops being a 404 in one handler and a 500 in another. A bare
// pgx.ErrNoRows (an org-scoped query that found nothing) is a 404; an
// apperr.Error maps by its Kind with its message; anything else is a 500 with a
// generic message (the real error is for logs, not the client).
func FromError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		Error(w, http.StatusNotFound, "not found")
		return
	}

	var ae *apperr.Error
	if errors.As(err, &ae) {
		status := http.StatusInternalServerError
		switch ae.Kind {
		case apperr.KindNotFound:
			status = http.StatusNotFound
		case apperr.KindForbidden:
			status = http.StatusForbidden
		case apperr.KindConflict:
			status = http.StatusConflict
		case apperr.KindValidation:
			status = http.StatusBadRequest
		}
		if status == http.StatusInternalServerError {
			ErrorWithRequest(w, r, status, "internal error")
			return
		}
		Error(w, status, ae.Msg)
		return
	}

	slog.Error("unhandled handler error", "error", err, "request_id", chimw.GetReqID(r.Context()))
	ErrorWithRequest(w, r, http.StatusInternalServerError, "internal error")
}
