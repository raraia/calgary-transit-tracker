// Package api serves on-time statistics as JSON (and CSV) over HTTP.
//
// Endpoints:
//
//	GET /healthz                          is the service up and can it reach the database?
//	GET /api/routes?days=7                every route, least reliable first
//	GET /api/routes/{id}/hourly?days=7    one route's on-time share by hour of day (?format=csv)
//	GET /api/routes/{id}/stops?limit=10   one route's worst stops
//	GET /                                 the dashboard (static files from web/)
//
// The handlers depend on the Querier interface rather than *store.Store, so the
// tests can hand them a fake and run without a database.
package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/raraia/calgary-transit-tracker/internal/ontime"
	"github.com/raraia/calgary-transit-tracker/internal/store"
)

// Querier is the slice of the store that the API needs.
type Querier interface {
	Ping(ctx context.Context) error
	RouteSummaries(ctx context.Context, f store.Filter) ([]store.RouteSummary, error)
	RouteHourly(ctx context.Context, routeID string, f store.Filter) ([]store.HourStat, error)
	WorstStops(ctx context.Context, routeID string, f store.Filter, limit int) ([]store.StopStat, error)
}

// settleTime is how long a stop visit must go without updates before we treat
// it as finished. Until then the bus hasn't arrived and the delay is a guess.
const settleTime = 2 * time.Minute

// routeIDPattern is what a valid route ID looks like. Anything else is
// rejected before it reaches the database. (Queries are parameterised, so this
// isn't what stops SQL injection; it just turns junk input into a clear 400.)
var routeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Server struct {
	q       Querier
	window  ontime.Window
	log     *slog.Logger
	now     func() time.Time
	limiter *rateLimiter
}

func New(q Querier, log *slog.Logger) *Server {
	return &Server{
		q:      q,
		window: ontime.Default,
		log:    log,
		now:    time.Now,
		// Each client IP gets a burst of 30 requests, refilled at 10 per second.
		// Plenty for the dashboard; stops one client from hammering the database.
		limiter: newRateLimiter(10, 30, time.Now),
	}
}

// Handler builds the complete HTTP handler: routes wrapped in middleware.
// static holds the dashboard files served at "/"; pass nil to serve only the API.
func (s *Server) Handler(static fs.FS) http.Handler {
	mux := http.NewServeMux()
	// Go 1.22+ patterns: the method and {wildcards} are part of the route.
	// r.PathValue("routeID") reads the wildcard back out.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/routes", s.handleRoutes)
	mux.HandleFunc("GET /api/routes/{routeID}/hourly", s.handleHourly)
	mux.HandleFunc("GET /api/routes/{routeID}/stops", s.handleStops)
	if static != nil {
		mux.Handle("GET /", http.FileServerFS(static))
	}

	// Middleware wraps the mux like layers of an onion. A request passes
	// through recover -> log -> headers -> rate limit -> mux, and the response
	// comes back out the same way.
	var h http.Handler = mux
	h = s.rateLimit(h)
	h = securityHeaders(h)
	h = s.logRequests(h)
	h = s.recoverPanics(h)
	return h
}

// --- handlers ----------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.q.Ping(ctx); err != nil {
		s.log.Warn("health check: database unreachable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	days, err := intParam(r, "days", 7, 1, 90)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	routes, err := s.q.RouteSummaries(r.Context(), s.filter(days))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if routes == nil {
		routes = []store.RouteSummary{} // encode as [] not null, so clients can always loop over it
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days":   days,
		"window": s.windowJSON(),
		"routes": routes,
	})
}

func (s *Server) handleHourly(w http.ResponseWriter, r *http.Request) {
	routeID, ok := s.routeID(w, r)
	if !ok {
		return
	}
	days, err := intParam(r, "days", 7, 1, 90)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hours, err := s.q.RouteHourly(r.Context(), routeID, s.filter(days))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if hours == nil {
		hours = []store.HourStat{}
	}

	if r.URL.Query().Get("format") == "csv" {
		writeHourlyCSV(w, routeID, hours)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"route_id": routeID,
		"days":     days,
		"window":   s.windowJSON(),
		"hours":    hours,
	})
}

func (s *Server) handleStops(w http.ResponseWriter, r *http.Request) {
	routeID, ok := s.routeID(w, r)
	if !ok {
		return
	}
	days, err := intParam(r, "days", 7, 1, 90)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := intParam(r, "limit", 10, 1, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stops, err := s.q.WorstStops(r.Context(), routeID, s.filter(days), limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if stops == nil {
		stops = []store.StopStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"route_id": routeID,
		"days":     days,
		"stops":    stops,
	})
}

// --- helpers -----------------------------------------------------------------

// filter turns "the last N days" into a store.Filter.
func (s *Server) filter(days int) store.Filter {
	now := s.now()
	return store.Filter{
		Since:         now.AddDate(0, 0, -days),
		SettledBefore: now.Add(-settleTime),
		Window:        s.window,
	}
}

func (s *Server) windowJSON() map[string]int {
	return map[string]int{"max_early_secs": s.window.EarlySecs(), "max_late_secs": s.window.LateSecs()}
}

// routeID reads and validates the {routeID} part of the URL. It writes the 400
// response itself and returns ok=false if the ID is invalid.
func (s *Server) routeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("routeID")
	if !routeIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return "", false
	}
	return id, true
}

// intParam reads an optional integer query parameter and checks its range.
func intParam(r *http.Request, name string, def, lo, hi int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be a whole number from %d to %d", name, lo, hi)
	}
	return n, nil
}

// serverError logs the real error but sends the client a generic message.
// Database errors can reveal table names and query details an attacker would
// find useful, so they stay in our logs.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusOK {
		// Stats only change as buses report in, so let browsers reuse a
		// response for a minute instead of re-querying on every click.
		w.Header().Set("Cache-Control", "public, max-age=60")
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeHourlyCSV(w http.ResponseWriter, routeID string, hours []store.HourStat) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	// routeID already matched routeIDPattern, so it's safe inside this header.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="route-%s-hourly.csv"`, routeID))
	cw := csv.NewWriter(w)
	cw.Write([]string{"hour", "visits", "on_time_ratio", "avg_delay_secs"})
	for _, h := range hours {
		cw.Write([]string{
			strconv.Itoa(h.Hour),
			strconv.Itoa(h.Visits),
			strconv.FormatFloat(h.OnTimeRatio, 'f', 4, 64),
			strconv.FormatFloat(h.AvgDelaySecs, 'f', 1, 64),
		})
	}
	cw.Flush()
}
