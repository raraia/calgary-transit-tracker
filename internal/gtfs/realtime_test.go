package gtfs

import (
	"testing"
	"time"

	pb "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// buildFeed encodes a FeedMessage the same way Calgary Transit's server would,
// so the decoder can be tested without touching the network.
func buildFeed(t *testing.T, header uint64, entities ...*pb.FeedEntity) []byte {
	t.Helper()
	msg := &pb.FeedMessage{
		Header: &pb.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Timestamp:           proto.Uint64(header),
		},
		Entity: entities,
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func tripUpdate(id, tripID, routeID string, rel pb.TripDescriptor_ScheduleRelationship, stops ...*pb.TripUpdate_StopTimeUpdate) *pb.FeedEntity {
	return &pb.FeedEntity{
		Id: proto.String(id),
		TripUpdate: &pb.TripUpdate{
			Trip: &pb.TripDescriptor{
				TripId:               proto.String(tripID),
				RouteId:              proto.String(routeID),
				StartDate:            proto.String("20261009"),
				ScheduleRelationship: rel.Enum(),
			},
			StopTimeUpdate: stops,
		},
	}
}

func TestDecodeTripUpdates(t *testing.T) {
	predicted := int64(1_791_600_000)
	data := buildFeed(t, 1_791_599_000,
		tripUpdate("1", "trip-a", "201", pb.TripDescriptor_SCHEDULED,
			// Delay only.
			&pb.TripUpdate_StopTimeUpdate{
				StopSequence: proto.Uint32(1), StopId: proto.String("s1"),
				Arrival: &pb.TripUpdate_StopTimeEvent{Delay: proto.Int32(120)},
			},
			// Predicted time only, given as a departure (first-stop style).
			&pb.TripUpdate_StopTimeUpdate{
				StopSequence: proto.Uint32(2), StopId: proto.String("s2"),
				Departure: &pb.TripUpdate_StopTimeEvent{Time: proto.Int64(predicted)},
			},
			// Exactly on time: delay present but zero. Must not be dropped.
			&pb.TripUpdate_StopTimeUpdate{
				StopSequence: proto.Uint32(3), StopId: proto.String("s3"),
				Arrival: &pb.TripUpdate_StopTimeEvent{Delay: proto.Int32(0)},
			},
			// Skipped stop: dropped.
			&pb.TripUpdate_StopTimeUpdate{
				StopSequence: proto.Uint32(4), StopId: proto.String("s4"),
				ScheduleRelationship: pb.TripUpdate_StopTimeUpdate_SKIPPED.Enum(),
				Arrival:              &pb.TripUpdate_StopTimeEvent{Delay: proto.Int32(60)},
			},
			// No delay and no time: dropped.
			&pb.TripUpdate_StopTimeUpdate{
				StopSequence: proto.Uint32(5), StopId: proto.String("s5"),
				Arrival: &pb.TripUpdate_StopTimeEvent{Uncertainty: proto.Int32(30)},
			},
		),
		// Cancelled trip: dropped entirely.
		tripUpdate("2", "trip-b", "3", pb.TripDescriptor_CANCELED,
			&pb.TripUpdate_StopTimeUpdate{
				StopSequence: proto.Uint32(1), StopId: proto.String("x"),
				Arrival: &pb.TripUpdate_StopTimeEvent{Delay: proto.Int32(10)},
			},
		),
	)

	snap, err := DecodeTripUpdates(data, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1_791_599_000, 0); !snap.GeneratedAt.Equal(want) {
		t.Errorf("GeneratedAt = %v, want %v", snap.GeneratedAt, want)
	}
	if len(snap.Events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(snap.Events), snap.Events)
	}

	e := snap.Events[0]
	if !e.HasDelay || e.Delay != 120 || !e.Predicted.IsZero() || e.TripID != "trip-a" || e.RouteID != "201" || e.StartDate != "20261009" {
		t.Errorf("event 0 = %+v", e)
	}
	e = snap.Events[1]
	if e.HasDelay || !e.Predicted.Equal(time.Unix(predicted, 0)) || e.StopID != "s2" {
		t.Errorf("event 1 = %+v", e)
	}
	e = snap.Events[2]
	if !e.HasDelay || e.Delay != 0 || e.StopSequence != 3 {
		t.Errorf("event 2 (on time) = %+v", e)
	}
}

func TestDecodeTripUpdatesFallbackClock(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	snap, err := DecodeTripUpdates(buildFeed(t, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, want fallback %v", snap.GeneratedAt, now)
	}
}

func TestDecodeTripUpdatesGarbage(t *testing.T) {
	if _, err := DecodeTripUpdates([]byte("<html>not a feed</html>"), time.Now()); err == nil {
		t.Error("expected an error for non-protobuf input")
	}
}

func TestServiceDate(t *testing.T) {
	calgary, err := time.LoadLocation("America/Edmonton")
	if err != nil {
		t.Skipf("no tz database: %v", err)
	}
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name      string
		startDate string
		observed  time.Time
		want      time.Time
	}{
		{"feed date wins", "20261009", time.Date(2026, 10, 12, 18, 0, 0, 0, calgary), day(2026, 10, 9)},
		{"afternoon, no feed date", "", time.Date(2026, 10, 9, 15, 0, 0, 0, calgary), day(2026, 10, 9)},
		{"1 am belongs to yesterday", "", time.Date(2026, 10, 10, 1, 0, 0, 0, calgary), day(2026, 10, 9)},
		{"5 am is today", "", time.Date(2026, 10, 10, 5, 0, 0, 0, calgary), day(2026, 10, 10)},
		{"malformed feed date falls back", "2026-10-09", time.Date(2026, 10, 9, 15, 0, 0, 0, calgary), day(2026, 10, 9)},
		// 06:00 UTC is midnight in Calgary (MDT) — still the previous service day.
		{"UTC input converted to Calgary time", "", time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC), day(2026, 10, 9)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceDate(tt.startDate, tt.observed, calgary); !got.Equal(tt.want) {
				t.Errorf("ServiceDate() = %v, want %v", got, tt.want)
			}
		})
	}
}
