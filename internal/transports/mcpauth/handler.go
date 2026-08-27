package mcpauth

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

const invalidRequestChallenge = `Bearer error="invalid_request"`

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
		credential, challenge := BearerCredential(r)
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

// BearerCredential returns the credential and an optional WWW-Authenticate challenge.
func BearerCredential(r *http.Request) (string, string) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", "Bearer"
	}

	if len(values) != 1 {
		return "", invalidRequestChallenge
	}

	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", invalidRequestChallenge
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
	kind := rv.Kind()
	canBeNil := kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice

	return canBeNil && rv.IsNil()
}
