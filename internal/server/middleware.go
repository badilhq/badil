package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireBearer returns middleware that enforces a Bearer token on the
// Authorization header, matching the scheme case-insensitively: the token
// must equal one of the non-empty tokens. The token comparison is
// constant-time so a leaked timing signal cannot reveal the secret
// prefix-by-prefix.
//
// With no non-empty token configured, the route is disabled (all requests get
// 401) — this is intentional so a misconfigured deployment fails closed
// instead of open.
func requireBearer(tokens ...string) func(http.Handler) http.Handler {
	var want [][]byte
	for _, t := range tokens {
		if t != "" {
			want = append(want, []byte(t))
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The auth scheme is a case-insensitive token (RFC 9110 §11.1).
			scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || !matchesAny([]byte(got), want) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// matchesAny reports whether got equals one of want, comparing it with each
// in constant time.
func matchesAny(got []byte, want [][]byte) bool {
	match := 0
	for _, w := range want {
		match |= subtle.ConstantTimeCompare(got, w)
	}
	return match == 1
}
