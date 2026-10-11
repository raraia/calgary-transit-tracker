package store

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/raraia/calgary-transit-tracker/internal/ontime"
)

// These are integration tests: they run the real SQL against a real Postgres.
//
// They only run when TEST_DATABASE_URL is set, and they WIPE that database, so
// point it at a throwaway database, never the one the collector writes to:
//
//	TEST_DATABASE_URL=postgres://ctt:ctt@localhost:5432/ctt_test go test ./internal/store/
//
// CI sets this up automatically (see .github/workflows/ci.yml).
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	s, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// Start every test from an empty database.
	if _, err := s.pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil { // second run must be a no-op
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("schema_migrations has %d rows, want 2", n)
	}
}

// loadTinySchedule loads one route with one trip of three stops, 10 minutes apart from 08:00.
func loadTinySchedule(t *testing.T, s *Store) {
	t.Helper()
	counts, err := s.ReplaceSchedule(context.Background(), []TableLoad{
		{"routes", []string{"route_id", "short_name", "long_name"}, pgx.CopyFromRows([][]any{
			{"201", "201", "Brentwood/Somerset"},
		})},
		{"stops", []string{"stop_id", "stop_name", "lat", "lon"}, pgx.CopyFromRows([][]any{
			{"s1", "First St", 51.0, -114.0}, {"s2", "Second St", 51.1, -114.1}, {"s3", "Third St", nil, nil},
		})},
		{"trips", []string{"trip_id", "route_id", "service_id", "direction_id", "headsign"}, pgx.CopyFromRows([][]any{
			{"t1", "201", "WKDY", 0, "Somerset"},
		})},
		{"stop_times", []string{"trip_id", "stop_id", "stop_sequence", "arrival_time", "arrival_secs"}, pgx.CopyFromRows([][]any{
			{"t1", "s1", 1, "08:00:00", 8 * 3600},
			{"t1", "s2", 2, "08:10:00", 8*3600 + 600},
			{"t1", "s3", 3, "08:20:00", 8*3600 + 1200},
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts["stop_times"] != 3 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestReplaceScheduleIsAtomic(t *testing.T) {
	s := testStore(t)
	loadTinySchedule(t, s)

	// A load with a broken row (trip pointing at a route that doesn't exist)
	// must fail and leave the previous schedule fully intact.
	_, err := s.ReplaceSchedule(context.Background(), []TableLoad{
		{"routes", []string{"route_id"}, pgx.CopyFromRows([][]any{{"999"}})},
		{"trips", []string{"trip_id", "route_id"}, pgx.CopyFromRows([][]any{{"tX", "no-such-route"}})},
	})
	if err == nil {
		t.Fatal("expected a foreign key error")
	}
	var n int
	s.pool.QueryRow(context.Background(), "SELECT count(*) FROM stop_times").Scan(&n)
	if n != 3 {
		t.Errorf("old schedule lost after failed load: %d stop_times rows", n)
	}
}

func TestUpsertAndDelayFromPredictedTime(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	loadTinySchedule(t, s)

	// 2025-11-02 was a fall-back day in Calgary: midnight was MDT (UTC-6) but
	// 8 am was MST (UTC-7), so the 08:00 scheduled arrival is 15:00 UTC. A bus
	// predicted at 15:02 UTC is 2 minutes late; naive midnight-based math would
	// call it 58 minutes early. (It was also the last one: the tz database has
	// Alberta on year-round UTC-6 from 2026-11-01. Because all of this goes
	// through the tz database, the queries handle that change with no code edits.)
	date := time.Date(2025, 11, 2, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2025, 11, 2, h, m, 0, 0, time.UTC) }
	t1 := at(14, 50)
	t2 := t1.Add(30 * time.Second)

	// First snapshot: stop 1 predicted 3 min late; stop 2 reports a delay directly.
	_, err := s.UpsertStopVisits(ctx, []StopVisit{
		{ServiceDate: date, TripID: "t1", StopSequence: 1, StopID: "s1", Predicted: at(15, 3)},
		{ServiceDate: date, TripID: "t1", StopSequence: 2, StopID: "s2", RouteID: "201", HasDelay: true, Delay: 400},
	}, t1)
	if err != nil {
		t.Fatal(err)
	}
	// Second snapshot: stop 1's prediction improves to 2 min late. The same
	// visit is listed twice, which must not trip up the upsert.
	_, err = s.UpsertStopVisits(ctx, []StopVisit{
		{ServiceDate: date, TripID: "t1", StopSequence: 1, StopID: "s1", Predicted: at(15, 1)},
		{ServiceDate: date, TripID: "t1", StopSequence: 1, StopID: "s1", Predicted: at(15, 2)},
	}, t2)
	if err != nil {
		t.Fatal(err)
	}
	// An older snapshot arriving late must be ignored.
	_, err = s.UpsertStopVisits(ctx, []StopVisit{
		{ServiceDate: date, TripID: "t1", StopSequence: 1, StopID: "s1", Predicted: at(16, 0)},
	}, t1.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	var (
		delay, obs int
		route      string
	)
	err = s.pool.QueryRow(ctx,
		"SELECT delay_secs, observations, route_id FROM visit_delays WHERE trip_id='t1' AND stop_sequence=1").
		Scan(&delay, &obs, &route)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 120 {
		t.Errorf("delay_secs = %d, want 120 (DST-correct)", delay)
	}
	if obs != 2 {
		t.Errorf("observations = %d, want 2 (stale snapshot ignored)", obs)
	}
	if route != "201" {
		t.Errorf("route_id = %q, want 201 filled from the schedule", route)
	}

	err = s.pool.QueryRow(ctx, "SELECT delay_secs FROM visit_delays WHERE trip_id='t1' AND stop_sequence=2").Scan(&delay)
	if err != nil || delay != 400 {
		t.Errorf("feed-reported delay = %d, %v; want 400", delay, err)
	}
}

func TestStatsQueries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Eight visits to stop s2 on route 201 at 3 pm Calgary time (21:00 UTC):
	// six on time, one 10 min late, and one absurd 5-hour "delay" that must be
	// filtered out as bad data. Plus two visits to s9, too few to rank.
	date := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	seen := time.Date(2026, 10, 9, 21, 0, 0, 0, time.UTC)
	delays := []int32{0, 30, -45, 120, 200, 299, 600, 5 * 3600}
	var visits []StopVisit
	for i, d := range delays {
		visits = append(visits, StopVisit{
			ServiceDate: date, TripID: "trip" + string(rune('a'+i)), StopSequence: 2,
			RouteID: "201", StopID: "s2", HasDelay: true, Delay: d,
			Predicted: seen,
		})
	}
	visits = append(visits,
		StopVisit{ServiceDate: date, TripID: "x1", StopSequence: 9, RouteID: "201", StopID: "s9", HasDelay: true, Delay: 900, Predicted: seen},
		StopVisit{ServiceDate: date, TripID: "x2", StopSequence: 9, RouteID: "201", StopID: "s9", HasDelay: true, Delay: 900, Predicted: seen},
	)
	if _, err := s.UpsertStopVisits(ctx, visits, seen); err != nil {
		t.Fatal(err)
	}

	f := Filter{Since: seen.Add(-time.Hour), SettledBefore: seen.Add(time.Hour), Window: ontime.Default}

	sums, err := s.RouteSummaries(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 || sums[0].RouteID != "201" || sums[0].Visits != 9 {
		t.Fatalf("summaries = %+v (want 9 visits: the 5-hour outlier excluded)", sums)
	}
	// On time: 0, 30, -45, 120, 200, 299 = 6 of 9.
	if math.Abs(sums[0].OnTimeRatio-6.0/9.0) > 1e-9 {
		t.Errorf("on-time ratio = %v, want %v", sums[0].OnTimeRatio, 6.0/9.0)
	}
	if sums[0].ShortName != "201" {
		t.Errorf("short name falls back to route_id when routes table is empty, got %q", sums[0].ShortName)
	}

	hours, err := s.RouteHourly(ctx, "201", f)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 1 || hours[0].Hour != 15 {
		t.Errorf("hourly = %+v, want one bucket at hour 15 (local time)", hours)
	}

	stops, err := s.WorstStops(ctx, "201", f, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stops) != 1 || stops[0].StopID != "s2" || stops[0].Visits != 7 {
		t.Errorf("worst stops = %+v, want only s2 with 7 visits (s9 has too few)", stops)
	}

	// A visit still being updated (last seen after SettledBefore) is excluded.
	f.SettledBefore = seen
	if sums, _ := s.RouteSummaries(ctx, f); len(sums) != 0 {
		t.Errorf("unsettled visits should be excluded, got %+v", sums)
	}
}
