package mcpauth

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

type Verifier interface {
	Verify(string) error
}

func New(verifier Verifier, next http.Handler) (http.Handler, error) {
	if isNilInterface(verifier) {
		return nil, errors.New("mcp token verifier is required")
	}
	if next == nil {
		return nil, errors.New("mcp downstream handler is required")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential, challenge := credentialFromRequest(r)
		if challenge != "" {
			writeUnauthorized(w, challenge)
			return
		}

		if err := verifier.Verify(credential); err != nil {
			switch {
			case errors.Is(err, mcptoken.ErrInvalidToken), errors.Is(err, mcptoken.ErrNoToken):
				writeUnauthorized(w, `Bearer error="invalid_token"`)
			case errors.Is(err, mcptoken.ErrUnavailable):
				writeUnavailable(w)
			default:
				writeUnavailable(w)
			}
			return
		}

		next.ServeHTTP(w, r)
	}), nil
}

func credentialFromRequest(r *http.Request) (string, string) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", "Bearer"
	}
	if len(values) != 1 {
		return "", `Bearer error="invalid_request"`
	}

	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", `Bearer error="invalid_request"`
	}

	return parts[1], ""
}

func writeUnauthorized(w http.ResponseWriter, challenge string) {
	w.Header().Set("WWW-Authenticate", challenge)
	w.WriteHeader(http.StatusUnauthorized)
}

func writeUnavailable(w http.ResponseWriter) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
