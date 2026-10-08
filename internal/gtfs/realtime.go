// Package gtfs fetches and decodes Calgary Transit's GTFS-Realtime feeds.
//
// GTFS-Realtime is a Google standard. The feed is NOT JSON — it's a binary
// format called Protocol Buffers. We use the official bindings library so we
// don't have to run protoc or parse bytes by hand.
package gtfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// StopDelay is one reading: a trip's lateness at one stop, at one moment.
type StopDelay struct {
	TripID       string
	RouteID      string
	StopID       string
	StopSequence int32
	DelaySeconds int32 // + late, - early
}

// FetchTripUpdates downloads the trip-updates feed and pulls out every
// stop-level delay reading it contains.
func FetchTripUpdates(ctx context.Context, url string) ([]StopDelay, error) {
	body, err := fetch(ctx, url)
	if err != nil {
		return nil, err
	}

	// Unmarshal the protobuf bytes into the generated FeedMessage struct.
	feed := &gtfs.FeedMessage{}
	if err := proto.Unmarshal(body, feed); err != nil {
		return nil, fmt.Errorf("decoding protobuf: %w", err)
	}

	var out []StopDelay
	// A feed has many "entities"; the ones we want carry a TripUpdate.
	for _, ent := range feed.GetEntity() {
		tu := ent.GetTripUpdate()
		if tu == nil {
			continue
		}
		trip := tu.GetTrip()
		// Each TripUpdate lists updates for the stops still ahead of the bus.
		for _, stu := range tu.GetStopTimeUpdate() {
			arr := stu.GetArrival()
			if arr == nil {
				continue // no arrival estimate for this stop; skip
			}
			out = append(out, StopDelay{
				TripID:       trip.GetTripId(),
				RouteID:      trip.GetRouteId(),
				StopID:       stu.GetStopId(),
				StopSequence: int32(stu.GetStopSequence()),
				DelaySeconds: arr.GetDelay(), // seconds; GTFS defines + as late
			})
		}
	}
	return out, nil
}

// fetch does the HTTP GET with a timeout and returns the raw bytes.
func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
