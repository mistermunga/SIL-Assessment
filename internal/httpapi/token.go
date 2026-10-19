package httpapi

import "time"

// tokenResponse is how an issued token leaves the API.
type tokenResponse struct {
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresAt time.Time `json:"expires_at"`
}

func newTokenResponse(token string, expiresAt time.Time) tokenResponse {
	return tokenResponse{Token: token, TokenType: "Bearer", ExpiresAt: expiresAt}
}
