package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const tokenIssuer = "clinic"

var ErrInvalidToken = errors.New("invalid token")

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret []byte, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: secret, ttl: ttl}
}

func (m *TokenManager) Issue(userID int32) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(m.ttl)

	claims := jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		Subject:   strconv.Itoa(int(userID)),
		Audience:  nil,
		ExpiresAt: jwt.NewNumericDate(exp),
		NotBefore: nil,
		IssuedAt:  jwt.NewNumericDate(now),
		ID:        "",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

func (m *TokenManager) Verify(raw string) (int32, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(tokenIssuer),
	)
	if err != nil {
		return 0, ErrInvalidToken
	}

	id, err := strconv.ParseInt(claims.Subject, 10, 32)
	if err != nil || id <= 0 {
		return 0, ErrInvalidToken
	}
	return int32(id), nil
}
