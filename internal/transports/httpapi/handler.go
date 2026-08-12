package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

var errLoggerRequired = errors.New("http api: logger is required")

type Handler struct {
	mux    *http.ServeMux
	logger *slog.Logger
}

func New(logger *slog.Logger) (*Handler, error) {
	if logger == nil {
		return nil, errLoggerRequired
	}

	handler := &Handler{
		mux:    http.NewServeMux(),
		logger: logger,
	}
	handler.mux.HandleFunc("GET /healthz", handler.health)

	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(
		w,
		http.StatusOK,
		map[string]string{"status": "ok"},
		h.logger,
	)
}

func writeJSON(w http.ResponseWriter, status int, value any, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil && logger != nil {
		logger.Error("writing http response", slog.Any("error", err))
	}
}
