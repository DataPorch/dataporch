package localadmin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/adamraziv/dataporch/internal/connection"
)

const maxRequestBody = 64 << 10

var (
	errImporterRequired = errors.New("local admin: importer is required")
	errLoggerRequired   = errors.New("local admin: logger is required")
)

type Importer interface {
	Import(context.Context, connection.ImportRequest) (connection.ImportResult, error)
}

type Handler struct{ mux *http.ServeMux }

func NewHandler(importer Importer, logger *slog.Logger) (http.Handler, error) {
	if importer == nil {
		return nil, errImporterRequired
	}
	if logger == nil {
		return nil, errLoggerRequired
	}
	handler := &Handler{mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /v1/connections/import", func(w http.ResponseWriter, r *http.Request) { handler.importConnection(w, r, importer, logger) })
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) importConnection(w http.ResponseWriter, r *http.Request, importer Importer, logger *slog.Logger) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request struct {
		DatabaseID       connection.ID   `json:"databaseId"`
		Kind             connection.Kind `json:"kind"`
		ConnectionString []byte          `json:"connectionString"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	defer clear(request.ConnectionString)

	result, err := importer.Import(r.Context(), connection.ImportRequest{ID: request.DatabaseID, Kind: request.Kind, ConnectionString: request.ConnectionString})
	if err != nil {
		logger.ErrorContext(r.Context(), "importing connection", "database_id", request.DatabaseID, "kind", request.Kind, "category", errorCategory(err))
		if errors.Is(err, connection.ErrInvalidConnectionString) {
			writeError(w, http.StatusBadRequest, "invalid_connection_string", "connection string is invalid")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "requested database is unavailable")
		return
	}
	status := "added"
	code := http.StatusCreated
	if result.Updated {
		status = "updated"
		code = http.StatusOK
	}
	writeJSON(w, code, struct {
		Status           string        `json:"status"`
		DatabaseID       connection.ID `json:"databaseId"`
		ConnectionTested bool          `json:"connectionTested"`
	}{Status: status, DatabaseID: result.ID, ConnectionTested: false})
}

func errorCategory(err error) string {
	if errors.Is(err, connection.ErrInvalidConnectionString) {
		return "invalid_connection_string"
	}
	return "database_unavailable"
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
