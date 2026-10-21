package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
)

// HandlerFunc is a handler that reports failure by returning an error. The server turns
// the error into a response (errors.go), so handlers never format error replies.
// Return an error only before anything has been written to w.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Route binds a "METHOD /path" pattern to a handler.
// Access nil means the route is open: no token is read or checked.
// A non-nil Access requires a valid token, then evaluates the rule on the caller.
// Quiet routes (health checks) log successful requests at debug level.
type Route struct {
	Pattern string
	Access  Access
	Quiet   bool
	Handler HandlerFunc
}

// Module is one feature's slice of the API.
type Module interface {
	Routes() []Route
}

// ErrorSource is optionally implemented by a module to map its domain errors to responses.
type ErrorSource interface {
	ErrorRules() []ErrorRule
}

type Server struct {
	authn  Authenticator
	log    *slog.Logger
	rules  []ErrorRule
	routes []Route
}

func NewServer(authn Authenticator, log *slog.Logger, modules ...Module) *Server {
	s := &Server{authn: authn, log: log, rules: coreRules()}
	for _, m := range modules {
		s.routes = append(s.routes, m.Routes()...)
		if src, ok := m.(ErrorSource); ok {
			s.rules = append(s.rules, src.ErrorRules()...)
		}
	}
	return s
}

// Handler builds the router. It panics on a malformed route, and ServeMux panics on
// duplicate or conflicting patterns, so wiring mistakes fail at startup, not per request.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes {
		if rt.Pattern == "" || rt.Handler == nil {
			panic(fmt.Sprintf("httpapi: invalid route %q", rt.Pattern))
		}
		h := rt.Handler
		if rt.Access != nil {
			h = s.guard(rt.Access, h)
		}
		mux.Handle(rt.Pattern, s.adapt(rt, h))
	}
	// Outermost first, so the access log sees every response, including
	// recovered panics and unmatched routes.
	return s.logRequests(s.recoverPanic(s.routeMisses(mux)))
}

// OpenRoutes lists the routes that need no token, for auditing.
func (s *Server) OpenRoutes() []string {
	var open []string
	for _, rt := range s.routes {
		if rt.Access == nil {
			open = append(open, rt.Pattern)
		}
	}
	return open
}

func (s *Server) adapt(rt Route, h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := infoFrom(r.Context()); info != nil {
			info.route = rt.Pattern
			info.quiet = rt.Quiet
		}
		if err := h(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}
