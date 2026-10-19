package auth

import "clinic/internal/store"

type Service struct {
	store  *store.Store
	tokens *TokenManager
}

func NewService(s *store.Store, t *TokenManager) *Service {
	return &Service{store: s, tokens: t}
}
