package wsbase

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// IsAuthorizedRequest checks if the request contains a valid auth token.
// If expectedToken is empty, all requests are authorized.
func IsAuthorizedRequest(expectedToken string, r *http.Request) bool {
	token := strings.TrimSpace(expectedToken)
	if token == "" {
		return true
	}

	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if bearerToken, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		if TokensEqual(token, strings.TrimSpace(bearerToken)) {
			return true
		}
	}

	queryToken := strings.TrimSpace(r.URL.Query().Get("token"))
	return TokensEqual(token, queryToken)
}

// TokensEqual performs constant-time comparison of two tokens.
func TokensEqual(expected, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
