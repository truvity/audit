package health_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/truvity/audit/internal/health"
)

func get(h http.Handler) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec.Code, rec.Body.String()
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestReadyWhenEveryCheckHolds(t *testing.T) {
	ok := func(context.Context) error { return nil }
	if code, _ := get(health.Ready(quiet)); code != http.StatusOK {
		t.Errorf("no checks = %d, want 200", code)
	}
	if code, _ := get(health.Ready(quiet, health.Check{Name: "database", Fn: ok}, health.Check{Name: "catalogues", Fn: ok})); code != http.StatusOK {
		t.Errorf("passing checks = %d, want 200", code)
	}
}

// What failed is named, and why is not: the endpoint is open to whatever can
// reach the pod, and a dependency's error can carry its address.
func TestNotReadyNamesTheCheckAndNeverTheError(t *testing.T) {
	ok := func(context.Context) error { return nil }
	bad := func(context.Context) error { return errors.New("dial tcp 10.1.2.3:5432: connection refused") }
	code, body := get(health.Ready(quiet,
		health.Check{Name: "database", Fn: bad}, health.Check{Name: "catalogues", Fn: ok}, health.Check{Name: "archive", Fn: bad}))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
	if !strings.Contains(body, "archive, database") || strings.Contains(body, "catalogues") || strings.Contains(body, "10.1.2.3") {
		t.Errorf("body = %q", body)
	}
}

// A dependency that does not answer is not ready, and the probe is not held for
// longer than the bound.
func TestADependencyThatHangsIsNotReady(t *testing.T) {
	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	code, _ := get(health.Ready(quiet, health.Check{Name: "database", Fn: hang}))
	if code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", code)
	}
	if took := time.Since(start); took > health.Timeout+time.Second {
		t.Errorf("the probe took %s", took)
	}
}
