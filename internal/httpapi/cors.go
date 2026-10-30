package httpapi

import (
	"net/http"
	"strings"
)

// cors answers browsers for the allowlisted origins (Config.TrustedOrigins)
// only. Other origins get no CORS headers, so browsers keep the response
// from their scripts; a preflight from them is refused. Credentials are
// allowed, so the allowlist must never contain "*".
//
// The session cookie is SameSite=Lax: a frontend on another origin of the
// same site works, a frontend on another site does not.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		h := w.Header()
		h.Add("Vary", "Origin")
		allowed := origin != "" && s.origins[origin]
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		if preflight {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			if !allowed || !corsHeadersAllowed(r.Header.Get("Access-Control-Request-Headers")) {
				writeError(w, r, errCrossOrigin)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if allowed {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
		}
		next.ServeHTTP(w, r)
	})
}

// corsHeadersAllowed accepts only the Content-Type request header.
func corsHeadersAllowed(list string) bool {
	for _, h := range strings.Split(list, ",") {
		if h = strings.TrimSpace(h); h != "" && !strings.EqualFold(h, "content-type") {
			return false
		}
	}
	return true
}
