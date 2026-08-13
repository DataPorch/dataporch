package mcp

import (
	"net/http"
	"net/url"
	"strings"
)

func withOriginValidation(
	protection *http.CrossOriginProtection,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) == 0 {
			next.ServeHTTP(w, r)

			return
		}

		hasSingleOrigin := len(origins) == 1 && isSerializedHTTPOrigin(origins[0])
		if !hasSingleOrigin {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)

			return
		}

		checkRequest := r.Clone(r.Context())
		checkRequest.Method = http.MethodPost
		checkRequest.Header = checkRequest.Header.Clone()
		checkRequest.Header.Del("Sec-Fetch-Site")

		if err := protection.Check(checkRequest); err != nil {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func isSerializedHTTPOrigin(value string) bool {
	origin, err := url.Parse(value)
	if err != nil {
		return false
	}

	hasHTTPOrHTTPScheme := strings.EqualFold(origin.Scheme, "http") ||
		strings.EqualFold(origin.Scheme, "https")
	hasNoQueryOrFragment := !strings.ContainsAny(value, "?#") &&
		!origin.ForceQuery &&
		origin.RawQuery == "" &&
		origin.Fragment == ""
	hasValidHostPort := origin.Host != "" &&
		!strings.HasSuffix(origin.Host, ":")
	hasOnlyOriginComponents := hasValidHostPort &&
		origin.User == nil &&
		origin.Path == "" &&
		origin.Opaque == ""

	return hasHTTPOrHTTPScheme && hasNoQueryOrFragment && hasOnlyOriginComponents
}
