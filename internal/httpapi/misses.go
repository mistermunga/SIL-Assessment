package httpapi

import "net/http"

// probe records what ServeMux would answer for a request, without sending it.
type probe struct {
	header http.Header
	status int
}

func (p *probe) Header() http.Header         { return p.header }
func (p *probe) Write(b []byte) (int, error) { return len(b), nil }
func (p *probe) WriteHeader(code int)        { p.status = code }

// routeMisses replaces ServeMux's plain-text 404 and 405 with the JSON error format.
// The 404/405 distinction and the Allow header come from the mux itself; everything
// else (matched routes, redirects) is left to the mux unchanged.
// An unknown path is a 404 even without a token, and a wrong method is a 405
// even on a route that needs a token: both are decided before any guard runs.
func (s *Server) routeMisses(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			p := &probe{header: http.Header{}}
			h.ServeHTTP(p, r)
			switch p.status {
			case http.StatusNotFound:
				s.writeError(w, r, &HTTPError{
					Status: http.StatusNotFound, Code: "route_not_found", Message: "no such endpoint"})
				return
			case http.StatusMethodNotAllowed:
				w.Header().Set("Allow", p.header.Get("Allow"))
				s.writeError(w, r, &HTTPError{
					Status: http.StatusMethodNotAllowed, Code: "method_not_allowed",
					Message: "method not allowed for this endpoint"})
				return
			}
		}
		// ServeHTTP, not the handler returned above: only ServeHTTP records
		// the matched pattern that r.PathValue reads.
		mux.ServeHTTP(w, r)
	})
}
