package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/adamraziv/dataporch/internal/catalog"
	"github.com/adamraziv/dataporch/internal/execution"
)

var (
	errListerRequired = errors.New("http api: resource lister is required")
	errLoggerRequired = errors.New("http api: logger is required")
	errLimitInvalid   = errors.New("http api: default limit must be positive")
)

type ResourceLister interface {
	ListResources(context.Context, int) ([]catalog.Resource, error)
}

type Handler struct {
	mux *http.ServeMux
}

func New(
	lister ResourceLister,
	defaultLimit int,
	logger *slog.Logger,
) (*Handler, error) {
	if lister == nil {
		return nil, errListerRequired
	}

	if defaultLimit <= 0 {
		return nil, errLimitInvalid
	}

	if logger == nil {
		return nil, errLoggerRequired
	}

	handler := &Handler{mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /healthz", handler.health)
	handler.mux.HandleFunc("GET /v1/resources", func(w http.ResponseWriter, r *http.Request) {
		handler.listResources(w, r, lister, defaultLimit, logger)
	})

	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}, nil)
}

func (h *Handler) listResources(
	w http.ResponseWriter,
	r *http.Request,
	lister ResourceLister,
	defaultLimit int,
	logger *slog.Logger,
) {
	limit := defaultLimit

	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeJSON(
				w,
				http.StatusBadRequest,
				map[string]string{"error": "limit must be an integer"},
				logger,
			)

			return
		}

		limit = parsed
	}

	resources, err := lister.ListResources(r.Context(), limit)
	if err != nil {
		if errors.Is(err, execution.ErrInvalidLimit) {
			writeJSON(
				w,
				http.StatusBadRequest,
				map[string]string{"error": "limit is outside the allowed range"},
				logger,
			)

			return
		}

		logger.ErrorContext(
			r.Context(),
			"listing resources",
			slog.Any("error", err),
		)
		writeJSON(
			w,
			http.StatusInternalServerError,
			map[string]string{"error": "internal server error"},
			logger,
		)

		return
	}

	writeJSON(
		w,
		http.StatusOK,
		struct {
			Resources []catalog.Resource `json:"resources"`
		}{Resources: resources},
		logger,
	)
}

func writeJSON(
	w http.ResponseWriter,
	status int,
	value any,
	logger *slog.Logger,
) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil && logger != nil {
		logger.Error("writing http response", slog.Any("error", err))
	}
}
