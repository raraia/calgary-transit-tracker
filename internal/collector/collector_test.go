package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	pb "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/raraia/calgary-transit-tracker/internal/store"
	"google.golang.org/protobuf/proto"
)

// fakeSink records what the collector tried to store.
type fakeSink struct {
	calls  int
	visits []store.StopVisit
	seenAt time.Time
	err    error
}

func (f *fakeSink) UpsertStopVisits(_ context.Context, v []store.StopVisit, seenAt time.Time) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	f.visits, f.seenAt = v, seenAt
	return len(v), nil
}

// feedAt builds a one-trip feed whose header timestamp is ts.
func feedAt(t *testing.T, ts int64) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.FeedMessage{
		Header: &pb.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: proto.Uint64(uint64(ts))},
		Entity: []*pb.FeedEntity{{
			Id: proto.String("1"),
			TripUpdate: &pb.TripUpdate{
				Trip: &pb.TripDescriptor{TripId: proto.String("t1"), RouteId: proto.String("201")},
				StopTimeUpdate: []*pb.TripUpdate_StopTimeUpdate{{
					StopSequence: proto.Uint32(7),
					StopId:       proto.String("5555"),
					Arrival:      &pb.TripUpdate_StopTimeEvent{Delay: proto.Int32(90)},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestCollector(t *testing.T, fetch FetchFunc, sink Sink) *Collector {
	loc, err := time.LoadLocation("America/Edmonton")
	if err != nil {
		t.Skipf("no tz database: %v", err)
	}
	return New(fetch, sink, loc, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPollOnceStoresAndSkipsDuplicates(t *testing.T) {
	ts := time.Date(2026, 10, 9, 21, 0, 0, 0, time.UTC).Unix() // 3 pm in Calgary
	data := feedAt(t, ts)
	sink := &fakeSink{}
	c := newTestCollector(t, func(context.Context) ([]byte, error) { return data, nil }, sink)

	res, err := c.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || res.Stored != 1 || sink.calls != 1 {
		t.Fatalf("first poll: %+v, sink calls %d", res, sink.calls)
	}
	v := sink.visits[0]
	wantDate := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if v.TripID != "t1" || v.StopSequence != 7 || !v.HasDelay || v.Delay != 90 || !v.ServiceDate.Equal(wantDate) {
		t.Errorf("visit = %+v", v)
	}
	if !sink.seenAt.Equal(time.Unix(ts, 0)) {
		t.Errorf("seenAt = %v, want the feed header time", sink.seenAt)
	}

	// Same snapshot again: skipped, nothing written.
	res, err = c.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || sink.calls != 1 {
		t.Errorf("second poll should be skipped: %+v, sink calls %d", res, sink.calls)
	}
}

func TestPollOnceRetriesAfterStoreFailure(t *testing.T) {
	data := feedAt(t, 1_791_600_000)
	sink := &fakeSink{err: errors.New("db down")}
	c := newTestCollector(t, func(context.Context) ([]byte, error) { return data, nil }, sink)

	if _, err := c.PollOnce(context.Background()); err == nil {
		t.Fatal("expected the store error to be returned")
	}
	// The database comes back; the same snapshot must be written, not skipped.
	sink.err = nil
	res, err := c.PollOnce(context.Background())
	if err != nil || res.Skipped || res.Stored != 1 {
		t.Errorf("retry: %+v, %v", res, err)
	}
}

func TestPollOnceFetchError(t *testing.T) {
	sink := &fakeSink{}
	c := newTestCollector(t, func(context.Context) ([]byte, error) { return nil, errors.New("timeout") }, sink)
	if _, err := c.PollOnce(context.Background()); err == nil || sink.calls != 0 {
		t.Errorf("err = %v, sink calls = %d", err, sink.calls)
	}
}
