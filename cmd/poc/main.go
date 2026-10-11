// Command poc fetches the live Calgary Transit trip-updates feed once and
// prints a sample of what's in it. No database needed.
//
// Handy for checking the feed is reachable and seeing how Calgary reports
// lateness (as a delay, a predicted time, or both).
//
// Run:  go run ./cmd/poc
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/raraia/calgary-transit-tracker/internal/gtfs"
)

const tripUpdatesURL = "https://data.calgary.ca/download/gs4m-mdc2/application%2Foctet-stream"

func main() {
	data, err := gtfs.Fetch(context.Background(), tripUpdatesURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	snap, err := gtfs.DecodeTripUpdates(data, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	var withDelay, withTime int
	for _, e := range snap.Events {
		if e.HasDelay {
			withDelay++
		}
		if !e.Predicted.IsZero() {
			withTime++
		}
	}
	fmt.Printf("Snapshot generated %s: %d stop events\n", snap.GeneratedAt.Local().Format(time.RFC1123), len(snap.Events))
	fmt.Printf("  %d report a delay, %d report a predicted time\n\n", withDelay, withTime)

	fmt.Println("First 10:")
	for i, e := range snap.Events {
		if i == 10 {
			break
		}
		late := "delay n/a"
		if e.HasDelay {
			late = fmt.Sprintf("%+d sec", e.Delay)
		}
		at := "time n/a"
		if !e.Predicted.IsZero() {
			at = e.Predicted.Local().Format("15:04:05")
		}
		fmt.Printf("  route %-5s trip %-12s stop %-6s  %-10s  predicted %s\n", e.RouteID, e.TripID, e.StopID, late, at)
	}
}
