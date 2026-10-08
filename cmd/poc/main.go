// Command poc is a one-shot proof of concept: it hits the live Calgary Transit
// trip-updates feed once and prints a few current delay readings.
//
// This is your "week 1 win" — run it and you'll see real buses and how late
// they are, right now, with no database needed.
//
// Run:  go run ./cmd/poc
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/raraia/calgary-transit-tracker/internal/gtfs"
)

const tripUpdatesURL = "https://data.calgary.ca/download/gs4m-mdc2/application%2Foctet-stream"

func main() {
	delays, err := gtfs.FetchTripUpdates(context.Background(), tripUpdatesURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Printf("Fetched %d stop delay readings from Calgary Transit.\n\n", len(delays))
	fmt.Println("Showing the first 10:")
	for i, d := range delays {
		if i >= 10 {
			break
		}
		fmt.Printf("  route %-5s trip %-12s stop %-6s  %+d sec\n",
			d.RouteID, d.TripID, d.StopID, d.DelaySeconds)
	}
}
