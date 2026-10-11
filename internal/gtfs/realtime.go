// Package gtfs fetches and decodes Calgary Transit's GTFS-Realtime feeds.
//
// GTFS-Realtime is a Google standard built on Protocol Buffers, a compact binary
// format, so the feed can't be read as JSON. We decode it with the official
// MobilityData bindings, then flatten the nested messages into plain Go structs
// so the rest of the app never has to know protobuf exists.
package gtfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// StopEvent is one stop on one trip, as reported in a single feed snapshot.
//
// For stops the bus hasn't reached yet these values are predictions that get
// more accurate as the bus gets closer. The last snapshot that mentions a stop
// is our best record of when the bus actually arrived there.
type StopEvent struct {
	TripID       string
	RouteID      string // may be empty; the schedule can fill it in later
	StartDate    string // service date as "YYYYMMDD", or "" if the feed omits it
	StopID       string
	StopSequence uint32

	// Agencies report lateness in one or both of two ways, and we can't assume
	// which one Calgary uses, so we keep both and decide later:
	//   Delay      seconds late (+) or early (-) vs. the schedule, valid if HasDelay
	//   Predicted  the absolute predicted arrival time, zero if not reported
	HasDelay  bool
	Delay     int32
	Predicted time.Time
}

// Snapshot is everything extracted from one fetch of the trip-updates feed.
type Snapshot struct {
	// GeneratedAt is when Calgary Transit produced this snapshot (from the feed
	// header). Using their timestamp instead of our clock means two polls that
	// receive the same unchanged feed can be recognised as duplicates.
	GeneratedAt time.Time
	Events      []StopEvent
}

// maxFeedBytes caps how much we'll read from the feed. A real snapshot is a few
// megabytes; the cap stops a broken or malicious server from making us read an
// endless response into memory.
const maxFeedBytes = 64 << 20 // 64 MiB

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Fetch downloads the raw bytes of a GTFS-Realtime feed.
func Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Identify ourselves so the data provider can tell who is polling them.
	req.Header.Set("User-Agent", "calgary-transit-tracker (+https://github.com/raraia/calgary-transit-tracker)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}

	// Read one byte past the limit so an oversized body is detected, not silently cut off.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFeedBytes {
		return nil, errors.New("feed larger than 64 MiB; refusing to parse")
	}
	return body, nil
}

// DecodeTripUpdates parses a trip-updates feed into a Snapshot.
//
// fallbackNow is used as GeneratedAt only if the feed header has no timestamp.
// Stops that can't tell us anything (skipped stops, cancelled trips, events
// with neither a delay nor a predicted time) are dropped here so callers only
// ever see usable data.
func DecodeTripUpdates(data []byte, fallbackNow time.Time) (Snapshot, error) {
	feed := &gtfs.FeedMessage{}
	if err := proto.Unmarshal(data, feed); err != nil {
		return Snapshot{}, fmt.Errorf("decoding protobuf: %w", err)
	}

	snap := Snapshot{GeneratedAt: fallbackNow}
	if ts := feed.GetHeader().GetTimestamp(); ts > 0 {
		snap.GeneratedAt = time.Unix(int64(ts), 0)
	}

	for _, entity := range feed.GetEntity() {
		tu := entity.GetTripUpdate()
		if tu == nil {
			continue // this entity is a vehicle position or alert, not a trip update
		}
		trip := tu.GetTrip()
		if trip.GetTripId() == "" {
			continue // without a trip_id we can't match it to the schedule
		}
		if trip.GetScheduleRelationship() == gtfs.TripDescriptor_CANCELED {
			// A cancelled trip has no arrivals to measure. Cancellations are a
			// reliability problem of their own and could be tracked separately.
			continue
		}

		for _, stu := range tu.GetStopTimeUpdate() {
			switch stu.GetScheduleRelationship() {
			case gtfs.TripUpdate_StopTimeUpdate_SKIPPED, gtfs.TripUpdate_StopTimeUpdate_NO_DATA:
				continue // the bus won't stop here, or the agency has no estimate
			}

			// Use the arrival estimate. The first stop of a trip often only has
			// a departure, so fall back to that.
			ev := stu.GetArrival()
			if ev == nil {
				ev = stu.GetDeparture()
			}
			if ev == nil {
				continue
			}

			e := StopEvent{
				TripID:       trip.GetTripId(),
				RouteID:      trip.GetRouteId(),
				StartDate:    trip.GetStartDate(),
				StopID:       stu.GetStopId(),
				StopSequence: stu.GetStopSequence(),
			}
			// Protobuf "optional" fields are pointers in Go: nil means "not sent".
			// That lets us tell "0 seconds late" apart from "delay not reported",
			// which GetDelay() alone can't (it returns 0 for both).
			if ev.Delay != nil {
				e.HasDelay = true
				e.Delay = *ev.Delay
			}
			if ev.Time != nil && *ev.Time > 0 {
				e.Predicted = time.Unix(*ev.Time, 0)
			}
			if !e.HasDelay && e.Predicted.IsZero() {
				continue // nothing to measure
			}
			snap.Events = append(snap.Events, e)
		}
	}
	return snap, nil
}
