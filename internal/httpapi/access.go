package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"clinic/internal/apperr"
	"clinic/internal/auth"
)

// Authenticator turns a raw token into the current principal. *auth.Service satisfies it.
type Authenticator interface {
	Authenticate(ctx context.Context, rawToken string) (auth.Principal, error)
}

// Access decides whether an authenticated caller may use a route. It is any predicate
// over the principal, so rules can combine roles or use any other principal field.
type Access func(p auth.Principal) bool

var (
	Authenticated Access = func(auth.Principal) bool { return true } // any valid token
	Patient       Access = auth.Principal.IsPatient
	Doctor        Access = auth.Principal.IsDoctor // admins count: an admin is a doctor
	Admin         Access = auth.Principal.IsAdmin
)

// AnyOf allows the caller if any rule does. With no rules it denies everyone.
func AnyOf(rules ...Access) Access {
	return func(p auth.Principal) bool {
		for _, r := range rules {
			if r(p) {
				return true
			}
		}
		return false
	}
}

// AllOf allows the caller only if every rule does. With no rules it denies everyone.
func AllOf(rules ...Access) Access {
	return func(p auth.Principal) bool {
		if len(rules) == 0 {
			return false
		}
		for _, r := range rules {
			if !r(p) {
				return false
			}
		}
		return true
	}
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the authenticated caller. On an open route it is the zero
// Principal, which has no role and matches no rows.
func principalFrom(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(principalKey{}).(auth.Principal)
	return p
}

var errUnauthorized = errors.New("authentication required")

// guard authenticates the request, then evaluates the access rule.
// A missing, malformed, expired or unknown token all produce the same 401.
func (s *Server) guard(access Access, next HandlerFunc) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		raw, ok := bearerToken(r)
		if !ok {
			return errUnauthorized
		}
		p, err := s.authn.Authenticate(r.Context(), raw)
		if errors.Is(err, auth.ErrInvalidToken) {
			return errUnauthorized
		}
		if err != nil {
			return err // infrastructure failure: 500, not 401
		}
		if info := infoFrom(r.Context()); info != nil {
			info.userID = p.UserID
		}
		if !access(p) {
			return apperr.ErrForbidden
		}
		if !access(p) {
			return apperr.ErrForbidden
		}
		return next(w, r.WithContext(withPrincipal(r.Context(), p)))
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
