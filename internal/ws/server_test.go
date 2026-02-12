package ws

import (
	"net/http/httptest"
	"testing"
)

func TestIsAuthorizedRequestWithoutToken(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	if !isAuthorizedRequest("", req) {
		t.Fatal("expected request without configured token to be authorized")
	}
}

func TestIsAuthorizedRequestBearerToken(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Authorization", "Bearer secret-token")

	if !isAuthorizedRequest("secret-token", req) {
		t.Fatal("expected bearer token to authorize request")
	}
}

func TestIsAuthorizedRequestQueryToken(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws?token=secret-token", nil)

	if !isAuthorizedRequest("secret-token", req) {
		t.Fatal("expected query token to authorize request")
	}
}

func TestIsAuthorizedRequestRejectsInvalidToken(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws?token=wrong", nil)
	req.Header.Set("Authorization", "Bearer also-wrong")

	if isAuthorizedRequest("secret-token", req) {
		t.Fatal("expected invalid tokens to be rejected")
	}
}
