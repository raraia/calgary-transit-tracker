// Command loadschedule downloads Calgary Transit's static GTFS schedule and
// loads it into the database, replacing whatever schedule was there.
//
// The schedule supplies route names, stop names and scheduled arrival times.
// Re-run it whenever Calgary Transit publishes a new timetable (a few times a year).
//
// Usage:
//
//	go run ./cmd/loadschedule                  download and load
//	go run ./cmd/loadschedule -file gtfs.zip   load a zip you already have
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/raraia/calgary-transit-tracker/internal/schedule"
	"github.com/raraia/calgary-transit-tracker/internal/store"
)

const defaultScheduleURL = "https://data.calgary.ca/download/npk7-z3bj/application%2Fzip"

func main() {
	file := flag.String("file", "", "load this GTFS zip instead of downloading one")
	url := flag.String("url", env("GTFS_STATIC_URL", defaultScheduleURL), "where to download the GTFS zip from")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(context.Background(), log, *file, *url); err != nil {
		log.Error("schedule load failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, file, url string) error {
	path := file
	if path == "" {
		log.Info("downloading schedule", "url", url)
		tmp, err := download(ctx, url)
		if err != nil {
			return err
		}
		defer os.Remove(tmp)
		path = tmp
	}

	feed, err := schedule.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer feed.Close()
	tables, err := feed.Tables()
	if err != nil {
		return err
	}

	st, err := store.New(ctx, env("DATABASE_URL", "postgres://ctt:ctt@localhost:5432/ctt"))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	loads := make([]store.TableLoad, len(tables))
	for i, t := range tables {
		loads[i] = store.TableLoad{Table: t.Name, Columns: t.Columns, Rows: t.Rows}
	}

	start := time.Now()
	counts, err := st.ReplaceSchedule(ctx, loads)
	if err != nil {
		return err
	}
	log.Info("schedule loaded",
		"routes", counts["routes"], "stops", counts["stops"],
		"trips", counts["trips"], "stop_times", counts["stop_times"],
		"took", time.Since(start).Round(time.Millisecond))
	return nil
}

// download saves the zip to a temporary file. archive/zip needs to jump around
// inside the file (the zip's index is at the end), so it can't read straight
// from the network stream.
func download(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "gtfs-*.zip")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 1<<30)); err != nil { // 1 GiB sanity cap
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
