package store

import (
	"context"
	"time"
)

// StopVisit is the latest thing we know about one bus visiting one stop.
type StopVisit struct {
	ServiceDate  time.Time // the service day (only year/month/day matter)
	TripID       string
	StopSequence int32
	RouteID      string // "" if the feed didn't say
	StopID       string

	HasDelay  bool      // true if Delay was reported by the feed
	Delay     int32     // seconds late (+) or early (-)
	Predicted time.Time // predicted arrival; zero if not reported
}

// visitKey identifies a stop visit; it's the stop_visits primary key.
type visitKey struct {
	date string
	trip string
	seq  int32
}

// upsertVisitsSQL inserts new visits and updates existing ones in a single
// statement ("upsert" = update or insert).
//
// unnest() turns the parallel arrays we send ($1..$7) back into rows, so
// thousands of visits go to Postgres in one round trip instead of one query each.
//
// ON CONFLICT handles visits we've already seen: refresh the prediction, bump
// last_seen_at and count the observation. The WHERE clause ignores snapshots
// that are older than what's stored, so a late or repeated feed snapshot can
// never overwrite fresher data.
const upsertVisitsSQL = `
INSERT INTO stop_visits (
    service_date, trip_id, stop_sequence, route_id, stop_id,
    feed_delay_secs, predicted_arrival, first_seen_at, last_seen_at
)
SELECT d, t, seq, r, s, fd, pa, $8, $8
FROM unnest($1::date[], $2::text[], $3::int[], $4::text[], $5::text[], $6::int[], $7::timestamptz[])
     AS u(d, t, seq, r, s, fd, pa)
ON CONFLICT (service_date, trip_id, stop_sequence) DO UPDATE SET
    route_id          = COALESCE(EXCLUDED.route_id, stop_visits.route_id),
    stop_id           = EXCLUDED.stop_id,
    feed_delay_secs   = EXCLUDED.feed_delay_secs,
    predicted_arrival = EXCLUDED.predicted_arrival,
    last_seen_at      = EXCLUDED.last_seen_at,
    observations      = stop_visits.observations + 1
WHERE stop_visits.last_seen_at < EXCLUDED.last_seen_at`

// UpsertStopVisits records one feed snapshot. seenAt is the snapshot's time.
// It returns how many distinct visits were sent to the database.
func (s *Store) UpsertStopVisits(ctx context.Context, visits []StopVisit, seenAt time.Time) (int, error) {
	visits = dedupeVisits(visits)
	if len(visits) == 0 {
		return 0, nil
	}

	// Build one slice per column. Pointer slices let us send SQL NULL (a nil
	// pointer) for values the feed didn't provide.
	n := len(visits)
	dates := make([]time.Time, n)
	trips := make([]string, n)
	seqs := make([]int32, n)
	routes := make([]*string, n)
	stops := make([]string, n)
	delays := make([]*int32, n)
	predicted := make([]*time.Time, n)

	for i, v := range visits {
		dates[i] = v.ServiceDate
		trips[i] = v.TripID
		seqs[i] = v.StopSequence
		stops[i] = v.StopID
		if v.RouteID != "" {
			routes[i] = &v.RouteID
		}
		if v.HasDelay {
			delays[i] = &v.Delay
		}
		if !v.Predicted.IsZero() {
			predicted[i] = &v.Predicted
		}
	}

	_, err := s.pool.Exec(ctx, upsertVisitsSQL, dates, trips, seqs, routes, stops, delays, predicted, seenAt)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// dedupeVisits keeps only the last entry for each visit key.
//
// Postgres refuses an upsert that touches the same row twice in one statement
// ("ON CONFLICT DO UPDATE command cannot affect row a second time"), and a feed
// can occasionally list the same stop twice.
func dedupeVisits(visits []StopVisit) []StopVisit {
	index := make(map[visitKey]int, len(visits))
	out := visits[:0:0] // new slice; don't modify the caller's
	for _, v := range visits {
		k := visitKey{v.ServiceDate.Format("2006-01-02"), v.TripID, v.StopSequence}
		if i, seen := index[k]; seen {
			out[i] = v // later entry wins
			continue
		}
		index[k] = len(out)
		out = append(out, v)
	}
	return out
}
