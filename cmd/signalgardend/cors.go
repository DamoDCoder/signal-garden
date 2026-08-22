package main

import (
	"net/http"
	"strings"
)

// corsMaxAge is how long a browser may cache a preflight result. Ten minutes
// keeps a development session from re-preflighting every control change while
// staying short enough that a policy change takes effect within one coffee.
const corsMaxAge = "600"

// allowedMethods and allowedHeaders cover the generated REST surface: the
// routes use GET, POST, and PATCH, and the only header a JSON client sets is
// content-type.
var (
	allowedMethods = strings.Join([]string{
		http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodOptions,
	}, ", ")
	allowedHeaders = "content-type"
)

// withCORS answers cross-origin requests for the browser client.
//
// It exists because the React client runs on its own development server and is
// therefore a different origin from this daemon. Without it every REST call
// fails in the browser while the WebSocket stream keeps working — WebSockets do
// not preflight — which reads as "the garden streams but no button works" and
// is a miserable thing to debug.
//
// The policy is deliberately permissive and deliberately local. This daemon
// serves one person's machine, holds no credentials, and has no authentication
// to protect; the public gateway that would authenticate and rate-limit it is a
// deployment concern, per docs/contracts.md. Credentials are *not* allowed,
// which is what keeps reflecting an arbitrary origin from being a real hole: a
// browser will not attach cookies to these requests, so a hostile page learns
// nothing it could not learn by connecting to the port itself.
func withCORS(next http.Handler, origin string) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allow := origin
		if origin == "*" {
			// Echo the caller's origin rather than literal "*", so the
			// same configuration works if this ever gains credentials
			// or a Vary-aware cache sits in front of it.
			if got := r.Header.Get("Origin"); got != "" {
				allow = got
			}
		}
		if allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
			w.Header().Add("Vary", "Origin")
		}

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
