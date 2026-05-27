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
