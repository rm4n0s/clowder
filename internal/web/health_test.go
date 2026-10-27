package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// healthFunc is a HealthChecker that runs a function.
type healthFunc func(ctx context.Context) error

func (f healthFunc) Check(ctx context.Context) error { return f(ctx) }

func TestHealthz(t *testing.T) {
	t.Parallel()
	failing := errors.New("StorePingFailed", "the database is gone", "database", "clowder")
	tests := []struct {
		name       string
		health     func(e *testEnv) HealthChecker // nil: keep the one of the env
		timeout    time.Duration
		method     string
		wantStatus int
		wantBody   string
		wantWarn   bool
	}{
		{name: "database up", wantStatus: http.StatusOK, wantBody: "ok\n"},
		{name: "head request", method: http.MethodHead, wantStatus: http.StatusOK},
		{
			name: "database closed", wantStatus: http.StatusServiceUnavailable, wantBody: "unavailable\n", wantWarn: true,
			health: func(e *testEnv) HealthChecker {
				if err := e.st.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				return app.NewHealth(e.st)
			},
		},
		{
			name: "check fails", wantStatus: http.StatusServiceUnavailable, wantBody: "unavailable\n", wantWarn: true,
			health: func(*testEnv) HealthChecker {
				return healthFunc(func(context.Context) error { return failing })
			},
		},
		{
			name: "check is slower than the deadline", wantStatus: http.StatusServiceUnavailable, wantBody: "unavailable\n", wantWarn: true,
			timeout: 20 * time.Millisecond,
			health: func(*testEnv) HealthChecker {
				return healthFunc(func(ctx context.Context) error {
					<-ctx.Done()
					return failing
				})
			},
		},
		{
			name: "no health service", wantStatus: http.StatusServiceUnavailable, wantBody: "unavailable\n", wantWarn: true,
			health: func(*testEnv) HealthChecker { return nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, nil)
			if tt.health != nil {
				e.srv.svc.Health = tt.health(e)
			}
			if tt.timeout != 0 {
				e.srv.healthTimeout = tt.timeout
			}
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req := get("/healthz")
			req.Method = method
			rec := e.send(req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			h := rec.Header()
			if got := h.Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := h.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			for _, want := range wantCSP {
				if !strings.Contains(h.Get("Content-Security-Policy"), want) {
					t.Errorf("Content-Security-Policy = %q, missing %q", h.Get("Content-Security-Policy"), want)
				}
			}
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			if got := h.Get("Set-Cookie"); got != "" {
				t.Errorf("Set-Cookie = %q", got)
			}

			// A success is a debug entry; a failure is an info entry and a
			// warn entry with the error.
			var requestLevel string
			var warns int
			for _, le := range logEntries(t, e.log) {
				switch {
				case le.Msg == "request" && le.Pattern == healthPattern:
					requestLevel = le.Level
				case le.Level == "WARN":
					warns++
					if le.Err == nil && !strings.Contains(le.Msg, "without a health service") {
						t.Errorf("warn entry %q has no err", le.Msg)
					}
				}
			}
			wantLevel := "DEBUG"
			if tt.wantStatus != http.StatusOK {
				wantLevel = "INFO"
			}
			if requestLevel != wantLevel {
				t.Errorf("request logged at %q, want %q", requestLevel, wantLevel)
			}
			if (warns > 0) != tt.wantWarn {
				t.Errorf("%d warn entries, want warn = %v", warns, tt.wantWarn)
			}
		})
	}
}

// TestHealthzNeedsNoSession: the route is public, with or without a cookie,
// and a bad cookie is neither checked nor cleared.
func TestHealthzNeedsNoSession(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	rec := e.send(get("/healthz", withCookie("not-a-session")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("Set-Cookie = %q", got)
	}
}

// TestHealthzOtherMethods: only GET (and HEAD) is routed.
func TestHealthzOtherMethods(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	req := get("/healthz")
	req.Method = http.MethodPut
	if rec := e.send(req); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestHealthzSecureHeaders: with an https base URL the answer carries HSTS.
func TestHealthzSecureHeaders(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *config.Config) { c.BaseURL = "https://clowder.example" })
	rec := e.send(get("/healthz"))
	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("no Strict-Transport-Security")
	}
}
