package localmcp

import (
	"net/http"

	"github.com/adamraziv/dataporch/internal/mcpcontrol"
	"github.com/adamraziv/dataporch/internal/transports/mcpauth"
)

func authenticatedHandler(credential string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, challenge := mcpauth.BearerCredential(r)
		if challenge != "" {
			writeUnauthorized(w, challenge)
			return
		}
		if err := mcpcontrol.Verify(credential, presented); err != nil {
			writeUnauthorized(w, `Bearer error="invalid_token"`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeUnauthorized(w http.ResponseWriter, challenge string) {
	w.Header().Set("WWW-Authenticate", challenge)
	w.WriteHeader(http.StatusUnauthorized)
}
