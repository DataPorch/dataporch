package localadmin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
	"github.com/adamraziv/dataporch/internal/mcptoken"
)

const maxRequestBody = 64 << 10

var (
	errImporterRequired     = errors.New("local admin: importer is required")
	errInvalidRequest       = errors.New("local admin: invalid request")
	errLoggerRequired       = errors.New("local admin: logger is required")
	errTokenManagerRequired = errors.New("local admin: mcp token manager is required")
)

type Importer interface {
	Import(context.Context, connection.ImportRequest) (connection.ImportResult, error)
}

type MCPTokenManager interface {
	Create(context.Context) (string, mcptoken.Metadata, error)
	Status() mcptoken.Status
	Rotate(context.Context) (string, mcptoken.Metadata, error)
	Revoke(context.Context) error
}

type Handler struct {
	mux          *http.ServeMux
	importer     Importer
	tokenManager MCPTokenManager
	logger       *slog.Logger
}

type importRequest struct {
	DatabaseID       connection.ID   `json:"databaseId"`
	Kind             connection.Kind `json:"kind"`
	ConnectionString []byte          `json:"connectionString"`
}

func NewHandler(importer Importer, tokenManager MCPTokenManager, logger *slog.Logger) (http.Handler, error) {
	if importer == nil {
		return nil, errImporterRequired
	}
	if tokenManager == nil {
		return nil, errTokenManagerRequired
	}

	if logger == nil {
		return nil, errLoggerRequired
	}

	handler := &Handler{
		mux:          http.NewServeMux(),
		importer:     importer,
		tokenManager: tokenManager,
		logger:       logger,
	}
	handler.mux.HandleFunc("POST /v1/connections/import", handler.importConnection)
	handler.mux.HandleFunc("GET /v1/mcp-token", handler.getMCPToken)
	handler.mux.HandleFunc("POST /v1/mcp-token", handler.createMCPToken)
	handler.mux.HandleFunc("POST /v1/mcp-token/rotate", handler.rotateMCPToken)
	handler.mux.HandleFunc("DELETE /v1/mcp-token", handler.revokeMCPToken)

	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) importConnection(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	request, err := decodeImportRequest(r.Body)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	defer zeroBytes(request.ConnectionString)

	result, err := h.importer.Import(r.Context(), connection.ImportRequest{
		ID:               request.DatabaseID,
		Kind:             request.Kind,
		ConnectionString: request.ConnectionString,
	})
	if err != nil {
		h.writeImportError(
			w,
			r,
			request,
			err,
		)

		return
	}

	writeImportResult(w, result)
}

type mcpTokenMetadataResponse struct {
	CreatedAt *time.Time `json:"created_at,omitempty"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
}

type mcpTokenStatusResponse struct {
	State    mcptoken.State           `json:"state"`
	Metadata mcpTokenMetadataResponse `json:"metadata"`
}

type mcpTokenMutationResponse struct {
	Token    string                   `json:"token"`
	Metadata mcpTokenMetadataResponse `json:"metadata"`
}

func (h *Handler) getMCPToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, mcpTokenStatusResponseFromStatus(h.tokenManager.Status()))
}

func (h *Handler) createMCPToken(w http.ResponseWriter, r *http.Request) {
	token, metadata, err := h.tokenManager.Create(r.Context())
	if err != nil {
		writeMCPTokenError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, mcpTokenMutationResponse{
		Token:    token,
		Metadata: mcpTokenMetadataResponseFromMetadata(metadata),
	})
}

func (h *Handler) rotateMCPToken(w http.ResponseWriter, r *http.Request) {
	token, metadata, err := h.tokenManager.Rotate(r.Context())
	if err != nil {
		writeMCPTokenError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, mcpTokenMutationResponse{
		Token:    token,
		Metadata: mcpTokenMetadataResponseFromMetadata(metadata),
	})
}

func (h *Handler) revokeMCPToken(w http.ResponseWriter, r *http.Request) {
	if err := h.tokenManager.Revoke(r.Context()); err != nil {
		writeMCPTokenError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func mcpTokenStatusResponseFromStatus(status mcptoken.Status) mcpTokenStatusResponse {
	return mcpTokenStatusResponse{
		State:    status.State,
		Metadata: mcpTokenMetadataResponseFromMetadata(status.Metadata),
	}
}

func mcpTokenMetadataResponseFromMetadata(metadata mcptoken.Metadata) mcpTokenMetadataResponse {
	response := mcpTokenMetadataResponse{}
	if !metadata.CreatedAt.IsZero() {
		createdAt := metadata.CreatedAt.UTC()
		response.CreatedAt = &createdAt
	}
	if metadata.RotatedAt != nil {
		rotatedAt := metadata.RotatedAt.UTC()
		response.RotatedAt = &rotatedAt
	}

	return response
}

func writeMCPTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mcptoken.ErrTokenExists):
		writeError(w, http.StatusConflict, "token_exists", "an MCP token is already configured")
	case errors.Is(err, mcptoken.ErrNoToken):
		writeError(w, http.StatusConflict, "token_not_configured", "an MCP token is not configured")
	default:
		writeError(w, http.StatusServiceUnavailable, "token_unavailable", "MCP token management is unavailable")
	}
}

func (h *Handler) writeImportError(
	w http.ResponseWriter,
	r *http.Request,
	request importRequest,
	err error,
) {
	h.logger.ErrorContext(
		r.Context(),
		"importing connection",
		"database_id",
		request.DatabaseID,
		"kind",
		request.Kind,
		"category",
		errorCategory(err),
	)

	if errors.Is(err, connection.ErrInvalidConnectionString) {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_connection_string",
			"connection string is invalid",
		)

		return
	}

	writeError(
		w,
		http.StatusServiceUnavailable,
		"database_unavailable",
		"requested database is unavailable",
	)
}

func decodeImportRequest(reader io.Reader) (importRequest, error) {
	var request importRequest

	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		return importRequest{}, err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return importRequest{}, errInvalidRequest
	}

	return request, nil
}

func writeRequestError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(
			w,
			http.StatusRequestEntityTooLarge,
			"request_too_large",
			"request is too large",
		)

		return
	}

	writeError(
		w,
		http.StatusBadRequest,
		"invalid_request",
		"request is invalid",
	)
}

func writeImportResult(w http.ResponseWriter, result connection.ImportResult) {
	status := "added"
	code := http.StatusCreated

	if result.IsUpdated {
		status = "updated"
		code = http.StatusOK
	}

	writeJSON(w, code, struct {
		Status             string        `json:"status"`
		DatabaseID         connection.ID `json:"databaseId"`
		IsConnectionTested bool          `json:"connectionTested"`
	}{Status: status, DatabaseID: result.ID, IsConnectionTested: false})
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

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
