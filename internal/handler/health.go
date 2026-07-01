package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nanohype/portal/internal/handler/respond"
)

type HealthHandler struct {
	db          *pgxpool.Pool
	environment string
}

func NewHealthHandler(db *pgxpool.Pool, environment string) *HealthHandler {
	return &HealthHandler{db: db, environment: environment}
}

// Live is the liveness probe: process-only, no dependency checks. A Postgres
// outage is a readiness problem — restarting healthy pods won't fix it.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready is the readiness probe: the server takes traffic only when Postgres is
// reachable.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		respond.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Check is the app-level health surface the UI reads: per-service status plus
// the dev_login flag in development.
func (h *HealthHandler) Check(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	status := "ok"
	services := map[string]string{}

	if err := h.db.Ping(ctx); err != nil {
		status = "degraded"
		services["postgres"] = "unhealthy"
	} else {
		services["postgres"] = "ok"
	}

	httpStatus := http.StatusOK
	if status != "ok" {
		httpStatus = http.StatusServiceUnavailable
	}
	resp := map[string]any{"status": status, "services": services}
	if h.environment == "development" {
		resp["dev_login"] = true
	}
	respond.JSON(w, httpStatus, resp)
}
