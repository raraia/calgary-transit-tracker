package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/raraia/calgary-transit-tracker/internal/ontime"
)

// Filter narrows which stop visits a stats query looks at.
type Filter struct {
	// Since: only visits last seen at or after this time.
	Since time.Time
	// SettledBefore: ignore visits updated after this time. A visit that's
	// still being updated is a bus that hasn't reached the stop yet, so its
	// delay is still a prediction rather than a result.
	SettledBefore time.Time
	// Window: what counts as on time.
	Window ontime.Window
}

// Delays outside this range are almost always bad data (a trip that never ran,
// a broken prediction) rather than a real bus. Leaving them in would let one
// glitch drag a whole route's average.
const (
	minPlausibleDelaySecs = -30 * 60
	maxPlausibleDelaySecs = 60 * 60
)

// minStopVisits is the fewest visits a stop needs before it can appear in the
// "worst stops" list. Otherwise a stop seen twice, once badly, tops the chart.
const minStopVisits = 5

// RouteSummary is one route's reliability over the filter's time range.
type RouteSummary struct {
	RouteID      string  `json:"route_id"`
	ShortName    string  `json:"short_name"`
	LongName     string  `json:"long_name"`
	Visits       int     `json:"visits"`
	OnTimeRatio  float64 `json:"on_time_ratio"` // 0..1
	AvgDelaySecs float64 `json:"avg_delay_secs"`
}

// HourStat is a route's reliability for one hour of the day (0-23, local time).
type HourStat struct {
	Hour         int     `json:"hour"`
	Visits       int     `json:"visits"`
	OnTimeRatio  float64 `json:"on_time_ratio"`
	AvgDelaySecs float64 `json:"avg_delay_secs"`
}

// StopStat is a route's reliability at one stop.
type StopStat struct {
	StopID       string  `json:"stop_id"`
	StopName     string  `json:"stop_name"`
	Visits       int     `json:"visits"`
	OnTimeRatio  float64 `json:"on_time_ratio"`
	AvgDelaySecs float64 `json:"avg_delay_secs"`
}

// The three queries below share the same building blocks:
//   - COUNT(*) FILTER (WHERE ...) counts only the on-time visits, so dividing
//     by COUNT(*) gives the on-time share.
//   - Parameters ($1, $2, ...) are always passed separately from the SQL text.
//     The database never treats a value as SQL, which is what prevents SQL
//     injection even if a route ID came straight from a URL.

const routeSummariesSQL = `
SELECT d.route_id,
       COALESCE(r.short_name, d.route_id) AS short_name,
       COALESCE(r.long_name, '')          AS long_name,
       COUNT(*)                           AS visits,
       COUNT(*) FILTER (WHERE d.delay_secs BETWEEN $3 AND $4)::float8 / COUNT(*) AS on_time_ratio,
       AVG(d.delay_secs)::float8          AS avg_delay_secs
FROM visit_delays d
LEFT JOIN routes r ON r.route_id = d.route_id
WHERE d.last_seen_at >= $1
  AND d.last_seen_at <  $2
  AND d.route_id IS NOT NULL
  AND d.delay_secs BETWEEN $5 AND $6
GROUP BY d.route_id, r.short_name, r.long_name
ORDER BY on_time_ratio ASC, visits DESC`

// RouteSummaries returns every route with data, least reliable first.
func (s *Store) RouteSummaries(ctx context.Context, f Filter) ([]RouteSummary, error) {
	rows, err := s.pool.Query(ctx, routeSummariesSQL,
		f.Since, f.SettledBefore,
		-f.Window.EarlySecs(), f.Window.LateSecs(),
		minPlausibleDelaySecs, maxPlausibleDelaySecs)
	if err != nil {
		return nil, err
	}
	// CollectRows reads every row into a slice; RowToStructByPos fills the
	// struct fields in the same order as the SELECT columns.
	return pgx.CollectRows(rows, pgx.RowToStructByPos[RouteSummary])
}

const routeHourlySQL = `
SELECT EXTRACT(HOUR FROM (COALESCE(d.scheduled_arrival, d.predicted_arrival, d.last_seen_at) AT TIME ZONE $8))::int AS hour,
       COUNT(*) AS visits,
       COUNT(*) FILTER (WHERE d.delay_secs BETWEEN $4 AND $5)::float8 / COUNT(*) AS on_time_ratio,
       AVG(d.delay_secs)::float8 AS avg_delay_secs
FROM visit_delays d
WHERE d.route_id = $1
  AND d.last_seen_at >= $2
  AND d.last_seen_at <  $3
  AND d.delay_secs BETWEEN $6 AND $7
GROUP BY hour
ORDER BY hour`

// RouteHourly returns one row per hour of day that has data for the route.
// Hours with no buses (e.g. 3 am) are simply absent.
func (s *Store) RouteHourly(ctx context.Context, routeID string, f Filter) ([]HourStat, error) {
	rows, err := s.pool.Query(ctx, routeHourlySQL,
		routeID, f.Since, f.SettledBefore,
		-f.Window.EarlySecs(), f.Window.LateSecs(),
		minPlausibleDelaySecs, maxPlausibleDelaySecs,
		transitTZ)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[HourStat])
}

const worstStopsSQL = `
SELECT d.stop_id,
       COALESCE(s.stop_name, d.stop_id) AS stop_name,
       COUNT(*) AS visits,
       COUNT(*) FILTER (WHERE d.delay_secs BETWEEN $4 AND $5)::float8 / COUNT(*) AS on_time_ratio,
       AVG(d.delay_secs)::float8 AS avg_delay_secs
FROM visit_delays d
LEFT JOIN stops s ON s.stop_id = d.stop_id
WHERE d.route_id = $1
  AND d.last_seen_at >= $2
  AND d.last_seen_at <  $3
  AND d.delay_secs BETWEEN $6 AND $7
GROUP BY d.stop_id, s.stop_name
HAVING COUNT(*) >= $8
ORDER BY avg_delay_secs DESC
LIMIT $9`

// WorstStops returns the route's stops with the highest average delay.
func (s *Store) WorstStops(ctx context.Context, routeID string, f Filter, limit int) ([]StopStat, error) {
	rows, err := s.pool.Query(ctx, worstStopsSQL,
		routeID, f.Since, f.SettledBefore,
		-f.Window.EarlySecs(), f.Window.LateSecs(),
		minPlausibleDelaySecs, maxPlausibleDelaySecs,
		minStopVisits, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[StopStat])
}
