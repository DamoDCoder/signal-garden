package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() (http.Handler, *bool) {
	reached := false
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}), &reached
}

// TestCORSPreflightIsAnsweredWithoutReachingTheHandler covers the case that
// breaks a browser client: a PATCH to update controls sends a preflight first,
// and an unanswered preflight fails the real request before it is made.
func TestCORSPreflightIsAnsweredWithoutReachingTheHandler(t *testing.T) {
	next, reached := okHandler()
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodOptions, "/v1/runs/demo/controls", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	req.Header.Set("Access-Control-Request-Headers", "content-type")

	withCORS(next, "*").ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if *reached {
		t.Error("preflight reached the gateway; it should be answered by the middleware")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q, want the caller's origin echoed", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPatch) {
		t.Errorf("allow-methods = %q, want PATCH: UpdateControls is the route that needs it", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "content-type") {
		t.Errorf("allow-headers = %q, want content-type", got)
	}
	if rec.Header().Get("Access-Control-Max-Age") == "" {
		t.Error("no max-age; every control change would preflight again")
	}
}

func TestCORSPassesRealRequestsThrough(t *testing.T) {
	next, reached := okHandler()
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/demo/snapshot", nil)
	req.Header.Set("Origin", "http://localhost:5173")

	withCORS(next, "*").ServeHTTP(rec, req)

	if !*reached {
		t.Fatal("the request never reached the gateway")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q, want the caller's origin", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("vary = %q, want Origin: the response body is the same but the header is not", got)
	}
}

// TestCORSNeverAllowsCredentials is the safety property behind reflecting an
// arbitrary origin. Without credentials a browser attaches no cookies, so a
// hostile page learns nothing it could not learn by connecting to the port
// itself. Allowing them would turn a permissive origin into a real hole.
func TestCORSNeverAllowsCredentials(t *testing.T) {
	next, _ := okHandler()
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/demo/snapshot", nil)
	req.Header.Set("Origin", "https://evil.example")

	withCORS(next, "*").ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("allow-credentials = %q, want it unset", got)
	}
}

func TestCORSHonoursAnExplicitOrigin(t *testing.T) {
	next, _ := okHandler()
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/demo/snapshot", nil)
	req.Header.Set("Origin", "https://evil.example")

	withCORS(next, "http://localhost:5173").ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q, want the configured origin rather than the caller's", got)
	}
}

// TestCORSDisabledAddsNothing keeps the off switch real: a deployment that sets
// an empty origin gets the handler it would have had before this middleware
// existed.
func TestCORSDisabledAddsNothing(t *testing.T) {
	next, reached := okHandler()
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodOptions, "/v1/runs/demo/controls", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)

	withCORS(next, "").ServeHTTP(rec, req)

	if !*reached {
		t.Error("the request was intercepted with CORS disabled")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q with CORS disabled, want it unset", got)
	}
}
