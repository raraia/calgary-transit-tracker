package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/raraia/calgary-transit-tracker/internal/store"
)

// fakeQuerier stands in for the database. It returns canned data and records
// the arguments it was called with so tests can check them.
type fakeQuerier struct {
	err     error
	pingErr error

	gotRoute  string
	gotFilter store.Filter
	gotLimit  int

	routes []store.RouteSummary
	hours  []store.HourStat
	stops  []store.StopStat
}

func (f *fakeQuerier) Ping(context.Context) error { return f.pingErr }
func (f *fakeQuerier) RouteSummaries(_ context.Context, flt store.Filter) ([]store.RouteSummary, error) {
	f.gotFilter = flt
	return f.routes, f.err
}
func (f *fakeQuerier) RouteHourly(_ context.Context, id string, flt store.Filter) ([]store.HourStat, error) {
	f.gotRoute, f.gotFilter = id, flt
	return f.hours, f.err
}
func (f *fakeQuerier) WorstStops(_ context.Context, id string, flt store.Filter, limit int) ([]store.StopStat, error) {
	f.gotRoute, f.gotFilter, f.gotLimit = id, flt, limit
	return f.stops, f.err
}

var fixedNow = time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)

func newTestServer(q Querier) *Server {
	s := New(q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return fixedNow }
	return s
}

// do sends one request through the full handler (middleware included).
func do(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	s.Handler(nil).ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	q := &fakeQuerier{routes: []store.RouteSummary{{RouteID: "201", ShortName: "201", Visits: 40, OnTimeRatio: 0.8}}}
	rec := do(t, newTestServer(q), "/api/routes?days=3")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Days   int                  `json:"days"`
		Routes []store.RouteSummary `json:"routes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Days != 3 || len(body.Routes) != 1 || body.Routes[0].RouteID != "201" {
		t.Errorf("body = %+v", body)
	}
	if want := fixedNow.AddDate(0, 0, -3); !q.gotFilter.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", q.gotFilter.Since, want)
	}
	if want := fixedNow.Add(-settleTime); !q.gotFilter.SettledBefore.Equal(want) {
		t.Errorf("SettledBefore = %v, want %v", q.gotFilter.SettledBefore, want)
	}
}

func TestEmptyResultsAreArraysNotNull(t *testing.T) {
	rec := do(t, newTestServer(&fakeQuerier{}), "/api/routes")
	if !strings.Contains(rec.Body.String(), `"routes":[]`) {
		t.Errorf("want an empty array, got %s", rec.Body)
	}
}

func TestBadInputIs400(t *testing.T) {
	s := newTestServer(&fakeQuerier{})
	for _, path := range []string{
		"/api/routes?days=0",
		"/api/routes?days=91",
		"/api/routes?days=abc",
		"/api/routes/has%20space/hourly",
		"/api/routes/" + strings.Repeat("x", 65) + "/hourly",
		"/api/routes/201/stops?limit=500",
	} {
		if rec := do(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, rec.Code)
		}
	}
}

func TestHourlyJSONAndCSV(t *testing.T) {
	q := &fakeQuerier{hours: []store.HourStat{{Hour: 8, Visits: 12, OnTimeRatio: 0.75, AvgDelaySecs: 95.5}}}
	s := newTestServer(q)

	rec := do(t, s, "/api/routes/201/hourly")
	if rec.Code != http.StatusOK || q.gotRoute != "201" || !strings.Contains(rec.Body.String(), `"hour":8`) {
		t.Errorf("JSON: %d %s (route %q)", rec.Code, rec.Body, q.gotRoute)
	}

	rec = do(t, s, "/api/routes/201/hourly?format=csv")
	want := "hour,visits,on_time_ratio,avg_delay_secs\n8,12,0.7500,95.5\n"
	if rec.Body.String() != want {
		t.Errorf("CSV = %q, want %q", rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestStopsLimit(t *testing.T) {
	q := &fakeQuerier{}
	do(t, newTestServer(q), "/api/routes/201/stops?limit=5")
	if q.gotLimit != 5 {
		t.Errorf("limit = %d, want 5", q.gotLimit)
	}
}

func TestDatabaseErrorIsHidden(t *testing.T) {
	q := &fakeQuerier{err: errors.New(`relation "stop_visits" does not exist`)}
	rec := do(t, newTestServer(q), "/api/routes")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "stop_visits") {
		t.Errorf("internal error details leaked to client: %s", rec.Body)
	}
}

func TestHealth(t *testing.T) {
	if rec := do(t, newTestServer(&fakeQuerier{}), "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("healthy: %d", rec.Code)
	}
	if rec := do(t, newTestServer(&fakeQuerier{pingErr: errors.New("down")}), "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("db down: %d, want 503", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := do(t, newTestServer(&fakeQuerier{}), "/healthz")
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s header", h)
		}
	}
}

func TestStaticFilesServed(t *testing.T) {
	s := newTestServer(&fakeQuerier{})
	files := fstest.MapFS{"index.html": {Data: []byte("<h1>dashboard</h1>")}}
	rec := httptest.NewRecorder()
	s.Handler(files).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "dashboard") {
		t.Errorf("static: %d %s", rec.Code, rec.Body)
	}
}

func TestRateLimiter(t *testing.T) {
	now := fixedNow
	rl := newRateLimiter(1, 3, func() time.Time { return now }) // 1/sec, burst 3

	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("request %d within burst was refused", i+1)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Error("4th request in the same instant should be refused")
	}
	if !rl.allow("5.6.7.8") {
		t.Error("a different client has its own bucket")
	}
	now = now.Add(time.Second) // one token drips back in
	if !rl.allow("1.2.3.4") {
		t.Error("should be allowed again after refill")
	}
}

func TestRateLimitMiddlewareReturns429(t *testing.T) {
	s := newTestServer(&fakeQuerier{})
	s.limiter = newRateLimiter(0.001, 1, func() time.Time { return fixedNow })
	h := s.Handler(nil)

	codes := make([]int, 2)
	for i := range codes {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		codes[i] = rec.Code
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusTooManyRequests {
		t.Errorf("codes = %v, want [200 429]", codes)
	}
}
